package auth

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"
)

// Role is a deployment role a person holds through the groups claim.
type Role string

// The deployment roles (design D4).
const (
	Viewer    Role = "viewer"
	Responder Role = "responder"
	Admin     Role = "admin"
)

// CookieName is the session cookie's name: a __Host- cookie, Secure, with
// Path=/ and no Domain, so that no other host or path can set it (M3).
const CookieName = "__Host-session"

// sessionAAD binds a sealed session to its use and format.
const sessionAAD = "agent-swarm web session cookie v1"

// Session is a signed-in person, as the session cookie seals it (design
// D4): the deployment's issuer and client ID, the subject claim's value, the
// roles, when the person last authenticated at the provider (auth_time,
// zero if the provider gave none), and the session's issue time and expiry.
// Times are kept to the second.
type Session struct {
	Issuer   string
	ClientID string
	Subject  string
	Roles    []Role
	AuthTime time.Time
	IssuedAt time.Time
	Expires  time.Time
}

// Has says whether the session holds role r.
func (s *Session) Has(r Role) bool { return slices.Contains(s.Roles, r) }

// sealed is a session's form inside the seal.
type sealed struct {
	Issuer   string `json:"iss"`
	ClientID string `json:"aud"`
	Subject  string `json:"sub"`
	Roles    []Role `json:"roles,omitempty"`
	AuthTime int64  `json:"auth_time,omitempty"`
	IssuedAt int64  `json:"iat"`
	Expires  int64  `json:"exp"`
}

// errForeign is a session for another deployment than the keys'.
var errForeign = errors.New("the session is for another deployment")

// Seal seals s under k with AES-256-GCM, a fresh nonce each time, in
// base64url: the session cookie's value. A session whose issuer or client
// ID is not k's deployment's is refused.
func (s *Session) Seal(k Keys) (string, error) {
	if k.set == nil {
		return "", errors.New("no session key")
	}
	if s.Issuer != k.set.issuer || s.ClientID != k.set.clientID {
		return "", errForeign
	}
	plain, err := json.Marshal(sealed{
		Issuer: s.Issuer, ClientID: s.ClientID, Subject: s.Subject, Roles: s.Roles,
		AuthTime: unix(s.AuthTime), IssuedAt: unix(s.IssuedAt), Expires: unix(s.Expires),
	})
	if err != nil {
		return "", fmt.Errorf("encode the session: %w", err)
	}
	aead := k.set.session
	nonce := make([]byte, aead.NonceSize(), aead.NonceSize()+len(plain)+aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("a nonce: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, plain, []byte(sessionAAD))), nil
}

// Open opens a session cookie's value under k: one sealed under another
// key, tampered with, or for another deployment than k's is refused (M3).
// It does not judge the session's expiry. Its errors never repeat the value.
func Open(k Keys, value string) (Session, error) {
	if k.set == nil {
		return Session{}, errors.New("no session key")
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return Session{}, errors.New("the session cookie is not base64url")
	}
	aead := k.set.session
	n := aead.NonceSize()
	if len(raw) < n+aead.Overhead() {
		return Session{}, errors.New("the session cookie is too short")
	}
	plain, err := aead.Open(nil, raw[:n], raw[n:], []byte(sessionAAD))
	if err != nil {
		return Session{}, errors.New("the session cookie does not open under the session key")
	}
	var v sealed
	if err := json.Unmarshal(plain, &v); err != nil {
		return Session{}, errors.New("the session cookie's content does not decode")
	}
	if v.Issuer != k.set.issuer || v.ClientID != k.set.clientID {
		return Session{}, errForeign
	}
	return Session{
		Issuer: v.Issuer, ClientID: v.ClientID, Subject: v.Subject, Roles: v.Roles,
		AuthTime: fromUnix(v.AuthTime), IssuedAt: fromUnix(v.IssuedAt), Expires: fromUnix(v.Expires),
	}, nil
}

// unix is t in Unix seconds, 0 for the zero time.
func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// fromUnix is the UTC time of Unix seconds, the zero time for 0.
func fromUnix(s int64) time.Time {
	if s == 0 {
		return time.Time{}
	}
	return time.Unix(s, 0).UTC()
}

// SessionCookie is the session cookie holding value, a sealed session that
// expires at expires (design D4; M3).
func SessionCookie(value string, expires time.Time) *http.Cookie {
	return sessionCookie(value, expires, time.Now())
}

// sessionCookie is SessionCookie at now.
func sessionCookie(value string, expires, now time.Time) *http.Cookie {
	maxAge := int(expires.Sub(now) / time.Second)
	if maxAge <= 0 {
		maxAge = -1
	}
	return &http.Cookie{
		Name: CookieName, Value: value, Path: "/", Expires: expires.UTC(), MaxAge: maxAge,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	}
}

// clearedCookie is the session cookie's deletion.
func clearedCookie() *http.Cookie {
	return &http.Cookie{
		Name: CookieName, Path: "/", MaxAge: -1, Expires: time.Unix(0, 0),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	}
}
