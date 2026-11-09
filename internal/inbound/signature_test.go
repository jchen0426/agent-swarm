package inbound_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/inbound"
)

var now = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

const maxAge = 5 * time.Minute

var body = []byte(`{"alerts":[]}`)

func sign(secret, ts string, b []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "." + string(b)))
	return hex.EncodeToString(mac.Sum(nil))
}

func stamp(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) }

func headers(ts, sig string) http.Header {
	h := http.Header{}
	if ts != "" {
		h.Set(inbound.HeaderTimestamp, ts)
	}
	if sig != "" {
		h.Set(inbound.HeaderSignature, sig)
	}
	return h
}

func TestASignatureOverTheTimestampAndBodyIsAccepted(t *testing.T) {
	ts := stamp(now)
	h := headers(ts, "v1="+sign("s3cret", ts, body))
	require.NoError(t, inbound.Verify([]string{"s3cret"}, h, body, now, maxAge))
}

func TestAnySignatureListedMayMatchAnyCurrentSecret(t *testing.T) {
	ts := stamp(now)
	secrets := []string{"new", "old"}
	byOld := "v1=" + sign("old", ts, body)
	byNew := "v1=" + sign("new", ts, body)
	garbage := "v1=" + strings.Repeat("0", 64)
	for name, sig := range map[string]string{
		"by the old secret":      byOld,
		"garbage then good":      garbage + "," + byNew,
		"v0 part before good v1": "v0=" + sign("new", ts, body) + " , " + byNew,
	} {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, inbound.Verify(secrets, headers(ts, sig), body, now, maxAge))
		})
	}
}

func TestABadOrMissingSignatureIsRefused(t *testing.T) {
	ts := stamp(now)
	good := sign("s3cret", ts, body)
	cases := map[string]http.Header{
		"no headers":            http.Header{},
		"no signature":          headers(ts, ""),
		"no timestamp":          headers("", "v1="+good),
		"timestamp 12a":         headers("12a", "v1="+sign("s3cret", "12a", body)),
		"timestamp too long":    headers("1234567890123", "v1="+sign("s3cret", "1234567890123", body)),
		"other body":            headers(ts, "v1="+sign("s3cret", ts, []byte("other"))),
		"other secret":          headers(ts, "v1="+sign("nope", ts, body)),
		"uppercase hex":         headers(ts, "v1="+strings.ToUpper(good)),
		"only v0":               headers(ts, "v0="+good),
		"63 hex characters":     headers(ts, "v1="+good[:63]),
		"65 hex characters":     headers(ts, "v1="+good+"0"),
		"signature of other ts": headers(ts, "v1="+sign("s3cret", "1", body)),
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			err := inbound.Verify([]string{"s3cret"}, h, body, now, maxAge)
			require.ErrorIs(t, err, inbound.ErrBadSignature)
			assert.NotContains(t, err.Error(), good)
			assert.NotContains(t, err.Error(), "s3cret")
		})
	}
}

func TestATimestampOutsideMaxAgeEitherWayIsStale(t *testing.T) {
	for name, tc := range map[string]struct {
		at    time.Time
		stale bool
	}{
		"past by 5 min and 1 s":   {now.Add(-maxAge - time.Second), true},
		"future by 5 min and 1 s": {now.Add(maxAge + time.Second), true},
		"past by exactly 5 min":   {now.Add(-maxAge), false},
		"future by exactly 5 min": {now.Add(maxAge), false},
	} {
		t.Run(name, func(t *testing.T) {
			ts := stamp(tc.at)
			h := headers(ts, "v1="+sign("s3cret", ts, body))
			err := inbound.Verify([]string{"s3cret"}, h, body, now, maxAge)
			if tc.stale {
				require.ErrorIs(t, err, inbound.ErrStale)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestAStaleTimestampWithABadSignatureIsBadNotStale(t *testing.T) {
	ts := stamp(now.Add(-time.Hour))
	h := headers(ts, "v1="+sign("nope", ts, body))
	require.ErrorIs(t, inbound.Verify([]string{"s3cret"}, h, body, now, maxAge), inbound.ErrBadSignature)
}

func TestTheSecretFileHoldsOneOrTwoValidLines(t *testing.T) {
	for name, tc := range map[string]struct {
		text string
		want []string
	}{
		"one":                 {"a", []string{"a"}},
		"one with newline":    {"a\n", []string{"a"}},
		"two with CRLF":       {"a\r\nb", []string{"a", "b"}},
		"two with final CRLF": {"a\r\nb\r\n", []string{"a", "b"}},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := inbound.Secrets(tc.text)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
	for name, text := range map[string]string{
		"empty":        "",
		"blank middle": "a\n\nb",
		"three":        "a\nb\nc",
		"space":        "a b",
		"control":      "a\x00",
		"only newline": "\n",
	} {
		t.Run("refuses "+name, func(t *testing.T) {
			_, err := inbound.Secrets(text)
			require.ErrorIs(t, err, inbound.ErrSecretFile)
			assert.NotContains(t, err.Error(), "a b")
		})
	}
}
