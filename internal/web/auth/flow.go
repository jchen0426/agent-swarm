package auth

import (
	"cmp"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// The sign-in flows' cookies (design D4; m21).
const (
	// flowPrefix begins every flow cookie's name; the rest is a hash of
	// the flow's state, so that each flow has its own cookie and two tabs
	// never clobber each other.
	flowPrefix = "__Host-flow-"
	// flowLife is how long a flow may take, from sign-in to the callback.
	flowLife = 10 * time.Minute
	// maxFlows is how many flows live at once; a sign-in past it drops
	// the oldest.
	maxFlows = 5
	// flowAAD binds a sealed flow to its use and format; the cookie's name,
	// and so the state's hash, and the deployment follow it.
	flowAAD = "agent-swarm web flow cookie v1"
)

// flow is one sign-in in progress, as its cookie seals it: its state, PKCE
// verifier and nonce, the page to return to, and, for a fresh
// authentication, the issuer and subject it must give again.
type flow struct {
	State    string `json:"st"`
	Verifier string `json:"pv"`
	Nonce    string `json:"n"`
	Return   string `json:"r"`
	Fresh    bool   `json:"f,omitempty"`
	Issuer   string `json:"iss,omitempty"`
	Subject  string `json:"sub,omitempty"`
	// Created orders the flows, in Unix nanoseconds; Expires ends this one,
	// in Unix seconds.
	Created int64 `json:"c"`
	Expires int64 `json:"e"`
}

// flowName is the name of the cookie of the flow of state.
func flowName(state string) string {
	sum := sha256.Sum256([]byte(state))
	return flowPrefix + base64.RawURLEncoding.EncodeToString(sum[:16])
}

// aad is the additional data a flow cookie of name is sealed with under k:
// the flow's use, then, each prefixed by its length, the deployment and
// the cookie's name.
func (k Keys) flowAAD(name string) []byte {
	out := []byte(flowAAD)
	for _, f := range []string{k.set.issuer, k.set.clientID, name} {
		out = fmt.Appendf(out, "\x00%d:%s", len(f), f)
	}
	return out
}

// sealFlow seals f under k's flow key, bound to the cookie name, in
// base64url.
func sealFlow(k Keys, name string, f *flow) (string, error) {
	if k.set == nil {
		return "", errors.New("no session key")
	}
	plain, err := json.Marshal(f)
	if err != nil {
		return "", fmt.Errorf("encode the flow: %w", err)
	}
	aead := k.set.flow
	nonce := make([]byte, aead.NonceSize(), aead.NonceSize()+len(plain)+aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("a nonce: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, plain, k.flowAAD(name))), nil
}

// openFlow opens the flow cookie name's value under k. A value sealed
// under another key or name, a session cookie's among them, is refused.
// It does not judge the flow's expiry. Its errors never repeat the value.
func openFlow(k Keys, name, value string) (flow, error) {
	if k.set == nil {
		return flow{}, errors.New("no session key")
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return flow{}, errors.New("the flow cookie is not base64url")
	}
	aead := k.set.flow
	n := aead.NonceSize()
	if len(raw) < n+aead.Overhead() {
		return flow{}, errors.New("the flow cookie is too short")
	}
	plain, err := aead.Open(nil, raw[:n], raw[n:], k.flowAAD(name))
	if err != nil {
		return flow{}, errors.New("the flow cookie does not open under the flow key")
	}
	var f flow
	if err := json.Unmarshal(plain, &f); err != nil {
		return flow{}, errors.New("the flow cookie's content does not decode")
	}
	return f, nil
}

// live says whether f has not expired at now.
func (f *flow) live(now time.Time) bool { return now.Unix() < f.Expires }

// flowCookie is a flow cookie: __Host-, Secure, HttpOnly, Lax, so that
// the provider's redirect back, a top-level GET, carries it.
func flowCookie(name, value string, now time.Time) *http.Cookie {
	return &http.Cookie{
		Name: name, Value: value, Path: "/", Expires: now.Add(flowLife).UTC(), MaxAge: int(flowLife / time.Second),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	}
}

// droppedFlow is a flow cookie's deletion.
func droppedFlow(name string) *http.Cookie {
	return &http.Cookie{
		Name: name, Path: "/", MaxAge: -1, Expires: time.Unix(0, 0),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	}
}

// capFlows makes room for one more flow among the request's flow
// cookies (m21): it deletes each that does not open or has expired, and
// the oldest live ones, until fewer than maxFlows live.
func capFlows(w http.ResponseWriter, r *http.Request, k Keys, now time.Time) {
	type live struct {
		name    string
		created int64
	}
	var flows []live
	for _, c := range r.Cookies() {
		if !strings.HasPrefix(c.Name, flowPrefix) {
			continue
		}
		f, err := openFlow(k, c.Name, c.Value)
		if err != nil || !f.live(now) {
			http.SetCookie(w, droppedFlow(c.Name))
			continue
		}
		flows = append(flows, live{c.Name, f.Created})
	}
	slices.SortFunc(flows, func(a, b live) int { return cmp.Compare(a.created, b.created) })
	for len(flows) >= maxFlows {
		http.SetCookie(w, droppedFlow(flows[0].name))
		flows = flows[1:]
	}
}

// requestFlow is the request's flow of state: its cookie opened under k,
// for that state, and live at now.
func requestFlow(r *http.Request, k Keys, state string, now time.Time) (flow, bool) {
	if state == "" {
		return flow{}, false
	}
	c, err := r.Cookie(flowName(state))
	if err != nil {
		return flow{}, false
	}
	f, err := openFlow(k, c.Name, c.Value)
	if err != nil || !f.live(now) || subtle.ConstantTimeCompare([]byte(f.State), []byte(state)) != 1 {
		return flow{}, false
	}
	return f, true
}

// randomToken is 32 random bytes in base64url: a state or a nonce.
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// SignIn is GET /auth/signin?return=: it begins a sign-in at the provider,
// in a flow cookie of its own, that comes back to the return path, or to
// the start page if the path is not safe (design D4). It answers 503 while
// the provider cannot be discovered, and refuses any method but GET.
func (a *Authenticator) SignIn() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !onlyGet(w, r) {
			return
		}
		a.begin(w, r, returnOf(r), nil)
	})
}

// returnOf is the request's return path, if safe, or the start page.
func returnOf(r *http.Request) string {
	if back, ok := SafeReturn(r.URL.Query().Get("return")); ok {
		return back
	}
	return "/"
}

// onlyGet refuses a request that is not a GET, 405 with Allow: GET, and
// says whether it was one: a flow begins, and returns, by GET alone (I8).
func onlyGet(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet {
		return true
	}
	w.Header().Set("Allow", http.MethodGet)
	page(w, http.StatusMethodNotAllowed, notice{
		Title: "Not a page to send a form to", Text: "Sign-in is reached by a link, never by a form.",
		Link: "/", LinkText: "Back to the start page",
	})
	return false
}

// begin starts a flow that returns to back: a fresh authentication of the
// session fresh, or a sign-in if fresh is nil. It seals the flow in its own
// cookie, after making room for it, and sends the person to the provider
// with PKCE, state, nonce and response_mode=query; a fresh authentication
// asks for max_age=0 and prompt=login (R76).
func (a *Authenticator) begin(w http.ResponseWriter, r *http.Request, back string, fresh *Session) {
	ctx := r.Context()
	k, err := a.keys.get(ctx)
	if err != nil {
		unavailable(w, "Sign-in is unavailable. Try again in a minute.")
		return
	}
	p, err := a.provider(ctx)
	if err != nil {
		unavailable(w, "The identity provider cannot be reached now, so nobody can sign in. Try again in a minute.")
		return
	}
	state, err := randomToken()
	if err != nil {
		unavailable(w, "Sign-in is unavailable. Try again in a minute.")
		return
	}
	nonce, err := randomToken()
	if err != nil {
		unavailable(w, "Sign-in is unavailable. Try again in a minute.")
		return
	}
	now := a.clock()
	f := &flow{
		State: state, Verifier: oauth2.GenerateVerifier(), Nonce: nonce, Return: back,
		Created: now.UnixNano(), Expires: now.Add(flowLife).Unix(),
	}
	if fresh != nil {
		f.Fresh, f.Issuer, f.Subject = true, fresh.Issuer, fresh.Subject
	}
	name := flowName(state)
	sealedFlow, err := sealFlow(k, name, f)
	if err != nil {
		unavailable(w, "Sign-in is unavailable. Try again in a minute.")
		return
	}
	capFlows(w, r, k, now)
	http.SetCookie(w, flowCookie(name, sealedFlow, now))
	opts := []oauth2.AuthCodeOption{
		oidc.Nonce(nonce), oauth2.S256ChallengeOption(f.Verifier), oauth2.SetAuthURLParam("response_mode", "query"),
	}
	if fresh != nil {
		opts = append(opts, oauth2.SetAuthURLParam("max_age", "0"), oauth2.SetAuthURLParam("prompt", "login"))
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, a.oauth(p, "").AuthCodeURL(state, opts...), http.StatusSeeOther)
}

// unavailable answers 503 with text.
func unavailable(w http.ResponseWriter, text string) {
	page(w, http.StatusServiceUnavailable, notice{Title: "Sign-in is unavailable", Text: text})
}

// Callback is GET /auth/callback, where the provider sends a person back.
// It deletes the flow's cookie whatever happens, and every answer carries
// Referrer-Policy: no-referrer and no-store, since its URL holds the code
// and the state (R87). A flow that is missing, foreign or past its ten
// minutes is refused 400; a token refused, 401, or 403 for a subject this
// interface cannot record or a person with no role. An accepted one sets
// the session, holding the token's roles alone, and answers a bare 303 to
// the flow's return path, never replaying a form (I8). Neither the query,
// nor a token, nor a cookie is ever logged.
func (a *Authenticator) Callback() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		if !onlyGet(w, r) {
			return
		}
		ctx := r.Context()
		k, err := a.keys.get(ctx)
		if err != nil {
			unavailable(w, "Sign-in is unavailable. Try again in a minute.")
			return
		}
		q := r.URL.Query()
		state := q.Get("state")
		if state != "" {
			http.SetCookie(w, droppedFlow(flowName(state)))
		}
		f, ok := requestFlow(r, k, state, a.clock())
		if !ok {
			a.log.WarnContext(ctx, "sign-in refused", "reason", "the callback's flow is missing, foreign or expired")
			page(w, http.StatusBadRequest, notice{
				Title: "Sign-in expired", Text: "This sign-in has expired, or began in another browser. Sign in again.",
				Link: SignInPath, LinkText: "Sign in again",
			})
			return
		}
		if q.Has("error") {
			a.refuse(w, r, failed("the identity provider answered with an error"))
			return
		}
		id, err := a.exchange(ctx, &f, q.Get("code"))
		if err == nil && f.Fresh {
			err = a.checkFresh(&f, id)
		}
		if err != nil {
			var ref *refusal
			if !errors.As(err, &ref) {
				ref = failed(err.Error())
			}
			a.refuse(w, r, ref)
			return
		}
		now := a.clock().Truncate(time.Second)
		s := &Session{
			Issuer: a.web.OIDC.Issuer, ClientID: a.web.OIDC.ClientID, Subject: id.subject, Roles: id.roles,
			AuthTime: id.authTime, IssuedAt: now, Expires: now.Add(a.web.SessionLength),
		}
		if err := a.SetSession(w, r, s); err != nil {
			unavailable(w, "Sign-in is unavailable. Try again in a minute.")
			return
		}
		a.log.InfoContext(ctx, "signed in", "subject", SubjectPrefix+id.subject, "roles", id.roles, "fresh", f.Fresh)
		back, safe := SafeReturn(f.Return)
		if !safe {
			back = "/"
		}
		w.Header().Set("Location", back)
		w.WriteHeader(http.StatusSeeOther)
	})
}

// refuse answers ref's page and logs its reason. A person with no role is
// signed out of any session they held: the provider now says they have
// none.
func (a *Authenticator) refuse(w http.ResponseWriter, r *http.Request, ref *refusal) {
	a.log.WarnContext(r.Context(), "sign-in refused", "reason", ref.reason)
	if ref.noRole {
		http.SetCookie(w, clearedCookie())
	}
	page(w, ref.status, notice{Title: ref.title, Text: ref.text, Link: SignInPath, LinkText: "Sign in again"})
}
