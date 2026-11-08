package webtest

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"maps"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// Signing is how the fake IdP signs its ID tokens.
type Signing int

const (
	// SignRS256 signs with the IdP's RSA key, which its JWKS publishes.
	SignRS256 Signing = iota
	// SignNone sends the token unsigned, alg none.
	SignNone
	// SignHS256WithPublicKey signs with HS256 keyed by the IdP's public key
	// in PEM: the algorithm-confusion attack.
	SignHS256WithPublicKey
)

// Client is the one client the fake IdP knows.
type Client struct {
	ID     string
	Secret string //nolint:gosec // G117: a test client's secret, which the fake IdP checks.
	// RedirectURL is the client's one redirect URL.
	RedirectURL string
}

// IDP is a fake OIDC provider (I4): an httptest TLS server under a
// certificate of a given CA, with discovery, an authorization endpoint
// that signs its person in at once, a token endpoint that checks the
// client, the code and PKCE, and a JWKS of a real RSA key. Its URL is its
// issuer. The claims its tokens carry are settable, and switches make it
// ignore prompt=login and max_age, omit auth_time, sign badly, or answer
// every request 503 as if it were down.
type IDP struct {
	// URL is the IdP's issuer, https://127.0.0.1:<port>.
	URL string

	t      testing.TB
	key    *rsa.PrivateKey
	client Client

	mu           sync.Mutex
	now          func() time.Time
	claims       map[string]any
	ignorePrompt bool
	omitAuth     bool
	down         bool
	signing      Signing
	login        time.Time // when its person last authenticated; zero for never
	codes        map[string]*grant
	queries      []url.Values
	issued       []string
	refuseBasic  bool
	discoveries  int
	onDiscovery  func()
	tokens       []TokenRequest
}

// TokenRequest is how a request to the token endpoint carried the
// client's secret: in the Authorization header, in the form, or both.
type TokenRequest struct {
	Basic, Form bool
}

// grant is an authorization code's request.
type grant struct {
	nonce, challenge, redirect string
	authTime                   time.Time
}

// keyID is the JWKS key's id; hmacKeyID is the id of the HMAC key the
// JWKS also publishes while the IdP signs with HS256, so that only the
// client's list of algorithms refuses such a token. The HMAC key names no
// alg: go-oidc drops a key whose alg it does not support.
const (
	keyID     = "webtest-1"
	hmacKeyID = "webtest-hs"
)

// NewIDP starts a fake IdP for client, under a certificate ca issues. It
// is closed when the test ends. Its person is "alice" with no groups
// claim until SetClaims says otherwise.
func NewIDP(t testing.TB, ca *CA, client Client) *IDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("an RSA key: %v", err)
	}
	p := &IDP{
		t: t, key: key, client: client, now: time.Now,
		claims: map[string]any{"sub": "alice"}, codes: map[string]*grant{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("GET /authorize", p.authorize)
	mux.HandleFunc("POST /token", p.token)
	mux.HandleFunc("GET /jwks", p.jwks)
	s := ca.UnstartedServer(t, p.unlessDown(mux))
	s.StartTLS()
	p.URL = s.URL
	return p
}

// RefuseBasic makes the token endpoint refuse a client that authenticates
// with the Authorization header, as a provider that takes
// client_secret_post alone does.
func (p *IDP) RefuseBasic(on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refuseBasic = on
}

// OnDiscovery sets a function each discovery request calls before it is
// answered, as down or not: a test blocks in it, or moves its clock, to
// make a slow provider. Discovery requests are counted (Discoveries).
func (p *IDP) OnDiscovery(f func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onDiscovery = f
}

// Discoveries is how many discovery requests the IdP received.
func (p *IDP) Discoveries() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.discoveries
}

// TokenRequests are how the token endpoint's requests carried the
// client's secret, in order.
func (p *IDP) TokenRequests() []TokenRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]TokenRequest(nil), p.tokens...)
}

// SetClock sets the clock the IdP's tokens and logins are timed by.
func (p *IDP) SetClock(now func() time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.now = now
}

// SetClaims sets the claims the IdP's ID tokens carry besides, and over,
// iss, aud, exp, iat, nonce and auth_time. A claim set to nil is left out.
func (p *IDP) SetClaims(claims map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.claims = maps.Clone(claims)
}

// IgnorePromptLogin makes the IdP sign its person in without
// authenticating them again, whatever prompt and max_age ask.
func (p *IDP) IgnorePromptLogin(on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ignorePrompt = on
}

// OmitAuthTime makes the IdP's tokens carry no auth_time.
func (p *IDP) OmitAuthTime(on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.omitAuth = on
}

// SetDown makes the IdP answer every request 503, or serve again.
func (p *IDP) SetDown(down bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.down = down
}

// SetSigning sets how the IdP signs its ID tokens.
func (p *IDP) SetSigning(s Signing) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.signing = s
}

// SetLogin sets when the IdP's person last authenticated, which the next
// authorization that does not authenticate them again gives as auth_time.
func (p *IDP) SetLogin(at time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.login = at
}

// Authorizations are the queries of the authorization requests the IdP
// received, in order.
func (p *IDP) Authorizations() []url.Values {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]url.Values, len(p.queries))
	for i, q := range p.queries {
		out[i] = maps.Clone(q)
	}
	return out
}

// Issued are the secrets the IdP gave out or received: every code, state,
// access token and ID token, for a test to check that none is logged.
func (p *IDP) Issued() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.issued...)
}

func (p *IDP) unlessDown(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		down := p.down
		p.mu.Unlock()
		if down && r.URL.Path != discoveryPath {
			http.Error(w, DownBody, http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// discoveryPath is the discovery document's path.
const discoveryPath = "/.well-known/openid-configuration"

// DownBody is the body of the IdP's answers while it is down, for a test
// to check that no log holds it.
const DownBody = "webtest IdP is down: this body must never be logged"

func (p *IDP) discovery(w http.ResponseWriter, _ *http.Request) {
	p.mu.Lock()
	p.discoveries++
	hook := p.onDiscovery
	p.mu.Unlock()
	if hook != nil {
		hook()
	}
	p.mu.Lock()
	down, algs := p.down, []string{"RS256"}
	if p.signing == SignHS256WithPublicKey {
		algs = append(algs, "HS256")
	}
	p.mu.Unlock()
	if down {
		http.Error(w, DownBody, http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                p.URL,
		"authorization_endpoint":                p.URL + "/authorize",
		"token_endpoint":                        p.URL + "/token",
		"jwks_uri":                              p.URL + "/jwks",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": algs,
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
	})
}

func (p *IDP) jwks(w http.ResponseWriter, _ *http.Request) {
	set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
		Key: &p.key.PublicKey, KeyID: keyID, Algorithm: string(jose.RS256), Use: "sig",
	}}}
	p.mu.Lock()
	hs := p.signing == SignHS256WithPublicKey
	p.mu.Unlock()
	if hs {
		set.Keys = append(set.Keys, jose.JSONWebKey{Key: p.publicPEM(), KeyID: hmacKeyID, Use: "sig"})
	}
	writeJSON(w, http.StatusOK, set)
}

// publicPEM is the IdP's public key in PEM.
func (p *IDP) publicPEM() []byte {
	der, err := x509.MarshalPKIXPublicKey(&p.key.PublicKey)
	if err != nil {
		p.t.Errorf("encode the public key: %v", err)
		return nil
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

// authorize signs its person in, authenticating them again when the
// request asks for it by prompt=login or max_age=0, unless told to ignore
// that, and redirects to the client's redirect URL with a code.
func (p *IDP) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.queries = append(p.queries, q)
	p.issued = append(p.issued, q.Get("state"))
	switch {
	case q.Get("client_id") != p.client.ID:
		http.Error(w, "unknown client", http.StatusBadRequest)
		return
	case q.Get("redirect_uri") != p.client.RedirectURL:
		http.Error(w, "unregistered redirect_uri", http.StatusBadRequest)
		return
	case q.Get("response_type") != "code":
		http.Error(w, "unsupported response_type", http.StatusBadRequest)
		return
	case q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "":
		http.Error(w, "PKCE S256 is required", http.StatusBadRequest)
		return
	}
	again := q.Get("prompt") == "login" || q.Get("max_age") == "0"
	if p.login.IsZero() || (again && !p.ignorePrompt) {
		p.login = p.now()
	}
	code := random(p.t)
	p.codes[code] = &grant{
		nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri"), authTime: p.login,
	}
	p.issued = append(p.issued, code)
	back, err := url.Parse(p.client.RedirectURL)
	if err != nil {
		http.Error(w, "bad redirect", http.StatusInternalServerError)
		return
	}
	v := url.Values{"code": {code}, "state": {q.Get("state")}}
	back.RawQuery = v.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

// token exchanges a code once, for the client that it was issued to, by
// its secret (basic or in the form), its redirect URL and its PKCE
// verifier.
func (p *IDP) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	id, secret, basic := r.BasicAuth()
	form := r.PostForm.Has("client_secret")
	p.mu.Lock()
	p.tokens = append(p.tokens, TokenRequest{Basic: basic, Form: form})
	refuseBasic := p.refuseBasic
	p.mu.Unlock()
	if basic && refuseBasic {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
		return
	}
	if basic {
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	} else {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != p.client.ID || subtle.ConstantTimeCompare([]byte(secret), []byte(p.client.Secret)) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	code := r.PostForm.Get("code")
	g, ok := p.codes[code]
	delete(p.codes, code)
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	switch {
	case !ok, r.PostForm.Get("grant_type") != "authorization_code", r.PostForm.Get("redirect_uri") != g.redirect,
		base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	idToken := p.mint(g)
	access := random(p.t)
	p.issued = append(p.issued, idToken, access)
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": access, "token_type": "Bearer", "expires_in": 300, "id_token": idToken,
	})
}

// mint is the ID token of g, signed as the IdP is set to. Its caller holds
// mu.
func (p *IDP) mint(g *grant) string {
	now := p.now()
	claims := map[string]any{
		"iss": p.URL, "aud": p.client.ID, "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
	}
	if g.nonce != "" {
		claims["nonce"] = g.nonce
	}
	if !p.omitAuth {
		claims["auth_time"] = g.authTime.Unix()
	}
	for k, v := range p.claims {
		if v == nil {
			delete(claims, k)
			continue
		}
		claims[k] = v
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		p.t.Errorf("encode the claims: %v", err)
		return ""
	}
	return p.sign(payload)
}

// sign signs payload as the IdP is set to.
func (p *IDP) sign(payload []byte) string {
	enc := base64.RawURLEncoding.EncodeToString
	switch p.signing {
	case SignNone:
		return enc([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + enc(payload) + "."
	case SignHS256WithPublicKey:
		signing := enc([]byte(`{"alg":"HS256","typ":"JWT","kid":"`+hmacKeyID+`"}`)) + "." + enc(payload)
		h := hmac.New(sha256.New, p.publicPEM())
		_, _ = h.Write([]byte(signing))
		return signing + "." + enc(h.Sum(nil))
	default:
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: p.key},
			(&jose.SignerOptions{}).WithType("JWT").WithHeader(jose.HeaderKey("kid"), keyID))
		if err != nil {
			p.t.Errorf("a signer: %v", err)
			return ""
		}
		jws, err := signer.Sign(payload)
		if err != nil {
			p.t.Errorf("sign: %v", err)
			return ""
		}
		out, err := jws.CompactSerialize()
		if err != nil {
			p.t.Errorf("serialize: %v", err)
			return ""
		}
		return out
	}
}

// random is 32 random bytes in base64url.
func random(t testing.TB) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Errorf("random bytes: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
