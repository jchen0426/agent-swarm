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
//
// The store is read outside the lock, one read at a time (m2). From
// refreshAfter on, one request refreshes the key while the others go on
// with the cached one; past keyCache, every request waits for the one read
// in flight, or for its own end. The read runs in its own goroutine, on a
// context of its own, so that every request, the one that began it
// included, returns on its own context.
type keyring struct {
	secrets          Secrets
	ref              string
	issuer, clientID string
	clock            func() time.Time
	log              *slog.Logger

	mu     sync.Mutex
	keys   Keys
	read   time.Time // when the read of keys began; zero for none
	logged time.Time // when a failure was last logged; zero for never
	flight *flight   // the read in flight, or nil
}

// refreshAfter is the age of a cached key from which one request reads it
// again, before it lapses at keyCache.
const refreshAfter = 50 * time.Second

// flight is one read of the key, which the requests that wait share.
type flight struct {
	done chan struct{}
	keys Keys
	err  error
}

// errUnavailable is a key that cannot be read, as a page sees it.
var errUnavailable = errors.New("the session key could not be read")

func (r *keyring) get(ctx context.Context) (Keys, error) {
	r.mu.Lock()
	now := r.clock()
	age := now.Sub(r.read)
	cached := !r.read.IsZero() && age >= 0 && age < keyCache
	if cached && (age < refreshAfter || r.flight != nil) {
		k := r.keys
		r.mu.Unlock()
		return k, nil
	}
	f := r.flight
	if f == nil {
		f = &flight{done: make(chan struct{})}
		r.flight = f
		go r.refresh(ctx, f, now)
	}
	r.mu.Unlock()
	select {
	case <-f.done:
		return f.keys, f.err
	case <-ctx.Done():
		return Keys{}, errUnavailable
	}
}

// readTimeout bounds one read of the key, which runs on its own context,
// so that the request that began it ending does not fail the requests that
// wait on it.
const readTimeout = 5 * time.Second

// refresh reads the key for f, which began at began, outside the lock, on a
// context of its own, bounded by readTimeout, that keeps ctx's values but
// not its end. It then keeps what it read, or drops the cached key and logs
// the failure. A store that panics is a failure too: the read in flight is
// always cleared and its waiters always told.
func (r *keyring) refresh(ctx context.Context, f *flight, began time.Time) {
	var (
		k   Keys
		err error
	)
	defer func() {
		if p := recover(); p != nil {
			k, err = Keys{}, fmt.Errorf("the secret store panicked: %v", p)
		}
		r.settle(ctx, f, began, k, err)
	}()
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), readTimeout)
	defer cancel()
	secret, err := r.secrets.Secret(readCtx, r.ref)
	if err == nil {
		k, err = NewKeys(secret, r.issuer, r.clientID)
	}
}

// settle ends the read f: it keeps k, read at began, or, on err, drops the
// cached key and logs the failure at most once each failureLog, and tells
// the waiters.
func (r *keyring) settle(ctx context.Context, f *flight, began time.Time, k Keys, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	defer close(f.done)
	r.flight = nil
	if err != nil {
		r.keys, r.read = Keys{}, time.Time{}
		if now := r.clock(); r.logged.IsZero() || now.Sub(r.logged) >= failureLog || now.Before(r.logged) {
			r.logged = now
			r.log.ErrorContext(ctx, "the session key could not be read: sign-in is unavailable", "ref", r.ref, "err", err)
		}
		f.err = errUnavailable
		return
	}
	r.keys, r.read = k, began
	f.keys = k
}
