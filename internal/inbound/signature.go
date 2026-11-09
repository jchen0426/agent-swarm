// Package inbound holds the platform's inbound-event machinery that is not
// the pure mapping: here, the request signature (spec Section 3.6).
package inbound

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jchen0426/agent-swarm/internal/registry/config"
)

// The header names a signed request carries.
const (
	HeaderTimestamp = "X-Agentswarm-Timestamp"
	HeaderSignature = "X-Agentswarm-Signature"
)

// The errors never hold a header, a body or a secret.
var (
	ErrBadSignature = errors.New("inbound: the request is not signed by a current secret")
	ErrStale        = errors.New("inbound: the request's timestamp is outside its maximum age")
	ErrSecretFile   = errors.New("inbound: the secret file does not hold one or two secrets")
)

const (
	maxTimestampDigits = 12
	signaturePrefix    = "v1="
	signatureHexLen    = 2 * sha256.Size
	// maxCandidates caps the signatures tried; the rest are ignored.
	maxCandidates = 8
)

// Secrets parses a secret file's text, as files.Store.Secret returns it: one
// or two lines (the current secret and, during a rollover, the previous one),
// each non-empty once a trailing carriage return is cut, with no space and no
// unseen character. One final line ending is allowed.
func Secrets(text string) ([]string, error) {
	text = strings.TrimSuffix(text, "\n")
	lines := strings.Split(text, "\n")
	if len(lines) > 2 {
		return nil, ErrSecretFile
	}
	secrets := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if line == "" || strings.ContainsFunc(line, func(r rune) bool { return r == ' ' || config.Unseen(r) }) {
			return nil, ErrSecretFile
		}
		secrets = append(secrets, line)
	}
	return secrets, nil
}

// Verify checks h's signature over "<timestamp>.<body>" against secrets, then
// the timestamp against now, within maxAge either way. The signature is
// checked first, so an unsigned caller learns nothing of the clock (R124).
// An empty secret is skipped, as a key anyone knows, and at most the first
// maxCandidates well-formed signatures are tried.
func Verify(secrets []string, h http.Header, body []byte, now time.Time, maxAge time.Duration) error {
	ts := h.Get(HeaderTimestamp)
	unix, ok := parseTimestamp(ts)
	if !ok {
		return ErrBadSignature
	}
	var candidates [][]byte
	for part := range strings.SplitSeq(h.Get(HeaderSignature), ",") {
		if c, ok := candidate(strings.TrimSpace(part)); ok {
			candidates = append(candidates, c)
			if len(candidates) == maxCandidates {
				break
			}
		}
	}
	if len(candidates) == 0 {
		return ErrBadSignature
	}
	matched := false
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(ts))
		mac.Write([]byte("."))
		mac.Write(body)
		sum := mac.Sum(nil)
		for _, c := range candidates {
			if hmac.Equal(c, sum) {
				matched = true
			}
		}
	}
	if !matched {
		return ErrBadSignature
	}
	if d := now.Sub(time.Unix(unix, 0)); d > maxAge || d < -maxAge {
		return ErrStale
	}
	return nil
}

// parseTimestamp accepts 1 to 12 decimal digits.
func parseTimestamp(s string) (int64, bool) {
	if s == "" || len(s) > maxTimestampDigits {
		return 0, false
	}
	var n int64
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int64(s[i]-'0')
	}
	return n, true
}

// candidate decodes a "v1=" part of 64 lowercase hex characters.
func candidate(part string) ([]byte, bool) {
	hexPart, ok := strings.CutPrefix(part, signaturePrefix)
	if !ok || len(hexPart) != signatureHexLen {
		return nil, false
	}
	for i := range len(hexPart) {
		c := hexPart[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return nil, false
		}
	}
	b, err := hex.DecodeString(hexPart)
	return b, err == nil
}
