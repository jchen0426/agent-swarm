package auth

import (
	"net/url"
	"path"
	"strings"
)

// maxReturn bounds a return path's length.
const maxReturn = 2048

// SafeReturn says whether p may be a return path, a page on this site to
// send a person back to after sign-in, and gives it back unchanged if so
// (design D4; n7). It must be at most 2048 bytes, start with a single /,
// hold no //, backslash, control character, byte past ASCII, or
// percent-encoded /, \ or control character, parse with
// no scheme, user or host, and not lead, its dot segments resolved, to
// /auth or under it, sign-in's own routes.
func SafeReturn(p string) (string, bool) {
	if !plainPath(p) {
		return "", false
	}
	u, err := url.Parse(p)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" {
		return "", false
	}
	if clean := path.Clean(u.Path); clean == "/auth" || strings.HasPrefix(clean, "/auth/") {
		return "", false
	}
	return p, true
}

// plainPath says whether p is a path that starts with a single / and holds
// no //, backslash, control character, byte past ASCII, percent-encoded /
// or \, or percent-encoded control character.
func plainPath(p string) bool {
	if p == "" || len(p) > maxReturn || p[0] != '/' || strings.Contains(p, "//") {
		return false
	}
	for i := range len(p) {
		if c := p[i]; c < 0x20 || c >= 0x7f || c == '\\' {
			return false
		}
	}
	return !refusedEscape(strings.ToLower(p))
}

// refusedEscape says whether lower, a lowercase path, holds a
// percent-encoded /, \ or control character, %00 to %1f or %7f.
func refusedEscape(lower string) bool {
	if strings.Contains(lower, "%2f") || strings.Contains(lower, "%5c") || strings.Contains(lower, "%7f") {
		return true
	}
	for i := 0; i+2 < len(lower); i++ {
		if lower[i] == '%' && (lower[i+1] == '0' || lower[i+1] == '1') && isHex(lower[i+2]) {
			return true
		}
	}
	return false
}

func isHex(c byte) bool { return ('0' <= c && c <= '9') || ('a' <= c && c <= 'f') }
