package webtest

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/netip"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// defaultPolicy is the referrer policy of a page that sets none.
const defaultPolicy = "strict-origin-when-cross-origin"

// policies are the referrer policies a page may set, by their tokens.
var policies = map[string]bool{
	"no-referrer": true, "no-referrer-when-downgrade": true, "same-origin": true, "origin": true,
	"strict-origin": true, "origin-when-cross-origin": true, "strict-origin-when-cross-origin": true, "unsafe-url": true,
}

// legacyMeta are the legacy values <meta name=referrer> also takes, and
// the policies they stand for.
var legacyMeta = map[string]string{
	"never": "no-referrer", "default": defaultPolicy, "always": "unsafe-url",
	"origin-when-crossorigin": "origin-when-cross-origin",
}

// BrowserOrigin is the Origin header a browser sends with a POST of the
// form posted to target on page, the body of resp (the Fetch standard's
// "append a request Origin header"; I8). The form's referrer policy is the
// page's, from its Referrer-Policy header (the last valid token), then each
// <meta name=referrer>, then the form's own referrerpolicy attribute or
// rel=noreferrer. Under no-referrer the origin is "null"; under
// same-origin, "null" to another origin; under the strict policies and
// no-referrer-when-downgrade, "null" from https to a target that is not
// potentially trustworthy; otherwise the page's origin.
func BrowserOrigin(resp *http.Response, page []byte, target *url.URL) string {
	from := resp.Request.URL
	policy := defaultPolicy
	for _, v := range resp.Header.Values("Referrer-Policy") {
		for token := range strings.SplitSeq(v, ",") {
			if token = strings.TrimSpace(token); policies[token] {
				policy = token
			}
		}
	}
	doc, err := html.Parse(bytes.NewReader(page))
	if err == nil {
		policy = pagePolicy(doc, from, target, policy)
	}
	switch policy {
	case "no-referrer":
		return "null"
	case "same-origin":
		if serialize(from) != serialize(target) {
			return "null"
		}
	case "no-referrer-when-downgrade", "strict-origin", "strict-origin-when-cross-origin":
		if strings.EqualFold(from.Scheme, "https") && !trustworthy(target) {
			return "null"
		}
	}
	return serialize(from)
}

// pagePolicy is policy as the page's meta elements, and then its form
// posted to target, set it.
func pagePolicy(doc *html.Node, from, target *url.URL, policy string) string {
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode || n.DataAtom != atom.Meta || !strings.EqualFold(attr(n, "name"), "referrer") {
			continue
		}
		content := strings.ToLower(strings.TrimSpace(attr(n, "content")))
		if p, ok := legacyMeta[content]; ok {
			content = p
		}
		if policies[content] {
			policy = content
		}
	}
	if f := formFor(doc, from, target); f != nil {
		if p := strings.ToLower(strings.TrimSpace(attr(f, "referrerpolicy"))); policies[p] {
			policy = p
		}
		for rel := range strings.FieldsSeq(strings.ToLower(attr(f, "rel"))) {
			if rel == "noreferrer" {
				policy = "no-referrer"
			}
		}
	}
	return policy
}

// formFor is the page's form whose action, resolved against from, is
// target, or nil.
func formFor(doc *html.Node, from, target *url.URL) *html.Node {
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode || n.DataAtom != atom.Form {
			continue
		}
		action, err := from.Parse(attr(n, "action"))
		if err == nil && action.String() == target.String() {
			return n
		}
	}
	return nil
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

// serialize is u's origin as a browser serializes it: the scheme and host
// lowercase, the default port left out.
func serialize(u *url.URL) string {
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	port := u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		return scheme + "://" + host + ":" + port
	}
	return scheme + "://" + host
}

// trustworthy says whether u's origin is potentially trustworthy: https, or
// a loopback host.
func trustworthy(u *url.URL) bool {
	if strings.EqualFold(u.Scheme, "https") {
		return true
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

// Page is a page the browser received: the final response, its body, and
// the URLs it was redirected to on the way, in order.
type Page struct {
	Resp      *http.Response
	Body      []byte
	Redirects []*url.URL
}

// Browser is a browser-like client (R96): a cookie jar, redirects followed
// but by a script's poll (Poll), and on every form post the Origin
// BrowserOrigin gives and Sec-Fetch-Site as Chrome sends it.
type Browser struct {
	t      testing.TB
	client *http.Client
}

// maxRedirects is how many redirects the browser follows.
const maxRedirects = 10

// NewBrowser is a browser over a copy of client, with its transport (a TLS
// server's Client, for instance) and a fresh cookie jar.
func NewBrowser(t testing.TB, client *http.Client) *Browser {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("a cookie jar: %v", err)
	}
	c := *client
	c.Jar = jar
	return &Browser{t: t, client: &c}
}

// SetCookie puts c in the jar for rawURL.
func (b *Browser) SetCookie(rawURL string, c *http.Cookie) {
	b.t.Helper()
	b.client.Jar.SetCookies(b.parse(rawURL), []*http.Cookie{c})
}

// Cookies are the jar's cookies for rawURL.
func (b *Browser) Cookies(rawURL string) []*http.Cookie {
	b.t.Helper()
	return b.client.Jar.Cookies(b.parse(rawURL))
}

func (b *Browser) parse(rawURL string) *url.URL {
	b.t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		b.t.Fatalf("parse %q: %v", rawURL, err)
	}
	return u
}

// Get navigates to rawURL, as a person typing it: Sec-Fetch-Site none.
func (b *Browser) Get(rawURL string) *Page {
	b.t.Helper()
	req, err := http.NewRequestWithContext(b.t.Context(), http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		b.t.Fatalf("a GET of %q: %v", rawURL, err)
	}
	req.Header.Set("Sec-Fetch-Site", "none")
	navigation(req)
	return b.do(req)
}

// Poll is a script's request of rawURL: Sec-Fetch-Site same-origin,
// Sec-Fetch-Mode cors, If-None-Match etag when not empty; it follows no
// redirect, so a 3xx comes back as itself.
func (b *Browser) Poll(rawURL, etag string) *Page {
	b.t.Helper()
	req, err := http.NewRequestWithContext(b.t.Context(), http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		b.t.Fatalf("a poll of %q: %v", rawURL, err)
	}
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	c := *b.client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return read(b.t, &c, req)
}

// Submit posts the page's form whose action is action, resolved against
// the page's URL, with its inputs' values, values set over them, as a
// browser does: Origin by BrowserOrigin and Sec-Fetch-Site by the page's
// origin and the target's. A form of method get is a GET of its query.
func (b *Browser) Submit(p *Page, action string, values url.Values) *Page {
	b.t.Helper()
	from := p.Resp.Request.URL
	target, err := from.Parse(action)
	if err != nil {
		b.t.Fatalf("resolve %q: %v", action, err)
	}
	doc, err := html.Parse(bytes.NewReader(p.Body))
	if err != nil {
		b.t.Fatalf("parse the page: %v", err)
	}
	f := formFor(doc, from, target)
	if f == nil {
		b.t.Fatalf("the page has no form posted to %s", target)
	}
	form := inputs(f)
	for k, v := range values {
		form[k] = v
	}
	var req *http.Request
	if strings.EqualFold(attr(f, "method"), "get") {
		u := *target
		u.RawQuery = form.Encode()
		req, err = http.NewRequestWithContext(b.t.Context(), http.MethodGet, u.String(), http.NoBody)
	} else {
		req, err = http.NewRequestWithContext(b.t.Context(), http.MethodPost, target.String(), strings.NewReader(form.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Origin", BrowserOrigin(p.Resp, p.Body, target))
		}
	}
	if err != nil {
		b.t.Fatalf("a request to %s: %v", target, err)
	}
	req.Header.Set("Sec-Fetch-Site", site(from, target))
	navigation(req)
	return b.do(req)
}

// inputs are a form's named inputs' values, as a browser submits them
// without a person's typing: hidden and text inputs, checked boxes, and
// text areas; no button, since the test names the one it presses in
// values.
func inputs(f *html.Node) url.Values {
	out := url.Values{}
	for n := range f.Descendants() {
		if n.Type != html.ElementNode {
			continue
		}
		name := attr(n, "name")
		if name == "" {
			continue
		}
		switch n.DataAtom {
		case atom.Input:
			switch strings.ToLower(attr(n, "type")) {
			case "submit", "button", "image", "reset", "file":
			case "checkbox", "radio":
				if _, checked := attrOK(n, "checked"); checked {
					out.Add(name, valueOr(n, "on"))
				}
			default:
				out.Add(name, attr(n, "value"))
			}
		case atom.Textarea:
			var text strings.Builder
			for c := range n.ChildNodes() {
				if c.Type == html.TextNode {
					text.WriteString(c.Data)
				}
			}
			out.Add(name, text.String())
		}
	}
	return out
}

func attrOK(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val, true
		}
	}
	return "", false
}

func valueOr(n *html.Node, def string) string {
	if v, ok := attrOK(n, "value"); ok {
		return v
	}
	return def
}

// navigation sets the fetch metadata of a top-level navigation.
func navigation(req *http.Request) {
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-User", "?1")
}

// site is Sec-Fetch-Site of a request from a page at from to target, as
// Chrome sends it: same-origin, same-site (the same scheme and the same
// last two labels of the host, or the same IP address), or cross-site.
func site(from, target *url.URL) string {
	if serialize(from) == serialize(target) {
		return "same-origin"
	}
	if !strings.EqualFold(from.Scheme, target.Scheme) {
		return "cross-site"
	}
	a, b := strings.ToLower(from.Hostname()), strings.ToLower(target.Hostname())
	if net.ParseIP(a) != nil || net.ParseIP(b) != nil {
		if a == b {
			return "same-site"
		}
		return "cross-site"
	}
	if lastTwo(a) == lastTwo(b) {
		return "same-site"
	}
	return "cross-site"
}

func lastTwo(host string) string {
	labels := strings.Split(strings.TrimSuffix(host, "."), ".")
	if len(labels) > 2 {
		labels = labels[len(labels)-2:]
	}
	return strings.Join(labels, ".")
}

// do sends req, follows redirects as a browser does, a POST answered 303
// becoming a GET with no Origin, and reads the final page.
func (b *Browser) do(req *http.Request) *Page {
	b.t.Helper()
	var redirects []*url.URL
	c := *b.client
	c.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return http.ErrUseLastResponse
		}
		redirects = append(redirects, next.URL)
		if next.Method == http.MethodGet || next.Method == http.MethodHead {
			next.Header.Del("Origin")
		}
		next.Header.Set("Sec-Fetch-Site", site(via[0].URL, next.URL))
		return nil
	}
	p := read(b.t, &c, req)
	p.Redirects = redirects
	return p
}

// read sends req with c and reads the page it answers: the final one, when
// c follows redirects.
func read(t testing.TB, c *http.Client, req *http.Request) *Page {
	t.Helper()
	resp, err := c.Do(req) //nolint:gosec // G704: a test's browser, sent where the test points it.
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", req.URL, err)
	}
	return &Page{Resp: resp, Body: body}
}
