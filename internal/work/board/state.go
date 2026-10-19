package board

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jchen0426/agent-swarm/internal/work/key"
)

// tagFormat is a task tag: a lowercase word, as agents subscribe to them.
var tagFormat = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

// State is a run's board, folded from its committed events in commit order.
type State struct {
	settings  Settings
	events    []Event
	index     map[EntryID]int     // entry id → position in events
	canonical map[EntryID]EntryID // hypothesis id → the hypothesis it merged into, or itself
	byKey     map[string]EntryID  // key string → canonical hypothesis, for mergeable keys
	retracted map[EntryID]EntryID // evidence id → the entry that retracted it
	rejected  map[EntryID]bool    // canonical hypothesis → rejected by a human
	dismissed map[EntryID]bool    // refuting evidence id → dismissed by a human
	lastClaim map[EntryID]EntryID // task → its latest claim
	completed map[EntryID]EntryID // task → the completion that ended it
	round     int                 // position of the latest round entry in events, or -1
	stop      int                 // position of the stop entry in events, or -1
}

// newState returns an empty board. Replay is the only way in from outside the
// package, so every board has checked settings.
func newState(settings Settings) *State {
	return &State{
		settings:  settings,
		index:     make(map[EntryID]int),
		canonical: make(map[EntryID]EntryID),
		byKey:     make(map[string]EntryID),
		retracted: make(map[EntryID]EntryID),
		rejected:  make(map[EntryID]bool),
		dismissed: make(map[EntryID]bool),
		lastClaim: make(map[EntryID]EntryID),
		completed: make(map[EntryID]EntryID),
		round:     -1,
		stop:      -1,
	}
}

// Replay folds committed events into a State. It checks the run's convergence
// settings, and that the events are in commit order, uniquely identified and
// consistent with the board rules. It trusts each evidence entry's provenance,
// which was checked when it was posted. Replaying no events gives an empty board.
func Replay(settings Settings, events []Event) (*State, error) {
	if err := settings.Convergence.check(); err != nil {
		return nil, err
	}
	if err := settings.Limits.check(); err != nil {
		return nil, err
	}
	s := newState(settings)
	for i := range events {
		if err := s.Apply(&events[i]); err != nil {
			return nil, fmt.Errorf("replay entry %d: %w", events[i].Seq, err)
		}
	}
	return s, nil
}

// Apply adds one committed event to the board, after checking it against the
// board rules and the commit order.
func (s *State) Apply(e *Event) error {
	if err := s.checkNewID(e.ID); err != nil {
		return err
	}
	if n := len(s.events); n > 0 {
		last := &s.events[n-1]
		if e.Seq <= last.Seq || e.At.Before(last.At) {
			return fmt.Errorf("%w: entry %s is out of commit order", ErrInvalid, e.ID)
		}
	}
	if err := s.check(e.Author, &e.Body); err != nil {
		return err
	}
	s.record(e)
	return nil
}

// Event returns the committed entry with the given id.
func (s *State) Event(id EntryID) (Event, bool) {
	e, ok := s.event(id)
	if !ok {
		return Event{}, false
	}
	return *e, true
}

// RetractionOf returns the entry that retracted the given evidence, if any.
func (s *State) RetractionOf(evidence EntryID) (EntryID, bool) {
	id, ok := s.retracted[evidence]
	return id, ok
}

func (s *State) checkNewID(id EntryID) error {
	if id == "" {
		return fmt.Errorf("%w: entry without an id", ErrInvalid)
	}
	if _, dup := s.index[id]; dup {
		return fmt.Errorf("%w: duplicate entry id %s", ErrInvalid, id)
	}
	return nil
}

// check applies the rules that every entry satisfies, both when it is posted
// and when it is replayed. A stopped board takes rulings only.
func (s *State) check(author Author, b *Body) error {
	if author.Name == "" {
		return fmt.Errorf("%w: entry without an author", ErrInvalid)
	}
	if s.stop >= 0 && b.Kind() != KindRuling {
		return fmt.Errorf("%w: its board takes rulings only", ErrStopped)
	}
	return s.checkBody(author, b)
}

// checkBody applies the rules of the entry's kind.
func (s *State) checkBody(author Author, b *Body) error {
	switch b.Kind() {
	case KindEvidence:
		return s.checkEvidence(author, b.Evidence)
	case KindHypothesis:
		return s.checkHypothesis(author, b.Hypothesis)
	case KindRetraction:
		return s.checkRetraction(author, b.Retraction)
	case KindRuling:
		return s.checkRuling(author, b.Ruling)
	case KindTask:
		return checkTask(author, b.Task)
	case KindClaim:
		return s.checkClaim(author, b.Claim)
	case KindCompletion:
		return s.checkCompletion(author, b.Completion)
	case KindTurnEnd:
		return agentsOnly(author, "end turns")
	case KindProposal:
		return s.checkProposal(author, b.Proposal)
	case KindNotice:
		return checkNotice(author, b.Notice)
	case KindRound:
		return s.checkRound(author, b.Round)
	case KindStop:
		return s.checkStop(author, b.Stop)
	default:
		return fmt.Errorf("%w: an entry holds exactly one body", ErrInvalid)
	}
}

func agentsOnly(author Author, what string) error {
	if author.Kind != AuthorAgent {
		return fmt.Errorf("%w: only agents %s", ErrNotAllowed, what)
	}
	return nil
}

func checkTask(author Author, t *Task) error {
	if err := agentsOnly(author, "post tasks"); err != nil {
		return err
	}
	if err := checkText("title", t.Title, MaxTitleRunes); err != nil {
		return err
	}
	if len(t.Tags) > MaxTags {
		return fmt.Errorf("%w: a task has at most %d tags", ErrInvalid, MaxTags)
	}
	for i, tag := range t.Tags {
		if !tagFormat.MatchString(tag) || slices.Index(t.Tags, tag) != i {
			return fmt.Errorf("%w: tag %q must be a lowercase word, each tag once", ErrInvalid, tag)
		}
	}
	return nil
}

func (s *State) checkClaim(author Author, c *Claim) error {
	if err := agentsOnly(author, "claim tasks"); err != nil {
		return err
	}
	_, err := s.task(c.Task)
	return err
}

func (s *State) checkCompletion(author Author, c *Completion) error {
	if err := agentsOnly(author, "complete tasks"); err != nil {
		return err
	}
	if _, err := s.task(c.Task); err != nil {
		return err
	}
	if _, done := s.completed[c.Task]; done {
		return fmt.Errorf("%w: task %s is already completed", ErrInvalid, c.Task)
	}
	if len(c.Evidence) == 0 || len(c.Evidence) > MaxCitations {
		return fmt.Errorf("%w: a completion cites 1 to %d evidence entries", ErrInvalid, MaxCitations)
	}
	for i, id := range c.Evidence {
		if slices.Index(c.Evidence, id) != i {
			return fmt.Errorf("%w: evidence %s is cited twice", ErrInvalid, id)
		}
		if _, err := s.liveEvidence(id); err != nil {
			return err
		}
	}
	return nil
}

func (s *State) checkProposal(author Author, p *Proposal) error {
	switch {
	case author.Kind != AuthorPlatform:
		return fmt.Errorf("%w: the gateway records proposals", ErrNotAllowed)
	case p.Proposer == "" || p.Tool == "" || p.SchemaHash == "" || p.ArgsHash == "" || p.CallID == "":
		return fmt.Errorf("%w: a proposal names its proposer, its tool and the tool's hash, its arguments' hash and its call", ErrInvalid)
	}
	if err := checkText("summary", SummaryText(p.Summary), MaxSummaryRunes); err != nil {
		return err
	}
	for name, f := range p.Facts {
		if len(f.Values) > MaxFactValues {
			return fmt.Errorf("%w: fact %s keeps more than %d values", ErrInvalid, name, MaxFactValues)
		}
		for _, v := range f.Values {
			if utf8.RuneCountInString(v) > MaxValueRunes || !json.Valid([]byte(v)) {
				return fmt.Errorf("%w: fact %s holds a value that is not JSON of at most %d characters", ErrInvalid, name, MaxValueRunes)
			}
		}
	}
	_, err := s.canonicalOf(p.Hypothesis)
	return err
}

// checkRound checks a round entry: the platform's, opening the round after
// the board's latest, and naming a hypothesis when it is a wrap-up round.
func (s *State) checkRound(author Author, r *RoundStart) error {
	if author.Kind != AuthorPlatform {
		return fmt.Errorf("%w: only the coordinator starts rounds", ErrNotAllowed)
	}
	last := 0
	if s.round >= 0 {
		last = s.events[s.round].Body.Round.Round
	}
	if r.Round != last+1 {
		return fmt.Errorf("%w: round %d cannot follow round %d", ErrInvalid, r.Round, last)
	}
	if r.WrapUp != "" {
		if _, err := s.canonicalOf(r.WrapUp); err != nil {
			return err
		}
	}
	return nil
}

// stopReason is a stop reason's code.
var stopReason = regexp.MustCompile(`^[a-z][a-z_]{0,31}$`)

// checkStop checks a stop entry: the coordinator's, with its reason as a
// code.
func (s *State) checkStop(author Author, st *Stop) error {
	switch {
	case author.Kind != AuthorPlatform:
		return fmt.Errorf("%w: only the coordinator stops a run", ErrNotAllowed)
	case !stopReason.MatchString(st.Reason):
		return fmt.Errorf("%w: a stop reason is a code of lowercase letters and underscores", ErrInvalid)
	case st.Conclusion != "":
		if e, ok := s.event(st.Conclusion); !ok || e.Body.Hypothesis == nil {
			return fmt.Errorf("%w: the conclusion %s is no hypothesis of the board", ErrInvalid, st.Conclusion)
		}
	}
	return nil
}

func checkNotice(author Author, n *Notice) error {
	switch {
	case author.Kind != AuthorPlatform:
		return fmt.Errorf("%w: only the platform posts notices", ErrNotAllowed)
	case n.Code == NoticeNoAgents:
		if *n != (Notice{Code: NoticeNoAgents}) {
			return fmt.Errorf("%w: a no_agents notice names nothing", ErrInvalid)
		}
		return nil
	case n.Code != NoticeRefusedCall && n.Code != NoticeFailedRead && !modelNotice(n.Code):
		return fmt.Errorf("%w: unknown notice %q", ErrInvalid, n.Code)
	case n.Agent == "":
		return fmt.Errorf("%w: a notice names its agent", ErrInvalid)
	case modelNotice(n.Code) && n.CallID == "":
		return fmt.Errorf("%w: a notice of a model call names the call", ErrInvalid)
	}
	return nil
}

func (s *State) checkEvidence(author Author, ev *Evidence) error {
	if err := checkText("claim", ev.Claim, MaxClaimRunes); err != nil {
		return err
	}
	if err := checkProvenance(author, &ev.Provenance); err != nil {
		return err
	}
	if (ev.Hypothesis == "") != (ev.Stance == "") {
		return fmt.Errorf("%w: a stance goes with a hypothesis, and a hypothesis with a stance", ErrInvalid)
	}
	if ev.Hypothesis != "" {
		if !author.IsContributor() {
			return fmt.Errorf("%w: the platform takes no stance", ErrNotAllowed)
		}
		if ev.Stance != Supports && ev.Stance != Refutes {
			return fmt.Errorf("%w: unknown stance %q", ErrInvalid, ev.Stance)
		}
		if _, err := s.canonicalOf(ev.Hypothesis); err != nil {
			return err
		}
	}
	if len(ev.Dismisses) > 0 {
		if author.Kind != AuthorHuman || ev.Stance != Supports {
			return fmt.Errorf("%w: only a human's support dismisses refutations", ErrInvalid)
		}
		for _, id := range ev.Dismisses {
			if _, err := s.refutation(id); err != nil {
				return err
			}
		}
		return checkReason(ev.Reason, true)
	}
	return checkReason(ev.Reason, false)
}

func checkProvenance(author Author, p *Provenance) error {
	switch author.Kind {
	case AuthorAgent:
		if p.Kind != FromReadCall || p.CallID == "" || p.Source == "" {
			return fmt.Errorf("%w: an agent's evidence cites one of its read calls", ErrProvenance)
		}
		return checkText("quote", p.Quote, MaxQuoteRunes)
	case AuthorHuman:
		if p.Kind != FromHuman || p.Source != HumanObservation {
			return fmt.Errorf("%w: a human's evidence is human observation", ErrProvenance)
		}
	case AuthorPlatform:
		if p.Kind != FromPlatform || p.Ref == "" || p.Source == "" {
			return fmt.Errorf("%w: the platform's evidence names its record", ErrProvenance)
		}
	default:
		return fmt.Errorf("%w: unknown author kind %q", ErrInvalid, author.Kind)
	}
	return nil
}

func (s *State) checkHypothesis(author Author, h *Hypothesis) error {
	if author.Kind != AuthorAgent {
		return fmt.Errorf("%w: only agents post hypotheses", ErrNotAllowed)
	}
	if err := checkText("statement", h.Statement, MaxStatementRunes); err != nil {
		return err
	}
	if err := s.settings.Schema.Validate(h.Key); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if len(h.Evidence) == 0 || len(h.Evidence) > MaxCitations {
		return fmt.Errorf("%w: a hypothesis cites 1 to %d evidence entries", ErrInvalid, MaxCitations)
	}
	seen := make(map[EntryID]bool, len(h.Evidence))
	for _, id := range h.Evidence {
		if seen[id] {
			return fmt.Errorf("%w: evidence %s is cited twice", ErrInvalid, id)
		}
		seen[id] = true
		if _, err := s.liveEvidence(id); err != nil {
			return err
		}
	}
	target, merges := s.mergeTarget(h.Key)
	switch {
	case h.AliasOf == target:
		return nil
	case merges:
		return fmt.Errorf("%w: hypothesis %s has key %s, so this one must merge into it", ErrInvalid, target, h.Key)
	default:
		return fmt.Errorf("%w: %s is not a hypothesis with an equal, mergeable key", ErrInvalid, h.AliasOf)
	}
}

func (s *State) checkRetraction(author Author, r *Retraction) error {
	if author.Kind != AuthorAgent {
		return fmt.Errorf("%w: only agents retract evidence", ErrNotAllowed)
	}
	e, err := s.evidence(r.Evidence)
	if err != nil {
		return err
	}
	if e.Author != author {
		return fmt.Errorf("%w: an agent retracts only its own evidence", ErrNotAllowed)
	}
	if _, done := s.retracted[r.Evidence]; done {
		return fmt.Errorf("%w: evidence %s is already retracted", ErrInvalid, r.Evidence)
	}
	return nil
}

func (s *State) checkRuling(author Author, r *Ruling) error {
	if author.Kind != AuthorHuman {
		return fmt.Errorf("%w: only humans issue rulings", ErrNotAllowed)
	}
	switch r.Kind {
	case RulingReject, RulingUnreject:
		if r.Refutation != "" {
			return fmt.Errorf("%w: a %s ruling names a hypothesis only", ErrInvalid, r.Kind)
		}
		h, err := s.canonicalOf(r.Hypothesis)
		if err != nil {
			return err
		}
		if r.Kind == RulingReject {
			if s.rejected[h] {
				return fmt.Errorf("%w: hypothesis %s is already rejected", ErrInvalid, h)
			}
			return checkReason(r.Reason, true)
		}
		if !s.rejected[h] {
			return fmt.Errorf("%w: hypothesis %s is not rejected", ErrInvalid, h)
		}
		return checkReason(r.Reason, false)
	case RulingDismiss:
		if r.Hypothesis != "" {
			return fmt.Errorf("%w: a dismissal names a refutation only", ErrInvalid)
		}
		if _, err := s.refutation(r.Refutation); err != nil {
			return err
		}
		if s.dismissed[r.Refutation] {
			return fmt.Errorf("%w: refutation %s is already dismissed", ErrInvalid, r.Refutation)
		}
		return checkReason(r.Reason, true)
	default:
		return fmt.Errorf("%w: unknown ruling %q", ErrInvalid, r.Kind)
	}
}

func checkText(name, value string, maxRunes int) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s is empty", ErrInvalid, name)
	}
	if utf8.RuneCountInString(value) > maxRunes {
		return fmt.Errorf("%w: %s is longer than %d characters", ErrInvalid, name, maxRunes)
	}
	return nil
}

func checkReason(r *Reason, required bool) error {
	if r == nil || r.Code == "" {
		if r != nil && r.Text != "" {
			return fmt.Errorf("%w: a reason's text needs a preset code", ErrInvalid)
		}
		if required {
			return ErrReasonRequired
		}
		return nil
	}
	if utf8.RuneCountInString(r.Text) > MaxReasonRunes {
		return fmt.Errorf("%w: a reason's text is longer than %d characters", ErrInvalid, MaxReasonRunes)
	}
	return nil
}

func (s *State) record(e *Event) {
	s.index[e.ID] = len(s.events)
	s.events = append(s.events, *e)
	b := &e.Body
	switch {
	case b.Evidence != nil:
		for _, id := range b.Evidence.Dismisses {
			s.dismissed[id] = true
		}
	case b.Hypothesis != nil:
		canon := e.ID
		if b.Hypothesis.AliasOf != "" {
			canon = b.Hypothesis.AliasOf
		}
		s.canonical[e.ID] = canon
		if canon == e.ID && s.settings.Schema.Mergeable(b.Hypothesis.Key) {
			s.byKey[b.Hypothesis.Key.String()] = e.ID
		}
	case b.Retraction != nil:
		s.retracted[b.Retraction.Evidence] = e.ID
	case b.Claim != nil:
		s.lastClaim[b.Claim.Task] = e.ID
	case b.Completion != nil:
		s.completed[b.Completion.Task] = e.ID
	case b.Round != nil:
		s.round = len(s.events) - 1
	case b.Stop != nil:
		s.stop = len(s.events) - 1
	case b.Ruling != nil:
		s.recordRuling(b.Ruling)
	}
}

func (s *State) recordRuling(r *Ruling) {
	switch r.Kind {
	case RulingReject:
		s.rejected[s.canonical[r.Hypothesis]] = true
	case RulingUnreject:
		delete(s.rejected, s.canonical[r.Hypothesis])
	case RulingDismiss:
		s.dismissed[r.Refutation] = true
	}
}

func (s *State) event(id EntryID) (*Event, bool) {
	i, ok := s.index[id]
	if !ok {
		return nil, false
	}
	return &s.events[i], true
}

func (s *State) evidence(id EntryID) (*Event, error) {
	e, ok := s.event(id)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if e.Body.Evidence == nil {
		return nil, fmt.Errorf("%w: %s is not evidence", ErrInvalid, id)
	}
	return e, nil
}

// task returns a task entry.
func (s *State) task(id EntryID) (*Event, error) {
	e, ok := s.event(id)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if e.Body.Task == nil {
		return nil, fmt.Errorf("%w: %s is not a task", ErrInvalid, id)
	}
	return e, nil
}

// liveEvidence returns evidence that has not been retracted.
func (s *State) liveEvidence(id EntryID) (*Event, error) {
	e, err := s.evidence(id)
	if err != nil {
		return nil, err
	}
	if _, gone := s.retracted[id]; gone {
		return nil, fmt.Errorf("%w: evidence %s was retracted", ErrInvalid, id)
	}
	return e, nil
}

// refutation returns live evidence that refutes a hypothesis.
func (s *State) refutation(id EntryID) (*Event, error) {
	e, err := s.liveEvidence(id)
	if err != nil {
		return nil, err
	}
	if e.Body.Evidence.Stance != Refutes {
		return nil, fmt.Errorf("%w: %s is not a refutation", ErrInvalid, id)
	}
	return e, nil
}

// canonicalOf resolves a hypothesis id, which may be an alias, to the
// hypothesis it merged into.
func (s *State) canonicalOf(id EntryID) (EntryID, error) {
	if canon, ok := s.canonical[id]; ok {
		return canon, nil
	}
	if _, exists := s.index[id]; exists {
		return "", fmt.Errorf("%w: %s is not a hypothesis", ErrInvalid, id)
	}
	return "", fmt.Errorf("%w: %s", ErrNotFound, id)
}

// mergeTarget returns the hypothesis that a new hypothesis with key k merges
// into, if any.
func (s *State) mergeTarget(k key.Key) (EntryID, bool) {
	if !s.settings.Schema.Mergeable(k) {
		return "", false
	}
	id, ok := s.byKey[k.String()]
	return id, ok
}
