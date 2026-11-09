//go:build e2e

package harness

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// The headers of a signed inbound request (spec Section 3.6): the request's
// timestamp, in Unix seconds, and its signature, "v1=" and the hex of
// HMAC-SHA256 over "<timestamp>.<body>" by the source's secret.
const (
	inboundTimestamp = "X-Agentswarm-Timestamp"
	inboundSignature = "X-Agentswarm-Signature"
	// inboundTimeout bounds one request to the inbound listener.
	inboundTimeout = time.Minute
)

// Inbound is serve's inbound listener (Plan 7c-1b), on loopback: POST
// /inbound/{source}, as a source's request comes.
type Inbound struct {
	base   string
	client *http.Client
}

// InboundAnswer is the inbound listener's answer: on 200 the count of each
// outcome its events had, and on a refusal its code.
type InboundAnswer struct {
	Opened            int    `json:"opened"`
	Appended          int    `json:"appended"`
	Late              int    `json:"late"`
	Repeat            int    `json:"repeat"`
	ResolvedUnmatched int    `json:"resolved_unmatched"`
	Unroutable        int    `json:"unroutable"`
	Refused           int    `json:"refused"`
	Error             string `json:"error"`
}

// Inbound returns serve's inbound listener, nil when its deployment has none
// (DeploymentSpec.Inbound) or serve exited before it bound its listeners.
func (i *Instance) Inbound() *Inbound {
	if i.addrs.Inbound == "" {
		return nil
	}
	return &Inbound{base: "http://" + i.addrs.Inbound, client: &http.Client{Timeout: inboundTimeout}}
}

// Send posts body to source as a source signs it with secret at at: the
// timestamp at's Unix seconds, the signature over "<timestamp>.<body>". It
// returns the answer's status, and its body.
func (in *Inbound) Send(t *testing.T, source string, body []byte, secret string, at time.Time) (status int, answer InboundAnswer) {
	t.Helper()
	ts := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	header := http.Header{}
	header.Set(inboundTimestamp, ts)
	header.Set(inboundSignature, "v1="+hex.EncodeToString(mac.Sum(nil)))
	return in.SendRaw(t, source, body, header)
}

// SendRaw posts body to source with header as it is, signed or not, and
// returns the answer's status, and its body. The test fails when no answer
// comes, or its body is not JSON.
func (in *Inbound) SendRaw(t *testing.T, source string, body []byte, header http.Header) (int, InboundAnswer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), inboundTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, in.base+"/inbound/"+source, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	for name, values := range header {
		req.Header[name] = values
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := in.client.Do(req) //nolint:gosec // G704: the instance's own inbound listener, on loopback.
	if err != nil {
		t.Fatalf("harness: POST /inbound/%s: %v", source, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("harness: read the answer to POST /inbound/%s: %v", source, err)
	}
	var a InboundAnswer
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatalf("harness: the answer to POST /inbound/%s (%d) is not JSON: %q", source, resp.StatusCode, raw)
	}
	return resp.StatusCode, a
}

// InboundItemID is the id of the item an inbound event opens, as serve
// derives it (R121, R129): a UUID of version 8 whose bits are the first 128
// of SHA-256 over the tenant, a zero byte and "inbound:<source>:<eventID>",
// with the version and variant bits set. Code under test/e2e cannot import
// serve's derivation, so the harness keeps its own, which a vector pins.
func InboundItemID(tenant, source, eventID string) string {
	sum := sha256.Sum256([]byte(tenant + "\x00" + "inbound:" + source + ":" + eventID))
	u := sum[:16]
	u[6] = u[6]&0x0f | 0x80 // version 8
	u[8] = u[8]&0x3f | 0x80 // the variant of RFC 9562
	h := hex.EncodeToString(u)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
