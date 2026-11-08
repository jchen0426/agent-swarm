package origin_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jchen0426/agent-swarm/internal/web/origin"
)

// Host folds ASCII case alone, drops one trailing dot and the default port,
// writes an IP address canonically with IPv6 in brackets, and refuses, as
// empty, a host past ASCII, percent-encoded, or that it cannot read
// (R95; m15).
func TestHostNormalizesASCIIAndRefusesTheRest(t *testing.T) {
	for in, want := range map[string]string{
		"Agents.Example.COM":      "agents.example.com",
		"agents.example.com.":     "agents.example.com",
		"agents.example.com:443":  "agents.example.com",
		"agents.example.com.:443": "agents.example.com",
		"agents.example.com:8443": "agents.example.com:8443",
		"agents.example.com:80":   "agents.example.com:80",
		"[2001:DB8::1]":           "[2001:db8::1]",
		"[2001:db8:0:0::1]:443":   "[2001:db8::1]",
		"[2001:db8::1]:8443":      "[2001:db8::1]:8443",
		"[::ffff:1.2.3.4]":        "[::ffff:1.2.3.4]",
		"127.0.0.1:443":           "127.0.0.1",
		"KEY.example.com":         "key.example.com",
		"z.example.com":           "z.example.com",
		"[@]":                     "",
		"":                        "",
		"Key.example.com":         "",
		"kéy.example.com":         "",
		"ſey.example.com":         "",
		"key.example.com。":        "",
		"%4Bey.example.com":       "",
		"key%2eexample.com":       "",
		"[fe80::1%25en0]":         "",
		"2001:db8::1":             "",
		"[2001:db8::1":            "",
		"agents.example.com..":    "",
		"user@agents.example.com": "",
		"agents.example.com/x":    "",
		"agents.example.com\\x":   "",
		"agents example.com":      "",
		"agents.example.com\x00":  "",
		"agents.example.com\x7f":  "",
		":443":                    "",
	} {
		assert.Equal(t, want, origin.Host(in, "443"), "%q", in)
	}
	assert.Equal(t, "agents.example.com", origin.Host("agents.example.com:80", "80"), "http's default port")
	assert.Equal(t, "agents.example.com:443", origin.Host("agents.example.com:443", "80"), "443 is not http's default")
	// Only A to Z fold: [, \, ], ^, _ and ` sit between Z and a in ASCII
	// and are left as they are.
	assert.Equal(t, "a_b.example.com", origin.Host("A_B.example.com", "443"))
}

func TestDefaultPort(t *testing.T) {
	assert.Equal(t, "80", origin.DefaultPort("http"))
	assert.Equal(t, "80", origin.DefaultPort("HTTP"))
	assert.Equal(t, "443", origin.DefaultPort("https"))
}

// Of is a URL's normalized origin; exact, as a browser's Origin header is
// compared, it refuses anything after the host (R96).
func TestOfIsTheNormalizedOrigin(t *testing.T) {
	for _, tc := range []struct {
		in           string
		loose, exact string
	}{
		{"https://agents.example.com", "https://agents.example.com", "https://agents.example.com"},
		{"HTTPS://Agents.Example.com:443", "https://agents.example.com", "https://agents.example.com"},
		{"http://agents.example.com:80", "http://agents.example.com", "http://agents.example.com"},
		{"http://agents.example.com:443", "http://agents.example.com:443", "http://agents.example.com:443"},
		{"https://agents.example.com.", "https://agents.example.com", "https://agents.example.com"},
		{"https://[2001:DB8::1]:443", "https://[2001:db8::1]", "https://[2001:db8::1]"},
		{"https://agents.example.com/", "https://agents.example.com", ""},
		{"https://agents.example.com/items", "https://agents.example.com", ""},
		{"https://agents.example.com?", "https://agents.example.com", ""},
		{"https://agents.example.com?x=1", "https://agents.example.com", ""},
		{"https://agents.example.com#", "https://agents.example.com", ""},
		{"https://agents.example.com#x", "https://agents.example.com", ""},
		{"https://agents.example.com:", "https://agents.example.com", "https://agents.example.com"},
		{"null", "", ""},
		{"", "", ""},
		{"agents.example.com", "", ""},
		{"ftp://agents.example.com", "", ""},
		{"https://user@agents.example.com", "", ""},
		{"https://Key.example.com", "", ""},
		{"https://%E2%84%AAey.example.com", "", ""},
		{"https://%61gents.example.com", "", ""},
		{"https://agents.example.com/%2e", "", ""},
		{"https://agents.example.com..", "", ""},
		{"https:agents.example.com", "", ""},
	} {
		assert.Equal(t, tc.loose, origin.Of(tc.in, false), "loose %q", tc.in)
		assert.Equal(t, tc.exact, origin.Of(tc.in, true), "exact %q", tc.in)
	}
}
