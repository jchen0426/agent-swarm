package web

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
)

// contentSecurityPolicy is every response's policy (design D3): nothing but
// the interface's own styles and images, forms posted to itself, no framing,
// and no base URL. There is no script in 9a-1.
const contentSecurityPolicy = "default-src 'none'; style-src 'self'; img-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'"

// secured sets the security headers on every response (design D3; R87): the
// policy, no framing, the referrer for the interface's own origin alone, and
// no sniffing. HSTS is the proxy's.
func secured(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

// Authenticated marks a page that only a signed-in person sees: its
// responses carry Cache-Control: no-store, so that neither the browser nor a
// cache keeps them (design D3; m13). A health route never passes through it.
func Authenticated(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// hostRefusal is a refused Host's answer, which names the setting it is held
// to (R95).
const hostRefusal = "Host does not match web.publicURL"

// hostCheck refuses a request whose Host is not web.publicURL's host,
// compared normalized (R95). No forwarded header is read. The first refusal
// is logged at Warn, with the Host received, which is not a secret: a proxy
// that rewrites Host shows once, not on each request (I11).
type hostCheck struct {
	// want is the public URL's host, normalized; empty, which no Host
	// matches, for a URL that does not parse.
	want string
	// port is the public URL's scheme's default port, which a Host may give
	// or leave out.
	port   string
	log    *slog.Logger
	warned sync.Once
}

func newHostCheck(publicURL string, log *slog.Logger) *hostCheck {
	c := &hostCheck{log: log, port: "443"}
	if u, err := url.Parse(publicURL); err == nil && u.Host != "" {
		c.port = defaultPort(u.Scheme)
		c.want = normalHost(u.Host, c.port)
	}
	return c
}

func (c *hostCheck) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := normalHost(r.Host, c.port); got == "" || got != c.want {
			c.warned.Do(func() {
				c.log.WarnContext(r.Context(), "a request's Host does not match web.publicURL: the proxy must forward the client's Host unchanged",
					"host", r.Host)
			})
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, hostRefusal)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// defaultPort is a scheme's default port.
func defaultPort(scheme string) string {
	if strings.EqualFold(scheme, "http") {
		return "80"
	}
	return "443"
}

// normalHost is a host, host:port, as the Host and Origin checks compare it
// (R95; m15): case folded, a trailing dot dropped, the default port, def,
// stripped, and an IP address in its canonical form, IPv6 in brackets. It is
// empty for a host it cannot read: an empty one, an IPv6 address out of
// brackets, or one with more than one trailing dot or a user.
func normalHost(hostport, def string) string {
	hostport = strings.ToLower(hostport)
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
	if host == "" || strings.HasSuffix(host, ".") || strings.ContainsAny(host, "@/\\ ") {
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
