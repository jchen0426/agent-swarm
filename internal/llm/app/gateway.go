package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jchen0426/agent-swarm/internal/actions/limits"
	"github.com/jchen0426/agent-swarm/internal/actions/redact"
	"github.com/jchen0426/agent-swarm/internal/llm/completion"
	"github.com/jchen0426/agent-swarm/internal/llm/cost"
	"github.com/jchen0426/agent-swarm/internal/llm/prompt"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
)

// Path is the one endpoint the gateway offers.
const Path = "/v1/messages"

// serviceTier is the tier every request is sent at: the standard one, which
// the models' cards price.
const serviceTier = "standard_only"

// maxRecordedRequest caps what the record keeps of a refused request.
const maxRecordedRequest = 4096

// MaxSessions bounds the sessions the gateway keeps state for: past it, it
// forgets the session used least recently, whose thinking blocks are then
// refused if they come back, and its tool calls' ids if they hold what a
// redaction rule matches.
const MaxSessions = 1024

// maxThinking bounds the thinking blocks the gateway keeps for a session:
// past it, it forgets the oldest.
const maxThinking = 1024

// maxCalls bounds the ids of tool calls the gateway keeps for a session:
// past it, it forgets the oldest. Each is at most 128 bytes, since the
// completion leaves out an id no request can hold.
const maxCalls = 1024

// Options configure the gateway (spec Section 15).
type Options struct {
	// MaxBodyBytes caps a request, which bounds what one call can cost.
	MaxBodyBytes int
	// MaxTokens caps a request's max_tokens.
	MaxTokens int64
	// Version is the anthropic-version every request carries.
	Version string
	// Limits are the deployment's daily cap and shadow cut.
	Limits cost.Limits
	// Cards are the models' prices. A model without a card is refused.
	Cards map[string]cost.Card
	// Reference is the price of one cost-weighted token, in pico-USD.
	Reference int64
	// InferenceGeo, when set, is the region every request is served in.
	InferenceGeo string
	// Vendor are the vendor's limits: calls at once, the share of
	// comparison and probation runs, and when it is degraded. The vendor
	// serves agents only, so no slot is kept for platform reads.
	Vendor limits.Settings
	// Backoff are the waits between attempts at a failure another attempt
	// may get past; the vendor's retry-after wins when it is longer.
	Backoff []time.Duration
	// VendorTimeout bounds a call to the vendor, which goes on after its
	// request has been answered so that its cost is recorded.
	VendorTimeout time.Duration
	// RecordTimeout bounds each read and write of the record, which run
	// apart from the request's context, and is the wait before an outcome
	// that could not be recorded is tried again.
	RecordTimeout time.Duration
	// MaxRefusals caps the refused requests a session's round records;
	// past it, the session is shut out until the next round.
	MaxRefusals int
	// LastCallWait is how long a session's request waits for a call that
	// goes on after its own request was answered, before it goes on beside
	// it.
	LastCallWait time.Duration
}

// IDs makes call ids.
type IDs interface {
	NewID() string
}

// Ports are what the gateway reaches through.
type Ports struct {
	Sessions  Sessions
	Revisions Revisions
	Vendor    Vendor
	Calls     Calls
	IDs       IDs
	Now       func() time.Time
}

// Inbound is one request a runtime sent.
type Inbound struct {
	Method, Path string
	// Query is the request's query string.
	Query string
	// Token is the session's token, as the request carried it.
	Token  string
	Header map[string][]string
	Body   []byte
	// Length is the size the request declared, if it declared one: the
	// record of a request larger than the gateway reads keeps it.
	Length int64
}

// Reply is the gateway's answer.
type Reply struct {
	Status int
	Header map[string]string
	Body   []byte
}

// Gateway is the LLM gateway: the only route from an agent's sandbox to
// the model vendor (spec Section 2). It forwards a request only when the
// session is live and in its round, the request is within the allowlist
// and the agent's configuration, the run has budget left and the day's
// spend allows it (spec Sections 5.6, 7 and 9.3). It records every request
// a session sends, refused or not, and records a forwarded one before it
// reaches the vendor, redacted as it was sent (spec Section 9.4). A session
// has one request in the gateway's hands at a time, and at most two calls
// at the vendor: a call that goes on after its request was answered holds
// the session for LastCallWait. The gateway holds its state in memory, so
// one instance is active at a time, as the gateway lease ensures (spec
// Section 8.2).
type Gateway struct {
	p      Ports
	opts   Options
	vendor *limits.Server

	mu       sync.Mutex
	lanes    map[sessionKey]*lane
	sessions map[sessionKey]*sessionState
	tick     uint64
	// busy counts the requests in the gateway's hands and the calls that
	// outlived them, until their outcomes are recorded. Once closing is
	// set, the gateway takes no request, and drained is closed when busy
	// falls to zero.
	busy    int
	closing bool
	drained chan struct{}
	// heard is when the vendor last answered an attempt, in Unix
	// nanoseconds.
	heard atomic.Int64
}

type sessionKey struct{ run, agent string }

// lane is a session's place at the gateway: the request in its hands, if
// any, and the calls that go on after their requests were answered.
type lane struct {
	// held is set while a request is in the gateway's hands, and waiting
	// while one waits for a call that goes on.
	held, waiting bool
	// going counts the calls that go on after their requests were
	// answered, and since is when the latest of them started.
	going int
	since time.Time
	// ended is closed, and made anew, when one of them ends.
	ended chan struct{}
}

// flight is one request in the gateway's hands and the call it made: open
// until the request is answered, calling while the call goes on, which
// started at started.
type flight struct {
	open, calling bool
	started       time.Time
}

// sessionState is what the gateway keeps of a session: the thinking blocks
// and the ids of the tool calls it gave it, oldest first, and the refusals of
// its current round.
type sessionState struct {
	used      uint64
	thinking  map[[32]byte]bool
	order     [][32]byte
	calls     map[string]bool
	callOrder []string
	round     int
	refused   int
	noticed   bool
}

// New returns a gateway.
func New(p *Ports, opts *Options) (*Gateway, error) {
	if err := opts.check(); err != nil {
		return nil, fmt.Errorf("app: %w", err)
	}
	vendor := opts.Vendor
	vendor.AgentsOnly = true
	return &Gateway{
		p: *p, opts: *opts, vendor: limits.New(vendor, nil, p.Now).Server("model vendor"),
		lanes: map[sessionKey]*lane{}, sessions: map[sessionKey]*sessionState{}, drained: make(chan struct{}),
	}, nil
}

// check reports options that cannot hold.
func (o *Options) check() error {
	if err := o.Limits.Check(); err != nil {
		return err
	}
	for model, card := range o.Cards {
		if err := card.Check(); err != nil {
			return fmt.Errorf("the card of %s: %w", model, err)
		}
	}
	if o.MaxBodyBytes <= 0 || o.MaxBodyBytes > cost.MaxCount || o.MaxTokens <= 0 || o.MaxTokens > cost.MaxCount ||
		o.Version == "" || o.Reference <= 0 || o.Reference > cost.MaxRate {
		return errors.New("the caps, the API version and the reference price must be set, within what a card prices")
	}
	return o.checkWaits()
}

// checkWaits reports timeouts, waits or a cap of refusals that cannot hold.
func (o *Options) checkWaits() error {
	if o.RecordTimeout <= 0 || o.VendorTimeout <= 0 || o.LastCallWait <= 0 || o.MaxRefusals <= 0 || anyNotPositive(o.Backoff) {
		return errors.New("the timeouts, the waits and the refusals a round records must be above zero")
	}
	return nil
}

func anyNotPositive(ds []time.Duration) bool {
	for _, d := range ds {
		if d <= 0 {
			return true
		}
	}
	return false
}

// Drain stops the gateway taking requests, and waits until those in its
// hands are answered and every call they made has ended and its outcome has
// been recorded, or ctx ends. A call it leaves going on counts at its
// estimate for good.
func (g *Gateway) Drain(ctx context.Context) error {
	g.mu.Lock()
	g.closing = true
	g.settle()
	drained := g.drained
	g.mu.Unlock()
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// enter counts a request in the gateway's hands, unless the gateway is
// stopping.
func (g *Gateway) enter() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closing {
		return false
	}
	g.busy++
	return true
}

// keep counts a call that may outlive its request, which is counted
// already.
func (g *Gateway) keep() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.busy++
}

// leave uncounts a request, or a call that outlived it.
func (g *Gateway) leave() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.busy--
	g.settle()
}

// settle closes drained once the gateway is stopping and nothing is in its
// hands. The caller holds g.mu.
func (g *Gateway) settle() {
	if !g.closing || g.busy > 0 {
		return
	}
	select {
	case <-g.drained:
	default:
		close(g.drained)
	}
}

// call is one request in the gateway's hands.
type call struct {
	g      *Gateway
	s      *Session
	red    *redact.Redactor
	rec    *CallRecord
	flight *flight
}

func (c *call) key() sessionKey { return sessionKey{c.s.Run, c.s.Agent} }

// hides reports whether a value of the client's holds, on its own, what the
// session's redaction rules hide: a refusal, stored redacted as a whole
// sentence, quotes no such value (prompt.Quote).
func (c *call) hides(s string) bool { return c.red.Text(s) != s || c.red.Escaped(s) }

// Messages serves one request: it checks it, records it, forwards it and
// records how it ended.
func (g *Gateway) Messages(ctx context.Context, in *Inbound) *Reply {
	if !g.enter() {
		return refusal(http.StatusServiceUnavailable, "api_error", "the gateway is stopping; try again", true)
	}
	defer g.leave()
	s, err := g.p.Sessions.Session(ctx, in.Token)
	switch {
	case errors.Is(err, ErrNoSession):
		return refusal(http.StatusUnauthorized, "authentication_error", "a live session's token is required", false)
	case err != nil:
		return refusal(http.StatusServiceUnavailable, "api_error", "the session could not be read; try again", true)
	}
	c := &call{g: g, s: &s, red: redact.New(), rec: &CallRecord{
		ID: g.p.IDs.NewID(), Run: s.Run, Agent: s.Agent, Round: s.Round, Class: s.Class, At: g.p.Now(),
		RequestBytes: max(int64(len(in.Body)), in.Length),
	}}
	if g.shutOut(&s) {
		return c.refuse(ctx, http.StatusTooManyRequests, "rate_limit_error", "the session is shut out until the next round")
	}
	rev, err := g.p.Revisions.At(ctx, s.Commit)
	if err != nil {
		return refusal(http.StatusServiceUnavailable, "api_error", "the configuration could not be read; try again", true)
	}
	c.red = always(rev).Before(redact.Rule{Literals: []string{in.Token}, Replacement: config.SessionTokenReplacement})
	switch {
	case in.Method != http.MethodPost || in.Path != Path || in.Query != "":
		return c.refuse(ctx, http.StatusNotFound, "not_found_error", fmt.Sprintf("only POST %s is offered, not %s %s", Path, prompt.Quote(in.Method, c.hides), prompt.Quote(in.Path+"?"+in.Query, c.hides)))
	case len(in.Body) > g.opts.MaxBodyBytes:
		return c.refuse(ctx, http.StatusRequestEntityTooLarge, "request_too_large", fmt.Sprintf("a request is at most %d bytes", g.opts.MaxBodyBytes))
	case !s.Deadline.IsZero() && !time.Now().Before(s.Deadline):
		return c.refuse(ctx, http.StatusForbidden, "permission_error", "the session's round has ended")
	}
	if reply := c.take(ctx); reply != nil {
		return reply
	}
	defer g.land(c, true)
	return c.serve(ctx, rev, in)
}

// serve checks a request against the agent's configuration, the allowlist
// and the budgets, then records and forwards it.
func (c *call) serve(ctx context.Context, rev *config.Revision, in *Inbound) *Reply {
	g := c.g
	agent := rev.Agent(c.s.Agent)
	if agent == nil {
		return c.refuse(ctx, http.StatusForbidden, "permission_error", "the session's agent is not in the configuration")
	}
	card, ok := g.opts.Cards[agent.Spec.Model]
	if !ok {
		return c.refuse(ctx, http.StatusForbidden, "permission_error", "no price is set for the agent's model")
	}
	req, err := prompt.Check(in.Header, in.Body, &prompt.Policy{
		Model: agent.Spec.Model, MaxTokens: g.opts.MaxTokens, Version: g.opts.Version, Answered: g.answered(c.s), Called: g.called(c.s),
		Hides: c.hides,
	})
	if err == nil {
		err = req.Redact(c.red)
	}
	if err != nil {
		c.recordRefused(in.Body)
		return c.refuse(ctx, http.StatusBadRequest, "invalid_request_error", err.Error())
	}
	c.rec.Model = req.Model()
	if reply := c.admit(ctx, in.Body); reply != nil {
		return reply
	}
	req.Pin(serviceTier, g.opts.InferenceGeo)
	body := req.Encode()
	record := req.Record(c.red)
	slot, reply := c.acquire(ctx, in.Body)
	if reply != nil {
		return reply
	}
	sum := sha256.Sum256([]byte(record))
	c.rec.Request, c.rec.RequestBytes, c.rec.RequestDigest = record, int64(len(body)), hex.EncodeToString(sum[:])
	c.rec.Estimate = cost.Weighted(card.Spend(cost.Estimate(c.rec.RequestBytes, req.MaxTokens())), g.opts.Reference)
	if !c.store(ctx, func(ctx context.Context) error { return g.p.Calls.Begin(ctx, c.rec) }) {
		slot()
		return refusal(http.StatusServiceUnavailable, "api_error", "the call could not be recorded, so it was not made", true)
	}
	if err := c.over(ctx); err != nil {
		slot()
		c.notMade(ctx, "the call was not made: "+err.Error())
		return c.cut("before the call was made")
	}
	return c.forward(ctx, slot, body, req.MaxTokens(), &card)
}

// notMade records that a call recorded was not made, which costs nothing,
// trying again apart from the request while the record fails. It counts
// against the session's round as a request dropped earlier does: its record
// holds its request in full.
func (c *call) notMade(ctx context.Context, reason string) {
	c.g.reserve(c.s)
	o := &Outcome{ID: c.rec.ID, Status: StatusError, Reason: reason}
	if c.end(ctx, o) {
		return
	}
	c.g.keep()
	go func() {
		defer c.g.leave()
		c.recordLater(context.WithoutCancel(ctx), o)
	}()
}

// admit reports, as a refusal, a call its run's budget or the day's spend
// does not allow.
func (c *call) admit(ctx context.Context, body []byte) *Reply {
	var run, today int64
	if !c.store(ctx, func(ctx context.Context) (err error) { run, err = c.g.p.Calls.Spent(ctx, c.s.Run); return err }) {
		return refusal(http.StatusServiceUnavailable, "api_error", "the run's spend could not be read; try again", true)
	}
	if !c.store(ctx, func(ctx context.Context) (err error) {
		today, err = c.g.p.Calls.SpentSince(ctx, cost.DayStart(c.g.p.Now()))
		return err
	}) {
		return refusal(http.StatusServiceUnavailable, "api_error", "the day's spend could not be read; try again", true)
	}
	if err := cost.Admit(run, c.s.Budget, today, c.s.Class, c.g.opts.Limits); err != nil {
		c.recordRefused(body)
		return c.refuse(ctx, http.StatusForbidden, "permission_error", err.Error())
	}
	return nil
}

// acquire takes a slot at the vendor for a call, unless the vendor is
// failing, or the request, the round or the run ends first; a call that
// does not start is declined.
func (c *call) acquire(ctx context.Context, body []byte) (func(), *Reply) {
	bounded, cancel := c.bounded(ctx)
	slot, err := c.g.vendor.Acquire(bounded, classOf(c.s.Class))
	ended := bounded.Err()
	cancel()
	if err == nil && ended != nil {
		slot()
		err = ended
	}
	if err == nil {
		return slot, nil
	}
	reply := refusal(http.StatusServiceUnavailable, "overloaded_error", "the model vendor is failing; the call was not made", false)
	reason := "the model vendor is failing: " + err.Error()
	if !errors.Is(err, limits.ErrDegraded) {
		reply, reason = c.cut("before the call was made"), "the request ended before the call was made: "+err.Error()
	}
	c.recordRefused(body)
	return nil, c.decline(ctx, StatusError, reason, reply)
}

// forward makes the call to the vendor apart from the request: the request
// is answered when the vendor answers, or when it ends, the run stops or
// the round ends, whichever comes first; the call goes on until the vendor
// answers or VendorTimeout passes, though it makes no attempt after the
// request has ended, and its outcome, with what it cost, is recorded either
// way. Until the call ends and its outcome has been tried
// once, it holds the session's place (take); a later try runs apart.
func (c *call) forward(ctx context.Context, slot func(), body []byte, maxTokens int64, card *cost.Card) *Reply {
	c.g.mu.Lock()
	c.flight.calling, c.flight.started = true, time.Now()
	c.g.mu.Unlock()
	c.g.keep()
	done := make(chan *result, 1)
	go func() {
		defer c.g.leave()
		detached := context.WithoutCancel(ctx)
		r := c.attempts(detached, ctx.Done(), slot, body, maxTokens, card)
		done <- r
		c.g.land(c, false)
		if !r.recorded {
			c.recordLater(detached, r.outcome)
		}
	}()
	bounded, cancel := c.bounded(ctx)
	defer cancel()
	select {
	case r := <-done:
		c.g.remember(c.s, r.thinking, r.calls)
		return r.reply
	case <-bounded.Done():
		return c.cut("before the model answered")
	}
}

// result is how a forwarded call ended: its reply, its outcome, whether
// the outcome is recorded, and the thinking blocks and the tool calls' ids of
// an answer returned.
type result struct {
	reply    *Reply
	outcome  *Outcome
	thinking [][32]byte
	calls    []string
	recorded bool
}

// attempts sends the request until the vendor answers it, or answers with a
// failure no other attempt gets past, or the request, the round, the run or
// the call's time leaves no room for another; then it frees the vendor's
// slot and records how the call ended. ended is closed when the request
// ends: an attempt made after it would go to the vendor, and could be
// charged for, with no one to read its answer.
func (c *call) attempts(ctx context.Context, ended <-chan struct{}, slot func(), body []byte, maxTokens int64, card *cost.Card) *result {
	ctx, cancel := context.WithTimeout(ctx, c.g.opts.VendorTimeout)
	defer cancel()
	var answer *Answer
	var err error
	n := 0
	for {
		answer, err = c.g.p.Vendor.Send(ctx, body)
		n++
		if err == nil {
			c.g.heard.Store(time.Now().UnixNano())
		}
		if !transient(answer, err) || n > len(c.g.opts.Backoff) {
			break
		}
		wait := jitter(c.g.opts.Backoff[n-1])
		if answer != nil {
			wait = max(wait, answer.RetryAfter)
		}
		if !c.wait(ctx, ended, wait) {
			break
		}
	}
	slot()
	if err != nil || transient(answer, nil) {
		c.g.vendor.Failed()
	} else {
		c.g.vendor.Succeeded()
	}
	return c.finish(ctx, answer, n, err, body, maxTokens, card)
}

// wait waits d before another attempt, unless the round would end first,
// or the request ends, the run stops or the call's time is up meanwhile.
func (c *call) wait(ctx context.Context, ended <-chan struct{}, d time.Duration) bool {
	if !c.s.Deadline.IsZero() && time.Until(c.s.Deadline) <= d {
		return false
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ended:
		return false
	case <-c.s.Done:
		return false
	case <-ctx.Done():
		return false
	}
}

// finish records how a forwarded call ended and answers it. A call whose
// answer was lost, or could not be read, counts its worst case. An answer
// that cannot be recorded is not returned: it would be model output the
// audit store does not hold. So an answer whose text hides what a redaction
// rule matches behind an escape, which no record may hold, is withheld as an
// error at the tokens the vendor reported. A withheld answer's thinking blocks
// and tool calls' ids are not remembered, and a returned answer's are. A
// request the vendor finds invalid or too large counts against the session's
// round.
func (c *call) finish(ctx context.Context, answer *Answer, attempts int, err error, body []byte, maxTokens int64, card *cost.Card) *result {
	o := &Outcome{ID: c.rec.ID, Status: StatusError, Attempts: attempts}
	var thinking [][32]byte
	var calls []string
	var reply *Reply
	switch {
	case errors.Is(err, ErrLost):
		o.Status, o.Usage = StatusUnknown, cost.Estimate(int64(len(body)), maxTokens)
		o.Reason = c.red.Text("the answer was lost: " + err.Error())
		reply = refusal(http.StatusBadGateway, "api_error", "the model vendor's answer was lost", false)
	case err != nil:
		o.Reason = c.red.Text("the vendor could not be reached: " + err.Error())
		reply = refusal(http.StatusBadGateway, "api_error", "the model vendor could not be reached", false)
	case answer.Status != http.StatusOK:
		o.Reason = fmt.Sprintf("the vendor answered %d: %s", answer.Status, completion.Failure(answer.Body, c.red))
		reply = &Reply{Status: answer.Status, Header: map[string]string{"Content-Type": "application/json", "X-Should-Retry": "false"}, Body: answer.Body}
		if answer.Status == http.StatusBadRequest || answer.Status == http.StatusRequestEntityTooLarge {
			c.g.vendorRefused(c.s)
		}
	default:
		comp, err := completion.Read(answer.Body, c.red)
		if err != nil {
			o.Status, o.Usage = StatusUnknown, cost.Estimate(int64(len(body)), maxTokens)
			o.Reason = "the answer could not be read: " + err.Error()
			reply = refusal(http.StatusBadGateway, "api_error", "the model vendor's answer could not be read", false)
			break
		}
		if comp.Record == "" {
			o.Usage, o.Details = comp.Usage, comp.Details
			o.Reason = "the answer was withheld: its text hides what a redaction rule matches behind an escape"
			reply = refusal(http.StatusBadGateway, "api_error", "the model vendor's answer could not be recorded safely, so it is not returned", false)
			break
		}
		o.Status, o.Completion, o.Usage, o.Details, thinking, calls = StatusOK, comp.Record, comp.Usage, comp.Details, comp.Thinking, comp.Calls
		reply = &Reply{Status: http.StatusOK, Header: map[string]string{"Content-Type": "application/json"}, Body: answer.Body}
	}
	o.Spend = card.Spend(o.Usage)
	o.Weighted = cost.Weighted(o.Spend, c.g.opts.Reference)
	if answer != nil && answer.RequestID != "" {
		reply.Header["Request-Id"] = answer.RequestID
	}
	if !c.end(ctx, o) {
		return &result{reply: refusal(http.StatusServiceUnavailable, "api_error", "the answer could not be recorded, so it is not returned", false), outcome: o}
	}
	return &result{reply: reply, outcome: o, thinking: thinking, calls: calls, recorded: true}
}

// recordLater tries again to record an outcome that could not be, after
// each RecordTimeout, until it is or VendorTimeout has passed. Until then,
// the call counts at its estimate.
func (c *call) recordLater(ctx context.Context, o *Outcome) {
	for until := time.Now().Add(c.g.opts.VendorTimeout); time.Now().Before(until); {
		time.Sleep(c.g.opts.RecordTimeout)
		if c.end(ctx, o) {
			return
		}
	}
}

// recordRefused keeps of a refused request what its record may hold: at
// most the first maxRecordedRequest bytes of its JSON, redacted, the size
// it came in, and the digest of its redacted JSON in full.
func (c *call) recordRefused(body []byte) {
	full := prompt.Redacted(body, c.red)
	if full == "" {
		return
	}
	sum := sha256.Sum256([]byte(full))
	c.rec.RequestDigest = hex.EncodeToString(sum[:])
	if len(full) > maxRecordedRequest {
		full = strings.ToValidUTF8(full[:maxRecordedRequest], "") + "…"
	}
	c.rec.Request = full
}

// refuse records a request the gateway refuses, with its reason, and
// answers it.
func (c *call) refuse(ctx context.Context, status int, kind, reason string) *Reply {
	return c.decline(ctx, StatusRefused, reason, refusal(status, kind, reason, false))
}

// decline records a request the gateway does not forward, with its status
// and reason, and answers it with reply. It counts against the session's
// round before it is recorded, so that the cap holds for requests sent at
// once: past the cap, the session's shut-out is recorded once, and later
// requests are answered unrecorded. So a sandbox cannot fill the audit
// store with requests it has refused, drops at once, or sends while the
// vendor is failing. A request that cannot be recorded is answered as the
// audit store being unavailable.
func (c *call) decline(ctx context.Context, status Status, reason string, reply *Reply) *Reply {
	switch c.g.reserve(c.s) {
	case recordShutOut:
		status, reason = StatusRefused, "the session's requests were refused too often this round; it is shut out until the next"
		reply = refusal(http.StatusTooManyRequests, "rate_limit_error", reason, false)
	case unrecorded:
		return refusal(http.StatusTooManyRequests, "rate_limit_error", "the session is shut out until the next round", false)
	}
	if !c.record(ctx, status, reason) {
		return refusal(http.StatusServiceUnavailable, "api_error", "the request could not be recorded; try again", true)
	}
	return reply
}

// record records a request that is not forwarded, and how it ended.
func (c *call) record(ctx context.Context, status Status, reason string) bool {
	return c.store(ctx, func(ctx context.Context) error { return c.g.p.Calls.Begin(ctx, c.rec) }) &&
		c.end(ctx, &Outcome{ID: c.rec.ID, Status: status, Reason: c.red.Text(reason)})
}

// end records an outcome, even once the request's context has ended.
func (c *call) end(ctx context.Context, o *Outcome) bool {
	if o.At.IsZero() {
		o.At = c.g.p.Now()
	}
	return c.store(ctx, func(ctx context.Context) error { return c.g.p.Calls.End(ctx, o) })
}

// store makes one read or write of the record within RecordTimeout, even
// once the request's context has ended, so that a request whose client went
// is recorded and counted as any other.
func (c *call) store(ctx context.Context, do func(context.Context) error) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.g.opts.RecordTimeout)
	defer cancel()
	return do(ctx) == nil
}

// over reports why the request cannot go on, if it cannot: its run
// stopped, its round ended, or it ended itself.
func (c *call) over(ctx context.Context) error {
	select {
	case <-c.s.Done:
		return errors.New("the run stopped")
	default:
	}
	if !c.s.Deadline.IsZero() && !time.Now().Before(c.s.Deadline) {
		return errors.New("the round ended")
	}
	return ctx.Err()
}

// bounded returns ctx, ended also when the session's round ends or its run
// stops.
func (c *call) bounded(ctx context.Context) (context.Context, context.CancelFunc) {
	cancelDeadline := context.CancelFunc(func() {})
	if !c.s.Deadline.IsZero() {
		ctx, cancelDeadline = context.WithDeadline(ctx, c.s.Deadline)
	}
	ctx, cancel := context.WithCancel(ctx)
	if c.s.Done != nil {
		go func() {
			select {
			case <-c.s.Done:
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	return ctx, func() {
		cancel()
		cancelDeadline()
	}
}

// cut answers a request that could not wait longer: its run stopped, its
// round ended, or it ended itself. what says what came first.
func (c *call) cut(what string) *Reply {
	select {
	case <-c.s.Done:
		return refusal(http.StatusBadGateway, "api_error", "the run stopped "+what, false)
	default:
	}
	if !c.s.Deadline.IsZero() && !time.Now().Before(c.s.Deadline) {
		return refusal(http.StatusGatewayTimeout, "timeout_error", "the round ended "+what, false)
	}
	return refusal(http.StatusBadGateway, "api_error", "the request ended "+what, false)
}

// take gives the request the session's place at the gateway. A call that
// goes on after its request was answered (its round ended, its run stopped
// or its client went) holds the place: the next request waits for it to
// end for at most LastCallWait, and then goes on beside it, once the vendor
// has answered some call since it started; while two go on, it waits until
// one ends. So the vendor never has more than two of a session's calls at
// once, and a vendor that answers no one gets no second call to hang. A
// wait ends when the request, its round or its run does. A request that
// finds another in the gateway's hands, or waiting, is refused: the client
// sent them at once.
func (c *call) take(ctx context.Context) *Reply {
	g, key := c.g, c.key()
	patience := time.Now().Add(g.opts.LastCallWait)
	for {
		g.mu.Lock()
		l := g.lanes[key]
		if l == nil {
			l = &lane{ended: make(chan struct{})}
			g.lanes[key] = l
		}
		if l.held || l.waiting {
			g.mu.Unlock()
			return c.refuse(ctx, http.StatusTooManyRequests, "rate_limit_error", "the session has a model call in flight; it makes one at a time")
		}
		if l.going == 0 || (l.going == 1 && !time.Now().Before(patience) && g.heard.Load() > l.since.UnixNano()) {
			l.held = true
			c.flight = &flight{open: true}
			g.mu.Unlock()
			return nil
		}
		l.waiting = true
		ended, until := l.ended, time.Time{}
		if l.going == 1 {
			until = patience
			if !time.Now().Before(patience) {
				until = time.Now().Add(g.opts.LastCallWait)
			}
		}
		g.mu.Unlock()
		waited := c.await(ctx, ended, until)
		g.mu.Lock()
		l.waiting = false
		g.tidy(key, l)
		g.mu.Unlock()
		if !waited {
			return c.decline(ctx, StatusError, "the session's last call went on for longer than the request could wait", c.cut("while the session's last call went on"))
		}
	}
}

// await waits until ended is closed, or until when it is set, and reports
// false when the request, its round or its run ended first.
func (c *call) await(ctx context.Context, ended <-chan struct{}, until time.Time) bool {
	bounded, cancel := c.bounded(ctx)
	defer cancel()
	var patience <-chan time.Time
	if !until.IsZero() {
		t := time.NewTimer(time.Until(until))
		defer t.Stop()
		patience = t.C
	}
	select {
	case <-ended:
	case <-patience:
	case <-bounded.Done():
		return false
	}
	return true
}

// land ends the request's part in its flight, or its call's: a call that
// goes on after its request was answered holds the session's place until
// it ends.
func (g *Gateway) land(c *call, request bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := c.key()
	l, f := g.lanes[key], c.flight
	if request {
		f.open, l.held = false, false
		if f.calling {
			l.going++
			if f.started.After(l.since) {
				l.since = f.started
			}
		}
	} else {
		f.calling = false
		if !f.open {
			l.going--
			close(l.ended)
			l.ended = make(chan struct{})
		}
	}
	if l.going == 0 {
		l.since = time.Time{}
	}
	g.tidy(key, l)
}

// tidy forgets a lane nothing holds. The caller holds g.mu.
func (g *Gateway) tidy(key sessionKey, l *lane) {
	if !l.held && !l.waiting && l.going == 0 {
		delete(g.lanes, key)
	}
}

// state returns what the gateway keeps of a session, marked as used, and
// moved on to the session's round when that is a later one. Past
// MaxSessions, the session used least recently is forgotten. The caller
// holds g.mu.
func (g *Gateway) state(s *Session) *sessionState {
	key := sessionKey{s.Run, s.Agent}
	g.tick++
	st := g.sessions[key]
	if st == nil {
		if len(g.sessions) >= MaxSessions {
			g.forgetLeastUsed()
		}
		st = &sessionState{round: s.Round, thinking: map[[32]byte]bool{}, calls: map[string]bool{}}
		g.sessions[key] = st
	}
	st.used = g.tick
	if s.Round > st.round {
		st.round, st.refused, st.noticed = s.Round, 0, false
	}
	return st
}

// forgetLeastUsed forgets the session used least recently. The caller
// holds g.mu.
func (g *Gateway) forgetLeastUsed() {
	var oldest sessionKey
	var least uint64
	for key, st := range g.sessions {
		if least == 0 || st.used < least {
			oldest, least = key, st.used
		}
	}
	delete(g.sessions, oldest)
}

// answered returns the check of a thinking block sent back: the gateway
// gave it to the session.
func (g *Gateway) answered(s *Session) func([32]byte) bool {
	return func(h [32]byte) bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.state(s).thinking[h]
	}
}

// called returns the check of a tool call's id sent back: the gateway gave
// it to the session, in an answer.
func (g *Gateway) called(s *Session) func(string) bool {
	return func(id string) bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.state(s).calls[id]
	}
}

// remember keeps the thinking blocks and the ids of the tool calls the
// gateway gave the session; past maxThinking and maxCalls, it forgets the
// oldest.
func (g *Gateway) remember(s *Session, hashes [][32]byte, calls []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.state(s)
	for _, h := range hashes {
		if st.thinking[h] {
			continue
		}
		st.thinking[h] = true
		st.order = append(st.order, h)
		if len(st.order) > maxThinking {
			delete(st.thinking, st.order[0])
			st.order = st.order[1:]
		}
	}
	for _, id := range calls {
		if st.calls[id] {
			continue
		}
		st.calls[id] = true
		st.callOrder = append(st.callOrder, id)
		if len(st.callOrder) > maxCalls {
			delete(st.calls, st.callOrder[0])
			st.callOrder = st.callOrder[1:]
		}
	}
}

// refusalRecord says how a refusal is recorded.
type refusalRecord int

const (
	// recordReason records the refusal with its reason.
	recordReason refusalRecord = iota
	// recordShutOut records it as the session's shut-out.
	recordShutOut
	// unrecorded does not record it.
	unrecorded
)

// reserve counts a refusal against the session's round before it is
// recorded, and says how it is recorded: with its reason while the round
// has refusals left, as the session's shut-out once, and not at all after.
func (g *Gateway) reserve(s *Session) refusalRecord {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.state(s)
	switch {
	case st.refused < g.opts.MaxRefusals:
		st.refused++
		return recordReason
	case !st.noticed:
		st.noticed = true
		return recordShutOut
	}
	return unrecorded
}

// vendorRefused counts the vendor's refusal of a session's request against
// the session's round, as the gateway's own refusals count, unless the
// round has passed since.
func (g *Gateway) vendorRefused(s *Session) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if st := g.state(s); st.round == s.Round {
		st.refused++
	}
}

// shutOut reports a session whose round has no refusals left.
func (g *Gateway) shutOut(s *Session) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.state(s).refused >= g.opts.MaxRefusals
}

// transient reports a failure another attempt may get past: one whose
// request never reached the vendor, or that the vendor says is passing.
func transient(answer *Answer, err error) bool {
	if err != nil {
		return errors.Is(err, ErrNotSent)
	}
	switch answer.Status {
	case http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout, 529:
		return true
	}
	return false
}

// jitter returns d with up to a fifth more, at random, so that sessions
// that failed together do not try again together.
func jitter(d time.Duration) time.Duration {
	return d + time.Duration(rand.Int64N(int64(d)/5+1)) //nolint:gosec // G404: spreading waits needs no secret
}

func classOf(c cost.Class) limits.Class {
	if c == cost.Shadow {
		return limits.Shadow
	}
	return limits.Primary
}

// refusal answers in the Messages API's form of an error. retry says
// whether the client may send the request again.
func refusal(status int, kind, message string, retry bool) *Reply {
	body, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]string{"type": kind, "message": message}})
	return &Reply{Status: status, Header: map[string]string{"Content-Type": "application/json", "X-Should-Retry": strconv.FormatBool(retry)}, Body: body}
}

// always returns the redaction rules that apply always.
func always(rev *config.Revision) *redact.Redactor {
	var rules []redact.Rule
	for _, r := range rev.AlwaysRedactionRules() {
		rules = append(rules, redact.FromConfig(r))
	}
	return redact.New(rules...)
}
