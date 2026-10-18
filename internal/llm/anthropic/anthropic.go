// Package anthropic is the LLM gateway's adapter of the model vendor
// (ADR-007). It sends a request the gateway checked to the vendor's
// Messages API with the deployment's key, which it reads from the secret
// store at each call so that a rotated key takes effect at once (spec
// Section 9.3), and with nothing of the client's: no header the client
// sent reaches the vendor. It never follows a redirect, which would carry
// the key to another host. It tells an attempt whose request never left
// from one whose answer was lost after the request reached the vendor,
// which the vendor may have charged for.
package anthropic

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/jchen0426/agent-swarm/internal/llm/app"
)

// MaxAnswerBytes caps the answer the gateway reads.
const MaxAnswerBytes = 8 << 20

// Secrets resolves secret references at the time of use.
type Secrets interface {
	Secret(ctx context.Context, ref string) (string, error)
}

// Config names the vendor's API and the deployment's key.
type Config struct {
	// BaseURL is the API's address: HTTPS, or plain HTTP to a loopback
	// host only, with no user information, query or fragment.
	BaseURL string
	// KeyRef is the secret reference of the API key.
	KeyRef string
	// Version is the anthropic-version every request carries.
	Version string
}

// Vendor sends requests to the vendor's Messages API. It implements the
// LLM gateway's Vendor port.
type Vendor struct {
	messages string
	keyRef   string
	version  string
	secrets  Secrets
	client   *http.Client
}

// New returns the vendor at cfg.BaseURL. Its errors quote the address only
// as shown returns it, and not at all when it names no host: such an address
// is likeliest a key pasted in its place, which would then be printed.
func New(cfg *Config, secrets Secrets) (*Vendor, error) {
	base, err := url.Parse(cfg.BaseURL)
	if err != nil {
		// The parser's error quotes the whole address, credentials included.
		return nil, errors.New("anthropic: the API's address is not a URL")
	}
	if base.Opaque != "" || base.Hostname() == "" {
		return nil, errors.New("anthropic: the API's address names no host")
	}
	if base.User != nil || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" || base.RawFragment != "" {
		return nil, fmt.Errorf("anthropic: the API's address %s carries user information, a query or a fragment", shown(base))
	}
	switch {
	case base.Scheme == "https":
	case base.Scheme == "http" && loopback(base.Hostname()):
	default:
		return nil, fmt.Errorf("anthropic: %s is reached over HTTPS; plain HTTP is for a loopback host only", shown(base))
	}
	if cfg.KeyRef == "" || cfg.Version == "" {
		return nil, errors.New("anthropic: the key's secret reference and the API version are required")
	}
	return &Vendor{
		messages: base.JoinPath("v1", "messages").String(),
		keyRef:   cfg.KeyRef,
		version:  cfg.Version,
		secrets:  secrets,
		client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}, nil
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// shown returns the address as an error may print it. Its user information,
// query, fragment and opaque part are left out, since their values may be
// credentials; url.URL.Redacted would keep a user name, which can be a token.
func shown(base *url.URL) string {
	s := *base
	s.User, s.RawQuery, s.ForceQuery, s.Fragment, s.RawFragment, s.Opaque = nil, "", false, "", "", ""
	return s.String()
}

// Send posts body to the Messages API and returns the vendor's answer,
// whatever its status. An attempt whose request was not written in full is
// app.ErrNotSent; one whose answer broke off, or was larger than the
// gateway reads, after the request was written in full, is app.ErrLost.
func (v *Vendor) Send(ctx context.Context, body []byte) (*app.Answer, error) {
	key, err := v.secrets.Secret(ctx, v.keyRef)
	if err != nil {
		return nil, fmt.Errorf("%w: read the API key: %w", app.ErrNotSent, err)
	}
	var wrote atomic.Bool
	// The hook gets the write's error: a request whose write failed was not
	// written in full, so the vendor has none to charge for.
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{WroteRequest: func(i httptrace.WroteRequestInfo) {
		if i.Err == nil {
			wrote.Store(true)
		}
	}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.messages, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", app.ErrNotSent, err)
	}
	req.Header.Set("X-Api-Key", key)
	req.Header.Set("Anthropic-Version", v.version)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "agentswarm-llm-gateway")
	resp, err := v.client.Do(req) //nolint:gosec // G704: the configured API's Messages endpoint only
	if err != nil {
		if wrote.Load() {
			return nil, fmt.Errorf("%w: %w", app.ErrLost, err)
		}
		return nil, fmt.Errorf("%w: %w", app.ErrNotSent, err)
	}
	defer func() { _ = resp.Body.Close() }()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, MaxAnswerBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: the answer broke off: %w", app.ErrLost, err)
	}
	if len(answer) > MaxAnswerBytes {
		return nil, fmt.Errorf("%w: the answer is larger than %d bytes", app.ErrLost, MaxAnswerBytes)
	}
	return &app.Answer{
		Status: resp.StatusCode, Body: answer,
		RequestID: resp.Header.Get("Request-Id"), RetryAfter: retryAfter(resp.Header.Get("Retry-After")),
	}, nil
}

// maxRetryAfter bounds the wait the vendor may ask for; the gateway waits
// less, within the round and the call's timeout.
const maxRetryAfter = time.Hour

// retryAfter reads a wait given in seconds or as a date; anything else is
// no wait.
func retryAfter(v string) time.Duration {
	if s, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.Duration(min(max(s, 0), int64(maxRetryAfter/time.Second))) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return min(max(time.Until(t), 0), maxRetryAfter)
	}
	return 0
}
