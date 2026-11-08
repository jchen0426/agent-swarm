package deploy

import (
	"crypto/x509"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// defaultWeb is the web block of a file that gives the block and leaves out
// its optional keys (Plan 9a-1, R86).
var defaultWeb = Web{
	OIDC:          OIDC{SubjectClaim: "sub", GroupsClaim: "groups"},
	SessionLength: 8 * time.Hour,
	FreshWindow:   5 * time.Minute,
	TimeZone:      "UTC",
}

// The bounds of the web block's durations (R86).
const (
	minSessionLength = time.Hour
	maxSessionLength = 24 * time.Hour
	minFreshWindow   = time.Minute
	maxFreshWindow   = 15 * time.Minute
)

// Web is the web block (Plan 9a-1, R86), its defaults filled in: the web
// interface for people, which serves on listeners.web, on loopback, behind
// the deployment's TLS-terminating proxy. A deployment without it has no web
// listener.
type Web struct {
	// PublicURL is the origin people reach the interface at, through the
	// proxy: https, with no path but /, and no query. It is the one source of
	// the origin, the Host check and the OIDC redirect URL.
	PublicURL string `yaml:"publicURL"`
	OIDC      OIDC   `yaml:"oidc"`
	Groups    Groups `yaml:"groups"`
	// SessionKeyRef is the reference of the session key.
	SessionKeyRef string `yaml:"sessionKeyRef"`
	// SessionLength is how long a sign-in lasts, 1 h to 24 h [8 h].
	SessionLength time.Duration `yaml:"sessionLength"`
	// FreshWindow is how recent a sign-in a sensitive act needs, 1 min to
	// 15 min [5 min].
	FreshWindow time.Duration `yaml:"freshWindow"`
	// TimeZone is the IANA zone pages show times in [UTC].
	TimeZone string `yaml:"timeZone"`
}

// OIDC is the identity provider people sign in with.
type OIDC struct {
	// Issuer is the provider's issuer URL, https.
	Issuer   string `yaml:"issuer"`
	ClientID string `yaml:"clientID"`
	// ClientSecretRef is the reference of the client's secret.
	ClientSecretRef string `yaml:"clientSecretRef"`
	// CABundle is a PEM file of CAs trusted besides the system roots, or
	// empty, as mcp.caBundle is (R94).
	CABundle string `yaml:"caBundle"`
	// SubjectClaim and GroupsClaim name the ID token's claims of a person's
	// subject [sub] and groups [groups].
	SubjectClaim string `yaml:"subjectClaim"`
	GroupsClaim  string `yaml:"groupsClaim"`
}

// Groups are the provider's groups that give each role: three distinct
// groups.
type Groups struct {
	Viewers    string `yaml:"viewers"`
	Responders string `yaml:"responders"`
	Admins     string `yaml:"admins"`
}

// web refuses what R86 refuses of the web block, and listeners.web: with the
// block, a web listener that is not on loopback, and each key of the block
// that has the wrong form; without it, a web listener, which would serve
// nothing. No problem repeats a value it refuses, which may be a secret.
func (c *checker) web(d *Deployment) {
	w := d.Web
	if w == nil {
		if d.Listeners.Web != "" {
			c.add("listeners.web", "is refused without a web block: it serves the web interface")
		}
		return
	}
	c.loopback("listeners.web", d.Listeners.Web, "such as 127.0.0.1:7404 or [::1]:7404: the web interface listens on loopback, behind the deployment's TLS proxy")
	c.publicURL("web.publicURL", w.PublicURL)
	c.httpsURL("web.oidc.issuer", w.OIDC.Issuer, "an https URL with a host, such as https://idp.example.com/realms/ops")
	c.required("web.oidc.clientID", w.OIDC.ClientID)
	c.reference("web.oidc.clientSecretRef", w.OIDC.ClientSecretRef)
	if w.OIDC.CABundle != "" {
		c.pemFile("web.oidc.caBundle", w.OIDC.CABundle)
	}
	c.required("web.oidc.subjectClaim", w.OIDC.SubjectClaim)
	c.required("web.oidc.groupsClaim", w.OIDC.GroupsClaim)
	c.groups(&w.Groups)
	c.reference("web.sessionKeyRef", w.SessionKeyRef)
	c.within("web.sessionLength", w.SessionLength, minSessionLength, maxSessionLength)
	c.within("web.freshWindow", w.FreshWindow, minFreshWindow, maxFreshWindow)
	c.timeZone("web.timeZone", w.TimeZone)
}

// publicURL refuses a public URL that is not https with a host, or that
// holds credentials, a path other than /, a query or a fragment. The
// problem never repeats the value.
func (c *checker) publicURL(key, value string) {
	const form = "an https URL with a host, and no credentials, path, query or fragment, such as https://agents.example.com"
	if !c.httpsURL(key, value, form) {
		return
	}
	u, _ := url.Parse(value)
	if u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		c.add(key, "must be %s", form)
	}
}

// httpsURL refuses a value that is not an https URL with a host, saying the
// form, and reports whether it is one. The problem never repeats the value.
func (c *checker) httpsURL(key, value, form string) bool {
	if !c.required(key, value) {
		return false
	}
	if u, err := url.Parse(value); err != nil || u.Scheme != "https" || u.Hostname() == "" {
		c.add(key, "must be %s", form)
		return false
	}
	return true
}

// pemFile refuses a path that is not a readable PEM file of at least one
// certificate, as serve reads mcp.caBundle. The problem names neither the
// file's content nor the error's words.
func (c *checker) pemFile(key, path string) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		c.add(key, "must be a readable PEM file of certificates: it cannot be read")
		return
	}
	if !x509.NewCertPool().AppendCertsFromPEM(raw) {
		c.add(key, "must be a readable PEM file of certificates: it holds none")
	}
}

// groups refuses a role's group left out, and two roles of one group: each
// role is given by a group of its own.
func (c *checker) groups(g *Groups) {
	seen := map[string]string{}
	for _, role := range []struct{ key, group string }{
		{"web.groups.viewers", g.Viewers}, {"web.groups.responders", g.Responders}, {"web.groups.admins", g.Admins},
	} {
		if !c.required(role.key, role.group) {
			continue
		}
		if other, ok := seen[role.group]; ok {
			c.add(role.key, "must be a group of its own: it is %s's", other)
			continue
		}
		seen[role.group] = role.key
	}
}

// within refuses a duration outside [low, high].
func (c *checker) within(key string, d, low, high time.Duration) {
	if d < low || d > high {
		c.add(key, "must be from %v to %v", low, high)
	}
}

// timeZone refuses a zone that is not IANA's: an empty one, the host's own,
// Local, and a name the zone database does not have.
func (c *checker) timeZone(key, value string) {
	if !c.required(key, value) {
		return
	}
	if _, err := time.LoadLocation(value); err != nil || value == "Local" {
		c.add(key, "must be an IANA time zone, such as UTC or Europe/Berlin")
	}
}
