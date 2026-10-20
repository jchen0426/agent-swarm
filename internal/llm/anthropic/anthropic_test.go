package anthropic_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/llm/anthropic"
	"github.com/jchen0426/agent-swarm/internal/llm/app"
)

// keys is a secret store that rotates the key: each read gives the next.
type keys struct{ n atomic.Int32 }

func (k *keys) Secret(_ context.Context, ref string) (string, error) {
	if ref != "secret://llm/anthropic" {
		return "", errors.New("no such secret")
	}
	return []string{"key-one", "key-two"}[k.n.Add(1)%2], nil
}

func vendor(t *testing.T, base string) *anthropic.Vendor {
	t.Helper()
	v, err := anthropic.New(&anthropic.Config{BaseURL: base, KeyRef: "secret://llm/anthropic", Version: "2023-06-01"}, &keys{})
	require.NoError(t, err)
	return v
}

// The vendor gets the body as given, the key read at each call and the
// gateway's version, and no other header that matters.
func TestARequestCarriesTheKeyAndNothingOfTheClients(t *testing.T) {
	var got []http.Header
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got, bodies = append(got, r.Header.Clone()), append(bodies, string(b))
		assert.Equal(t, "/v1/messages", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		w.Header().Set("request-id", "req_1")
		_, _ = io.WriteString(w, `{"ok": true}`)
	}))
	defer srv.Close()
	v := vendor(t, srv.URL)

	a, err := v.Send(t.Context(), []byte(`{"model":"m"}`))
	require.NoError(t, err)
	_, err = v.Send(t.Context(), []byte(`{"model":"m"}`))
	require.NoError(t, err)

	assert.Equal(t, 200, a.Status)
	assert.JSONEq(t, `{"ok": true}`, string(a.Body))
	assert.Equal(t, "req_1", a.RequestID)
	assert.Equal(t, []string{`{"model":"m"}`, `{"model":"m"}`}, bodies)
	assert.Equal(t, []string{"key-two", "key-one"}, []string{got[0].Get("X-Api-Key"), got[1].Get("X-Api-Key")}, "read at each call")
	for _, h := range got {
		assert.Equal(t, "2023-06-01", h.Get("Anthropic-Version"))
		assert.Equal(t, "application/json", h.Get("Content-Type"))
		assert.Empty(t, h.Get("Authorization"))
		assert.Empty(t, h.Get("Anthropic-Workspace-Id"))
		assert.Empty(t, h.Get("Anthropic-Beta"))
		for name := range h {
			assert.False(t, strings.HasPrefix(name, "X-Stainless"), name)
		}
	}
}

// A redirect is never followed: the key would go with it to another host.
func TestARedirectIsNotFollowed(t *testing.T) {
	var reached atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached.Store(true) }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/v1/messages", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()

	a, err := vendor(t, srv.URL).Send(t.Context(), []byte(`{}`))

	require.NoError(t, err)
	assert.Equal(t, http.StatusTemporaryRedirect, a.Status)
	assert.False(t, reached.Load())
}

// The wait the vendor asks for is read in seconds or as a date, and never
// as more than an hour.
func TestTheWaitTheVendorAsksForIsRead(t *testing.T) {
	for header, want := range map[string]time.Duration{"7": 7 * time.Second, "": 0, "soon": 0, "-3": 0, "99999999999": time.Hour} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if header != "" {
				w.Header().Set("retry-after", header)
			}
			w.WriteHeader(529)
		}))

		a, err := vendor(t, srv.URL).Send(t.Context(), []byte(`{}`))

		srv.Close()
		require.NoError(t, err)
		assert.Equal(t, want, a.RetryAfter, header)
	}
}

// A request that never left is ErrNotSent, which another attempt may get
// past; one whose answer broke off after it was written, or was larger
// than the gateway reads, is ErrLost: the vendor may have charged for it.
func TestFailuresToReachTheVendor(t *testing.T) {
	large := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", 9<<20))
	}))
	defer large.Close()
	_, err := vendor(t, large.URL).Send(t.Context(), []byte(`{}`))
	require.ErrorIs(t, err, app.ErrLost)

	cut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		conn, _, err := w.(http.Hijacker).Hijack()
		if !assert.NoError(t, err) {
			return
		}
		_ = conn.Close()
	}))
	defer cut.Close()
	_, err = vendor(t, cut.URL).Send(t.Context(), []byte(`{}`))
	require.ErrorIs(t, err, app.ErrLost)

	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	_, err = vendor(t, gone.URL).Send(t.Context(), []byte(`{}`))
	require.ErrorIs(t, err, app.ErrNotSent)
}

// A key that cannot be read is the platform's own failure: nothing is sent,
// and it is ErrNoKey, not ErrNotSent, which another attempt may get past.
func TestAKeyThatCannotBeReadIsNotTheVendors(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	v, err := anthropic.New(&anthropic.Config{BaseURL: srv.URL, KeyRef: "secret://llm/missing", Version: "2023-06-01"}, &keys{})
	require.NoError(t, err)

	_, err = v.Send(t.Context(), []byte(`{}`))

	require.ErrorIs(t, err, app.ErrNoKey)
	require.NotErrorIs(t, err, app.ErrNotSent)
	assert.Zero(t, hits.Load())
}

// A write that fails before the request is in full leaves the vendor no
// request to charge for, so the attempt is not sent and another may get
// past it. The cut case of TestFailuresToReachTheVendor, whose request was
// written in full, stays lost.
func TestAWriteThatFailsIsNotSent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// The body is never read: the connection is reset while the 8 MiB
		// request is still being written.
		conn, _, err := w.(http.Hijacker).Hijack()
		if !assert.NoError(t, err) {
			return
		}
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.SetLinger(0)
		}
		_ = conn.Close()
	}))
	defer srv.Close()

	_, err := vendor(t, srv.URL).Send(t.Context(), []byte(`{"x":"`+strings.Repeat("a", 8<<20)+`"}`))

	require.ErrorIs(t, err, app.ErrNotSent)
}

// The vendor is reached over HTTPS, or over plain HTTP on a loopback host
// only; its key is a secret reference.
func TestTheVendorIsReachedOverHTTPS(t *testing.T) {
	for _, base := range []string{"http://api.anthropic.com", "ftp://127.0.0.1", "https://", "not a url"} {
		_, err := anthropic.New(&anthropic.Config{BaseURL: base, KeyRef: "secret://llm/anthropic", Version: "2023-06-01"}, &keys{})

		require.Error(t, err, base)
	}
	_, err := anthropic.New(&anthropic.Config{BaseURL: "https://api.anthropic.com", KeyRef: "", Version: "2023-06-01"}, &keys{})
	require.Error(t, err, "a key reference is required")
	_, err = anthropic.New(&anthropic.Config{BaseURL: "https://api.anthropic.com", KeyRef: "secret://llm/anthropic", Version: "2023-06-01"}, &keys{})
	require.NoError(t, err)
}

// The vendor's address names a host and a path, and no credential rides on
// it: user information, a query, a fragment and an opaque part are refused,
// and no error quotes their values. An address that names no host, as a key
// pasted in its place does, is refused without being quoted at all.
func TestTheVendorsAddressNamesAHostAndNothingElse(t *testing.T) {
	const secret = "sk-ant-SECRETVALUE"
	for _, base := range []string{
		secret,
		"https:///" + secret,
		"api.anthropic.com/" + secret,
		"/" + secret,
		"https://user:" + secret + "@api.anthropic.com",
		"https://" + secret + "@api.anthropic.com",
		"http://user:" + secret + "@127.0.0.1:8080",
		"https://api.anthropic.com/?key=" + secret,
		"https://api.anthropic.com/?",
		"https://api.anthropic.com/#" + secret,
		"https:user:" + secret + "@api.anthropic.com",
		"https://:443",
		"http://user:" + secret + "@[::1",
	} {
		_, err := anthropic.New(&anthropic.Config{BaseURL: base, KeyRef: "secret://llm/anthropic", Version: "2023-06-01"}, &keys{})

		require.Error(t, err, base)
		assert.NotContains(t, err.Error(), secret, base)
	}
}
