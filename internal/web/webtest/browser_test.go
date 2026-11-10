package webtest_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/web/webtest"
)

// served is a response to a GET of pageURL with the given Referrer-Policy
// headers.
func served(t *testing.T, pageURL string, policies ...string) *http.Response {
	t.Helper()
	u, err := url.Parse(pageURL)
	require.NoError(t, err)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Request: &http.Request{Method: http.MethodGet, URL: u}}
	for _, p := range policies {
		resp.Header.Add("Referrer-Policy", p)
	}
	return resp
}

func target(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u
}

// form is a page whose head holds head and whose body is a form posted to
// /act, with the given attributes.
func form(head, attrs string) []byte {
	return []byte(`<!doctype html><html><head>` + head + `</head><body>` +
		`<form method="post" action="/other" referrerpolicy="no-referrer"></form>` +
		`<form method="post" action="/act" ` + attrs + `><button>Act</button></form></body></html>`)
}

// A form's POST carries Origin: null when the effective referrer policy of
// the page that served it is no-referrer: from the Referrer-Policy header,
// then <meta name=referrer>, then the form's own referrerpolicy or
// rel=noreferrer; otherwise it carries the page's origin, unless the policy
// withholds it from this target (the Fetch standard; I8).
func TestBrowserOriginSendsNullUnderNoReferrer(t *testing.T) {
	const page = "https://agents.example.com/items"
	act := target(t, "https://agents.example.com/act")
	for _, tc := range []struct {
		name     string
		policies []string
		head     string
		attrs    string
		target   *url.URL
		want     string
	}{
		{name: "no policy", want: "https://agents.example.com"},
		{name: "same-origin header", policies: []string{"same-origin"}, want: "https://agents.example.com"},
		{name: "no-referrer header", policies: []string{"no-referrer"}, want: "null"},
		{name: "the header's last valid token", policies: []string{"no-referrer, same-origin, bogus"}, want: "https://agents.example.com"},
		{name: "the last header", policies: []string{"same-origin", "no-referrer"}, want: "null"},
		{name: "meta over the header", policies: []string{"same-origin"}, head: `<meta name="referrer" content="no-referrer">`, want: "null"},
		{name: "meta's legacy never", head: `<meta name="Referrer" content="never">`, want: "null"},
		{name: "meta back to same-origin", policies: []string{"no-referrer"}, head: `<meta name="referrer" content="same-origin">`, want: "https://agents.example.com"},
		{name: "an invalid meta is ignored", policies: []string{"no-referrer"}, head: `<meta name="referrer" content="bogus">`, want: "null"},
		{name: "the form's referrerpolicy", policies: []string{"same-origin"}, attrs: `referrerpolicy="no-referrer"`, want: "null"},
		{name: "the form's rel=noreferrer", policies: []string{"same-origin"}, attrs: `rel="external noreferrer"`, want: "null"},
		{name: "another form's attribute is not this one's", policies: []string{"same-origin"}, want: "https://agents.example.com"},
		{name: "same-origin to another origin", policies: []string{"same-origin"}, target: target(t, "https://evil.example.com/act"), want: "null"},
		{name: "a downgrade under the default", target: target(t, "http://agents.example.com/act"), want: "null"},
		{name: "a downgrade under unsafe-url", policies: []string{"unsafe-url"}, target: target(t, "http://agents.example.com/act"), want: "https://agents.example.com"},
	} {
		to := act
		if tc.target != nil {
			to = tc.target
		}
		got := webtest.BrowserOrigin(served(t, page, tc.policies...), form(tc.head, tc.attrs), to)
		assert.Equal(t, tc.want, got, tc.name)
	}

	// The origin is serialized as a browser does: the default port left
	// out, a port kept, the host lowercase.
	assert.Equal(t, "https://127.0.0.1:8443",
		webtest.BrowserOrigin(served(t, "https://127.0.0.1:8443/items"), form("", ""), target(t, "https://127.0.0.1:8443/act")))
	assert.Equal(t, "https://agents.example.com",
		webtest.BrowserOrigin(served(t, "https://Agents.Example.com:443/items"), form("", ""), act))
}

// received is a request a test server received: its path and its header.
type received struct {
	path   string
	header http.Header
}

// A poll is a script's request, as live.js sends it (Plan 9a-2, R164):
// Sec-Fetch-Site same-origin and Sec-Fetch-Mode cors, with none of a
// navigation's metadata; the jar's cookies, as a same-origin request
// carries them; If-None-Match the ETag it is given, and none when it is
// given none; and no redirect followed, a 3xx coming back as itself.
func TestPollSendsItsETagAndFollowsNoRedirect(t *testing.T) {
	var (
		mu   sync.Mutex
		seen []received
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, received{path: r.URL.Path, header: r.Header.Clone()})
		mu.Unlock()
		switch r.URL.Path {
		case "/items/a1/board":
			w.Header().Set("ETag", `"v1"`)
			if r.Header.Get("If-None-Match") == `"v1"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			_, _ = io.WriteString(w, "the regions")
		case "/items/a1/moved":
			http.Redirect(w, r, "/elsewhere", http.StatusSeeOther)
		default:
			_, _ = io.WriteString(w, "elsewhere")
		}
	}))
	defer srv.Close()
	b := webtest.NewBrowser(t, srv.Client())
	b.SetCookie(srv.URL, &http.Cookie{Name: "agentswarm_session", Value: "sealed"})

	full := b.Poll(srv.URL+"/items/a1/board", "")
	unchanged := b.Poll(srv.URL+"/items/a1/board", `"v1"`)
	moved := b.Poll(srv.URL+"/items/a1/moved", "")

	assert.Equal(t, http.StatusOK, full.Resp.StatusCode)
	assert.Equal(t, "the regions", string(full.Body))
	assert.Equal(t, `"v1"`, full.Resp.Header.Get("ETag"))
	assert.Equal(t, http.StatusNotModified, unchanged.Resp.StatusCode, "the ETag it was given matches")
	assert.Empty(t, unchanged.Body)
	assert.Equal(t, http.StatusSeeOther, moved.Resp.StatusCode, "a 3xx comes back as itself")
	assert.Equal(t, "/elsewhere", moved.Resp.Header.Get("Location"))
	assert.Empty(t, moved.Redirects, "no redirect was followed")
	assert.Equal(t, srv.URL+"/items/a1/moved", moved.Resp.Request.URL.String())

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, seen, 3, "the moved poll's Location was never asked for")
	for i, r := range seen {
		assert.Equal(t, "same-origin", r.header.Get("Sec-Fetch-Site"), "request %d", i)
		assert.Equal(t, "cors", r.header.Get("Sec-Fetch-Mode"), "request %d", i)
		assert.Empty(t, r.header.Values("Sec-Fetch-User"), "request %d is no navigation", i)
		assert.NotEqual(t, "document", r.header.Get("Sec-Fetch-Dest"), "request %d is no navigation", i)
		assert.Equal(t, "agentswarm_session=sealed", r.header.Get("Cookie"), "request %d carries the jar's cookie", i)
	}
	assert.Empty(t, seen[0].header.Values("If-None-Match"), "a poll given no ETag sends none")
	assert.Equal(t, []string{`"v1"`}, seen[1].header.Values("If-None-Match"))
	assert.Empty(t, seen[2].header.Values("If-None-Match"))
}
