package auth

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/work/app"
)

// CallbackPath is where the provider sends a person back, on web.publicURL.
const CallbackPath = "/auth/callback"

// FreshPath is where a signed-in person authenticates again, to act as an
// admin; a GET with ?return= names the form to come back to.
const FreshPath = "/auth/fresh"

// The provider's timings.
const (
	// idpTimeout bounds each request to the provider.
	idpTimeout = 10 * time.Second
	// discoveryRetry is how long a failed discovery is not tried again, so
	// that a provider that is down is not asked on every sign-in.
	discoveryRetry = 5 * time.Second
)

// maxSubjectRunes bounds a subject so that, prefixed SubjectPrefix as the
// actor's subject is, it is within an item's principal's bound (R80; m4): a
// person who signs in can always be named a principal.
const maxSubjectRunes = app.MaxNamedRunes - len(SubjectPrefix)

// signingAlgs are the ID token algorithms accepted: asymmetric ones alone,
// whatever the provider's discovery names, so that neither alg none nor an
// HMAC keyed by a public key passes.
var signingAlgs = []string{
	oidc.RS256, oidc.RS384, oidc.RS512, oidc.ES256, oidc.ES384, oidc.ES512, oidc.PS256, oidc.PS384, oidc.PS512,
}

// errRedirect refuses a redirect from the provider: discovery and the
// token endpoint answer where they are asked.
var errRedirect = errors.New("the identity provider answered with a redirect, which is not followed")

// idpClient is the HTTP client of the provider (R94): the system roots and
// the PEM bundle at caBundle, if any, trusted; no proxy, since the token
// request carries the client's secret; no redirect followed; each request
// bounded.
func idpClient(caBundle string) (*http.Client, error) {
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	if caBundle != "" {
		raw, err := os.ReadFile(filepath.Clean(caBundle))
		if err != nil {
			return nil, errors.New("web.oidc.caBundle cannot be read")
		}
		if !roots.AppendCertsFromPEM(raw) {
			return nil, errors.New("web.oidc.caBundle holds no PEM certificate")
		}
	}
	dialer := &net.Dialer{Timeout: idpTimeout, KeepAlive: 30 * time.Second}
	return &http.Client{
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			TLSClientConfig:       &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout:   idpTimeout,
			ResponseHeaderTimeout: idpTimeout,
			IdleConnTimeout:       90 * time.Second,
			MaxIdleConns:          4,
			ForceAttemptHTTP2:     true,
		},
		Timeout:       idpTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errRedirect },
	}, nil
}

// discovery is the provider's discovery, made at first use and kept once
// it succeeds, so that serve starts and leads while the provider is down
// (M4). One discovery is in flight at a time, outside the lock, and the
// sign-ins that come meanwhile wait on it, each until its own request ends.
// A failure is not tried again for discoveryRetry from its end.
type discovery struct {
	mu       sync.Mutex
	provider *oidc.Provider
	failed   time.Time     // when discovery last ended in failure; zero for never
	flight   chan struct{} // closed when the discovery in flight ends; nil for none
}

// errProviderDown is a provider whose discovery fails, as a page sees it.
var errProviderDown = errors.New("the identity provider cannot be reached")

// provider is the provider, discovered if it is not yet.
func (a *Authenticator) provider(ctx context.Context) (*oidc.Provider, error) {
	d := &a.discovery
	d.mu.Lock()
	if d.provider != nil {
		p := d.provider
		d.mu.Unlock()
		return p, nil
	}
	if since := a.clock().Sub(d.failed); d.flight == nil && !d.failed.IsZero() && since >= 0 && since < discoveryRetry {
		d.mu.Unlock()
		return nil, errProviderDown
	}
	f := d.flight
	if f == nil {
		f = make(chan struct{})
		d.flight = f
		go a.discover(ctx, f)
	}
	d.mu.Unlock()
	select {
	case <-f:
	case <-ctx.Done():
		return nil, errProviderDown
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.provider == nil {
		return nil, errProviderDown
	}
	return d.provider, nil
}

// discover runs the discovery f, on a context of its own that keeps ctx's
// values but not its end, bounded by idpTimeout. It keeps the provider, or
// records the failure's end and logs it, and then ends f.
func (a *Authenticator) discover(ctx context.Context, f chan struct{}) {
	var (
		p   *oidc.Provider
		err = errors.New("discovery panicked")
	)
	defer func() {
		_ = recover()
		d := &a.discovery
		d.mu.Lock()
		defer d.mu.Unlock()
		defer close(f)
		d.flight = nil
		if err != nil {
			d.failed = a.clock()
			a.log.WarnContext(ctx, "the identity provider's discovery failed: sign-in is unavailable until it answers",
				append([]any{"issuer", a.web.OIDC.Issuer}, discoveryFailure(err)...)...)
			return
		}
		d.provider = p
	}()
	dctx, cancel := context.WithTimeout(a.idpContext(context.WithoutCancel(ctx)), idpTimeout)
	defer cancel()
	p, err = oidc.NewProvider(dctx, a.web.OIDC.Issuer)
}

// discoveryFailure are the log attributes of a failed discovery: what
// failed, and the status for an answer that was not 200, never the body
// the provider answered, which go-oidc puts in its error (spec 9.4).
func discoveryFailure(err error) []any {
	var mismatch *oidc.IssuerMismatchError
	var ue *url.Error
	switch {
	case errors.As(err, &mismatch):
		return []any{"cause", "the discovered issuer is not web.oidc.issuer"}
	case errors.As(err, &ue):
		return []any{"cause", "the provider could not be reached", "err", ue.Err}
	}
	if m := statusPrefix.FindStringSubmatch(err.Error()); m != nil {
		status, _ := strconv.Atoi(m[1])
		return []any{"cause", "the provider answered with an error", "status", status}
	}
	return []any{"cause", "the discovery document could not be read"}
}

// statusPrefix is the HTTP status go-oidc begins its error with when
// discovery is not answered 200.
var statusPrefix = regexp.MustCompile(`^([1-5]\d{2}) `)

// idpContext is ctx carrying the provider's client, for go-oidc and oauth2.
func (a *Authenticator) idpContext(ctx context.Context) context.Context {
	return oidc.ClientContext(ctx, a.client)
}

// oauth is the client's OAuth 2.0 configuration at p, with secret, which
// only the code's exchange needs.
func (a *Authenticator) oauth(p *oidc.Provider, secret string) *oauth2.Config {
	// The secret goes in the Authorization header alone
	// (client_secret_basic): never retried in the form, so that it is sent
	// once.
	endpoint := p.Endpoint()
	endpoint.AuthStyle = oauth2.AuthStyleInHeader
	return &oauth2.Config{
		ClientID: a.web.OIDC.ClientID, ClientSecret: secret, Endpoint: endpoint,
		RedirectURL: a.origin + CallbackPath,
		Scopes:      append([]string{oidc.ScopeOpenID, "profile", "email"}, a.web.OIDC.ExtraScopes...),
	}
}

// refusal is a sign-in refused: the page's status, title and words, and
// the reason logged, which never holds a token, a code or a cookie.
type refusal struct {
	status      int
	title, text string
	reason      string
	// noRole marks a person signed in at the provider with no role here.
	noRole bool
}

func (r *refusal) Error() string { return r.reason }

// failed is a refusal of a token or an exchange, all of which one page
// answers: the person can do nothing about it but tell the operators.
func failed(reason string) *refusal {
	return &refusal{
		status: http.StatusUnauthorized, title: "Sign-in failed",
		text:   "Sign-in failed: the identity provider's answer could not be accepted. Try again, and if it fails again, tell the operators.",
		reason: reason,
	}
}

// identity is what an accepted ID token says of its person.
type identity struct {
	issuer   string
	subject  string
	roles    []Role
	authTime time.Time
}

// exchange redeems the flow f's code at the provider and verifies the ID
// token it answers: its signature, by an asymmetric algorithm alone, its
// issuer, audience and expiry (go-oidc), its nonce, and its azp when it
// has several audiences (M2). It then reads the subject and the roles.
func (a *Authenticator) exchange(ctx context.Context, f *flow, code string) (*identity, error) {
	if code == "" {
		return nil, failed("the callback carries no code")
	}
	p, err := a.provider(ctx)
	if err != nil {
		return nil, &refusal{
			status: http.StatusServiceUnavailable, title: "Sign-in is unavailable",
			text: "The identity provider cannot be reached now. Try again in a minute.", reason: err.Error(),
		}
	}
	secret, err := a.keys.secrets.Secret(ctx, a.web.OIDC.ClientSecretRef)
	if err != nil {
		return nil, &refusal{
			status: http.StatusServiceUnavailable, title: "Sign-in is unavailable",
			text: "The client's secret cannot be read now. Try again in a minute.", reason: "the client secret could not be read",
		}
	}
	ictx := a.idpContext(ctx)
	tok, err := a.oauth(p, strings.TrimSpace(secret)).Exchange(ictx, code, oauth2.VerifierOption(f.Verifier))
	if err != nil {
		return nil, failed(exchangeReason(err))
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok || raw == "" {
		return nil, failed("the token response carries no ID token")
	}
	verifier := p.Verifier(&oidc.Config{ClientID: a.web.OIDC.ClientID, SupportedSigningAlgs: signingAlgs, Now: a.clock})
	idt, err := verifier.Verify(ictx, raw)
	if err != nil {
		return nil, failed(fmt.Sprintf("the ID token is refused: %v", err))
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(f.Nonce)) != 1 || f.Nonce == "" {
		return nil, failed("the ID token's nonce is not the flow's")
	}
	var claims map[string]json.RawMessage
	if err := idt.Claims(&claims); err != nil {
		return nil, failed("the ID token's claims do not decode")
	}
	if r := a.checkAZP(idt, claims); r != nil {
		return nil, r
	}
	return a.identify(idt.Issuer, claims)
}

// exchangeReason is the reason logged for a failed exchange: for the
// provider's error answer, its status and error code alone, never its body.
func exchangeReason(err error) string {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		status := 0
		if re.Response != nil {
			status = re.Response.StatusCode
		}
		return fmt.Sprintf("the code's exchange was refused: status %d, error %q", status, re.ErrorCode)
	}
	return fmt.Sprintf("the code's exchange failed: %v", err)
}

// checkAZP refuses a token whose azp is not the client, and one with
// several audiences and no azp: go-oidc checks neither (M2).
func (a *Authenticator) checkAZP(idt *oidc.IDToken, claims map[string]json.RawMessage) *refusal {
	var azp string
	if raw, ok := claims["azp"]; ok {
		if err := json.Unmarshal(raw, &azp); err != nil {
			return failed("the ID token's azp is not a string")
		}
		if azp != a.web.OIDC.ClientID {
			return failed("the ID token's azp is not this client")
		}
	}
	if len(idt.Audience) > 1 && azp == "" {
		return failed("the ID token has several audiences and no azp")
	}
	return nil
}

// identify reads the subject claim and the groups claim of claims, issued
// by issuer.
func (a *Authenticator) identify(issuer string, claims map[string]json.RawMessage) (*identity, error) {
	subject, r := subjectOf(claims, a.web.OIDC.SubjectClaim)
	if r != nil {
		return nil, r
	}
	id := &identity{issuer: issuer, subject: subject}
	if raw, ok := claims["auth_time"]; ok {
		var n json.Number
		if err := json.Unmarshal(raw, &n); err != nil {
			return nil, failed("the ID token's auth_time is not a number")
		}
		secs, err := n.Float64()
		if err != nil || secs <= 0 {
			return nil, failed("the ID token's auth_time is not a time")
		}
		id.authTime = time.Unix(int64(secs), 0).UTC()
	}
	roles, r := a.rolesOf(claims)
	if r != nil {
		return id, r
	}
	id.roles = roles
	return id, nil
}

// subjectOf is the subject claim's value, checked as R80 checks a person
// at sign-in: a string, not empty, at most maxSubjectRunes characters,
// holding no character a reader could miss (config.Unseen), and neither
// beginning nor ending with white space, any Unicode white space that
// strings.TrimSpace trims (D14). Open trims an item's principal,
// sso:<subject>, whole (R80), so a subject that ends in white space would
// sign in and then be refused as its own principal; one that begins with it
// is refused by D14's rule alone. It is never checked against the redaction
// rules (m20).
func subjectOf(claims map[string]json.RawMessage, claim string) (string, *refusal) {
	refuse := func(why string) *refusal {
		return &refusal{
			status: http.StatusForbidden, title: "Sign-in refused",
			text:   "Your identity provider gave a subject this interface cannot record: " + why + ". Tell the operators.",
			reason: "the subject claim " + why,
		}
	}
	raw, ok := claims[claim]
	if !ok {
		return "", refuse("it is missing")
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", refuse("it is not a string")
	}
	switch {
	case s == "":
		return "", refuse("it is empty")
	case utf8.RuneCountInString(s) > maxSubjectRunes:
		return "", refuse(fmt.Sprintf("it is longer than %d characters", maxSubjectRunes))
	case strings.ContainsFunc(s, config.Unseen):
		return "", refuse("it holds a character a reader could miss, such as a tab, a line feed or a zero-width space")
	case strings.TrimSpace(s) != s:
		return "", refuse("it begins or ends with white space")
	}
	return s, nil
}

// rolesOf are the roles the groups claim gives, its values matched as
// the provider emits them (R89). A missing claim, an overage marker that
// names it, or none of the deployment's groups is a person with no role.
func (a *Authenticator) rolesOf(claims map[string]json.RawMessage) ([]Role, *refusal) {
	claim := a.web.OIDC.GroupsClaim
	noRole := func(cause string) *refusal {
		return &refusal{
			status: http.StatusForbidden, title: "No role", noRole: true,
			text:   "You signed in, but you have no role here: " + cause + ". Ask the operators to add you to one of this deployment's groups.",
			reason: "no role: " + cause,
		}
	}
	if raw, ok := claims["_claim_names"]; ok {
		var names map[string]json.RawMessage
		if json.Unmarshal(raw, &names) == nil {
			if _, over := names[claim]; over {
				return nil, noRole("your identity provider has too many groups to list in the token (an overage), so it named none")
			}
		}
	}
	raw, ok := claims[claim]
	if !ok {
		return nil, noRole("your identity provider's token carries no groups claim " + fmt.Sprintf("%q", claim))
	}
	var groups []string
	if err := json.Unmarshal(raw, &groups); err != nil {
		var one string
		if json.Unmarshal(raw, &one) != nil {
			return nil, noRole(fmt.Sprintf("the groups claim %q is not a list of names", claim))
		}
		groups = []string{one}
	}
	g := a.web.Groups
	var roles []Role
	for _, m := range []struct {
		group string
		role  Role
	}{{g.Viewers, Viewer}, {g.Responders, Responder}, {g.Admins, Admin}} {
		for _, have := range groups {
			if have == m.group {
				roles = append(roles, m.role)
				break
			}
		}
	}
	if len(roles) == 0 {
		return nil, noRole("you are in none of this deployment's groups")
	}
	return roles, nil
}
