package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Secrets reads a secret by its reference, at use (the secret store serve
// opens).
type Secrets interface {
	Secret(ctx context.Context, ref string) (string, error)
}

// The session key's bounds and lifetime (R86).
const (
	// minKeyBytes is the fewest bytes the session key may decode to.
	minKeyBytes = 32
	// keyCache is how long a key read is used before it is read again: a
	// rotated key signs everyone out within it.
	keyCache = time.Minute
	// failureLog is how often a key that cannot be read is logged at most.
	failureLog = time.Minute
)

// The HKDF info strings of the three subkeys (R86): one key never both
// seals and signs.
const (
	sessionInfo = "agent-swarm web session v1"
	csrfInfo    = "agent-swarm web csrf v1"
	formsInfo   = "agent-swarm web forms v1"
)

// Keys are one deployment's keys, derived by HKDF with SHA-256 from the
// secret at web.sessionKeyRef (R86): the session's AES-256-GCM key, the
// CSRF token's HMAC key and the form keys' HMAC key. They are bound to the
// deployment's issuer and client ID: a session for another deployment is
// neither sealed nor opened under them (M3). Both instances of a deployment
// share them, since they share the secret (M5).
// The zero Keys holds none: it seals, opens and signs nothing.
type Keys struct{ set *keySet }

type keySet struct {
	issuer, clientID string
	session          cipher.AEAD
	csrf, forms      []byte
}

// NewKeys derives the keys of the deployment of issuer and clientID from
// secret, the session key as the secret store holds it: base64 (std) of at
// least 32 random bytes, with surrounding whitespace trimmed (m22). Its
// errors never repeat the secret.
func NewKeys(secret, issuer, clientID string) (Keys, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(secret))
	if err != nil {
		return Keys{}, errors.New("the session key is not base64 (std)")
	}
	if len(raw) < minKeyBytes {
		return Keys{}, fmt.Errorf("the session key holds %d bytes, fewer than %d", len(raw), minKeyBytes)
	}
	derive := func(info string) ([]byte, error) { return hkdf.Key(sha256.New, raw, nil, info, 32) }
	sk, err := derive(sessionInfo)
	if err != nil {
		return Keys{}, fmt.Errorf("derive the session key: %w", err)
	}
	ck, err := derive(csrfInfo)
	if err != nil {
		return Keys{}, fmt.Errorf("derive the CSRF key: %w", err)
	}
	fk, err := derive(formsInfo)
	if err != nil {
		return Keys{}, fmt.Errorf("derive the forms key: %w", err)
	}
	block, err := aes.NewCipher(sk)
	if err != nil {
		return Keys{}, fmt.Errorf("the session cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return Keys{}, fmt.Errorf("the session cipher: %w", err)
	}
	return Keys{&keySet{issuer: issuer, clientID: clientID, session: aead, csrf: ck, forms: fk}}, nil
}

// Issuer and ClientID are the deployment the keys are bound to.
func (k Keys) Issuer() string {
	if k.set == nil {
		return ""
	}
	return k.set.issuer
}

func (k Keys) ClientID() string {
	if k.set == nil {
		return ""
	}
	return k.set.clientID
}

// Token is s's CSRF form token: the HMAC, under the CSRF key, of the
// session's issuer, client ID, subject and issue time, in base64url. It is
// the same for every form of one sign-in, and differs for every other.
func (k Keys) Token(s *Session) string {
	if k.set == nil {
		return ""
	}
	return mac(k.set.csrf, s.Issuer, s.ClientID, s.Subject, strconv.FormatInt(s.IssuedAt.Unix(), 10))
}

// FormMAC is a form key's MAC (R85): the HMAC, under the forms key, of the
// subject, the act, the item and the key k, in base64url.
func (k Keys) FormMAC(subject, act, item, key string) string {
	if k.set == nil {
		return ""
	}
	return mac(k.set.forms, subject, act, item, key)
}

// ValidForm says whether got is the form key's MAC, compared in constant
// time.
func (k Keys) ValidForm(subject, act, item, key, got string) bool {
	want := k.FormMAC(subject, act, item, key)
	return want != "" && hmac.Equal([]byte(got), []byte(want))
}

// mac is the HMAC-SHA256 under key of fields, each prefixed by its length,
// so that no two lists of fields give one message, in base64url.
func mac(key []byte, fields ...string) string {
	if len(key) == 0 {
		return ""
	}
	h := hmac.New(sha256.New, key)
	var n [8]byte
	for _, f := range fields {
		binary.BigEndian.PutUint64(n[:], uint64(len(f)))
		_, _ = h.Write(n[:])
		_, _ = h.Write([]byte(f))
	}
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// keyring reads the session key at use, through the secret store, and
// keeps it at most keyCache (R86). A key that cannot be read, or reads
// malformed, is an error, whatever was read before: sign-in fails closed.
// The failure is logged at most once each failureLog.
type keyring struct {
	secrets          Secrets
	ref              string
	issuer, clientID string
	clock            func() time.Time
	log              *slog.Logger

	mu     sync.Mutex
	keys   Keys
	read   time.Time // when keys were read; zero for none
	logged time.Time // when a failure was last logged; zero for never
}

// errUnavailable is a key that cannot be read, as a page sees it.
var errUnavailable = errors.New("the session key could not be read")

func (r *keyring) get(ctx context.Context) (Keys, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.clock()
	if !r.read.IsZero() && now.Sub(r.read) < keyCache && !now.Before(r.read) {
		return r.keys, nil
	}
	r.keys, r.read = Keys{}, time.Time{}
	secret, err := r.secrets.Secret(ctx, r.ref)
	var k Keys
	if err == nil {
		k, err = NewKeys(secret, r.issuer, r.clientID)
	}
	if err != nil {
		if r.logged.IsZero() || now.Sub(r.logged) >= failureLog || now.Before(r.logged) {
			r.logged = now
			r.log.ErrorContext(ctx, "the session key could not be read: sign-in is unavailable", "ref", r.ref, "err", err)
		}
		return Keys{}, errUnavailable
	}
	r.keys, r.read = k, now
	return k, nil
}
