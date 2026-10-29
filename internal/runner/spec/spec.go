// Package spec is a turn's spec (Contract C6): one JSON object, which a
// runner writes on the runtime's standard input, and the runtime, agentswarm
// turn, reads. It is the one Go type that the runners write and the runtime
// reads (Plan 7d-2, Decision 9). Fields are added, never renamed or removed,
// and a field the runtime does not know is left alone.
package spec

import (
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// A spec's versions, the runtime image's label that lists those it reads,
// and a spec's size at most.
const (
	V1 = 1 // Plan 7d-1's form
	V2 = 2 // v, a socket per gateway, cert
	// Label is the runtime image's label: the versions its runtime reads,
	// comma-separated, such as "1,2". A runner writes the newest version that
	// both it and the image know (Newest).
	Label = "io.agentswarm.turn-spec"
	// MaxSize caps a spec: the runtime reads no more of its standard input
	// before the spec ends.
	MaxSize = 1 << 20
)

// Reads lists the versions this build's runtime reads; the runtime image's
// label lists them (build/runtime/Dockerfile).
var Reads = []int{V1, V2}

// Spec is a turn's spec. Nothing in it is written anywhere else: its tokens
// print, and log, as a placeholder.
type Spec struct {
	// V is the spec's version: absent, or 0, it reads as V1.
	V     int     `json:"v,omitempty"`
	Turn  string  `json:"turn"`
	Run   string  `json:"run"`
	Agent string  `json:"agent"`
	Round int     `json:"round"`
	MCP   Gateway `json:"mcp"`
	LLM   Gateway `json:"llm"`
	// Cert, when set, holds a PEM certificate, the only one the runtime
	// trusts for the gateways' https URLs, at TLS 1.3 at least.
	Cert  string `json:"cert,omitempty"`
	Model string `json:"model"`
	// System holds the agent's skills, each sent as its own system block.
	System []string `json:"system"`
	Prompt string   `json:"prompt"`
	// Deadline is the round's deadline, in the process's clock, plus the
	// runtime's slack.
	Deadline time.Time `json:"deadline"`
	// MaxTokens is the deployment's runner.maxTokens (R14).
	MaxTokens int64 `json:"maxTokens"`
}

// Gateway is where a turn reaches a gateway, and its session's token there.
type Gateway struct {
	URL   string `json:"url"`
	Token Token  `json:"token"`
	// Socket, when set, is the absolute path of the Unix socket the runtime
	// dials for every connection to the gateway, whatever its URL's host.
	Socket string `json:"socket,omitempty"`
}

// Token is a session's token, which only the spec's JSON holds: the runtime
// sends it only in a request's header, and it prints as "[session token]"
// through String, GoString, Format and LogValue, so that no error or log can
// hold it. Its JSON is the token itself, which the runtime reads.
type Token string

// shown is what a token prints as.
const shown = "[session token]"

func (Token) String() string { return shown }

func (Token) GoString() string { return shown }

// Format prints a token as its placeholder whatever the verb: fmt asks
// String and GoString only for some verbs, and prints the token itself for
// the others, such as %d.
func (Token) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, shown) }

// LogValue logs a token as its placeholder.
func (Token) LogValue() slog.Value { return slog.StringValue(shown) }

// LogValue is a gateway's entry as slog logs it: its url, its token as the
// placeholder, and its socket. slog's JSON handler would otherwise marshal
// the struct with encoding/json, which never asks Token.LogValue. Its
// receiver is a value, so that a Gateway and a *Gateway are both covered.
func (g Gateway) LogValue() slog.Value {
	return slog.GroupValue(slog.String("url", g.URL), slog.Any("token", g.Token), slog.String("socket", g.Socket))
}

// Read reads a turn's spec: one JSON object of at most MaxSize bytes, after
// which it reads no more of r; then Check. A field it does not know is left
// alone.
func Read(r io.Reader) (*Spec, error) {
	limited := &io.LimitedReader{R: r, N: MaxSize + 1}
	dec := json.NewDecoder(limited)
	var s Spec
	err := dec.Decode(&s)
	switch {
	case err != nil && limited.N == 0, err == nil && dec.InputOffset() > MaxSize:
		return nil, errors.New("the turn's spec is larger than 1 MiB")
	case err != nil:
		return nil, fmt.Errorf("read the turn's spec: %w", err)
	}
	if err := s.Check(); err != nil {
		return nil, err
	}
	return &s, nil
}

// Check refuses a spec the runtime cannot take: one of a version it does not
// read; one without a field it needs, each gateway's HTTP URL and token, the
// model, the prompt, the deadline and maxTokens; one whose gateway's socket
// is not an absolute path; and one whose cert holds no PEM certificate.
func (s *Spec) Check() error {
	if v := s.Version(); !slices.Contains(Reads, v) {
		return fmt.Errorf("the turn's spec is of version %d, which this runtime does not read", v)
	}
	if err := s.MCP.check("the MCP gateway"); err != nil {
		return err
	}
	if err := s.LLM.check("the LLM gateway"); err != nil {
		return err
	}
	switch {
	case s.Cert != "" && !x509.NewCertPool().AppendCertsFromPEM([]byte(s.Cert)):
		return errors.New("the turn's spec has a cert that holds no PEM certificate")
	case s.Model == "":
		return errors.New("the turn's spec names no model")
	case s.Prompt == "":
		return errors.New("the turn's spec has no prompt")
	case s.Deadline.IsZero():
		return errors.New("the turn's spec has no deadline")
	case s.MaxTokens < 1:
		return errors.New("the turn's spec has no maxTokens")
	}
	return nil
}

// check refuses a gateway's entry without an HTTP URL or a token, or whose
// socket is not an absolute path; name names the gateway.
func (g *Gateway) check(name string) error {
	if u, err := url.Parse(g.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("the turn's spec has no URL for %s", name)
	}
	if g.Token == "" {
		return fmt.Errorf("the turn's spec has no token for %s", name)
	}
	if g.Socket != "" && !filepath.IsAbs(g.Socket) {
		return fmt.Errorf("the turn's spec names a socket for %s that is not an absolute path", name)
	}
	return nil
}

// Version is the spec's version: V1 when V is 0, as when it is absent.
func (s *Spec) Version() int {
	if s.V == 0 {
		return V1
	}
	return s.V
}

// LogValue is a spec as slog logs it: a group of its fields that holds
// neither token. slog's JSON handler would otherwise marshal the struct with
// encoding/json, which never asks Token.LogValue, and a Token.MarshalJSON
// that hid the token would hide it from the runtime too. Its receiver is a
// value, so that a Spec and a *Spec are both covered.
//
//nolint:gocritic // hugeParam: a value receiver, so that slog finds LogValue on a Spec as well as a *Spec.
func (s Spec) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Int("v", s.V), slog.String("turn", s.Turn), slog.String("run", s.Run), slog.String("agent", s.Agent), slog.Int("round", s.Round),
		slog.Any("mcp", s.MCP), slog.Any("llm", s.LLM), slog.String("cert", s.Cert), slog.String("model", s.Model),
		slog.Any("system", s.System), slog.String("prompt", s.Prompt), slog.Time("deadline", s.Deadline), slog.Int64("maxTokens", s.MaxTokens),
	)
}

// labelForm is the form of the runtime image's label (C6). RE2's \d is
// [0-9], no other digit.
var labelForm = regexp.MustCompile(`^[1-9]\d*(,[1-9]\d*)*$`)

// ParseLabel reads the value of the runtime image's label (Label): the
// versions its runtime reads, comma-separated, such as "1,2".
func ParseLabel(v string) ([]int, error) {
	if !labelForm.MatchString(v) {
		return nil, fmt.Errorf("the image's label %s is %q, which is not a list of versions such as \"1,2\"", Label, v)
	}
	var versions []int
	for part := range strings.SplitSeq(v, ",") {
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("the image's label %s is %q: %w", Label, v, err)
		}
		versions = append(versions, n)
	}
	return versions, nil
}

// Newest returns the newest version that both the image reads and the
// runner writes, and false when they share none.
func Newest(image, writes []int) (int, bool) {
	newest, ok := 0, false
	for _, v := range image {
		if slices.Contains(writes, v) && (!ok || v > newest) {
			newest, ok = v, true
		}
	}
	return newest, ok
}
