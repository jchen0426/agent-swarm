// Package origin normalizes hosts and origins as the web interface's Host
// check and its CSRF gate compare them (Plan 9a-1, R95 and R96; m15): a
// default port stripped, ASCII case folded, one trailing dot dropped, and an
// IP address in its canonical form, IPv6 in brackets. Only ASCII is read: a
// host holding any other byte is refused, never folded, so that a Unicode
// letter that folds to an ASCII one, as the Kelvin sign U+212A folds to k,
// never matches the public URL's host.
package origin

import (
	"net"
	"net/netip"
	"net/url"
	"strings"
)

// DefaultPort is a scheme's default port: 80 for http, 443 for any other.
func DefaultPort(scheme string) string {
	if strings.EqualFold(scheme, "http") {
		return "80"
	}
	return "443"
}

// Host is hostport, a host or host:port, normalized, with def, the scheme's
// default port, stripped. It is empty for a host it cannot read: an empty
// one, one with a byte past ASCII, an IPv6 address out of brackets, or one
// with more than one trailing dot, a user, a slash, a backslash, a space or a
// percent sign.
func Host(hostport, def string) string {
	hostport, ok := lowerASCII(hostport)
	if !ok {
		return ""
	}
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		host, port = hostport, ""
		if strings.HasPrefix(host, "[") {
			if !strings.HasSuffix(host, "]") {
				return ""
			}
			host = host[1 : len(host)-1]
		} else if strings.Contains(host, ":") {
			// An IPv6 address out of its brackets, or a port with no host.
			return ""
		}
	}
	if port == def {
		port = ""
	}
	host = strings.TrimSuffix(host, ".")
	if host == "" || strings.HasSuffix(host, ".") || strings.ContainsAny(host, "@/\\ %") {
		return ""
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		host = ip.String()
		if ip.Is6() {
			host = "[" + host + "]"
		}
	}
	if port != "" {
		return host + ":" + port
	}
	return host
}

// Of is the normalized origin, scheme://host[:port], of an absolute URL: of
// web.publicURL, or of a request's Origin header. It is empty for a value
// that is no http or https origin: one that does not parse, has a byte past
// ASCII or a percent sign, a user, a host it cannot read, or, when exact,
// anything after the host, since a browser's Origin header is the origin
// alone.
func Of(raw string, exact bool) string {
	if _, ok := lowerASCII(raw); !ok || strings.Contains(raw, "%") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	if exact && (u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "") {
		return ""
	}
	host := Host(u.Host, DefaultPort(scheme))
	if host == "" {
		return ""
	}
	return scheme + "://" + host
}

// lowerASCII is s with A to Z folded to a to z, and false if s holds a byte
// past ASCII or a control character.
func lowerASCII(s string) (string, bool) {
	b := []byte(s)
	for i, c := range b {
		switch {
		case c >= 0x80 || c < 0x20 || c == 0x7f:
			return "", false
		case 'A' <= c && c <= 'Z':
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b), true
}
