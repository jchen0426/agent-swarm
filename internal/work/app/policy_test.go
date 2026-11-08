package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/admin/switching"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// The roles of the people who act, as the web layer builds their actors
// (R76): a viewer; a responder; an admin while fresh, who is a responder
// too; an admin who is not fresh, a responder alone; and the loopback
// token's admin.
var (
	viewer        = app.Actor{Subject: "sso:dave"}
	responder     = app.Actor{Subject: "sso:dave", Responder: true}
	freshAdmin    = app.Actor{Subject: "sso:dave", Responder: true, Admin: true}
	notFreshAdmin = app.Actor{Subject: "sso:dave", Responder: true}
)

// owned is an incident opened by the loopback admin for the principal
// sso:carol, with the responder sso:erin.
func owned(h *harness) item.ID {
	h.t.Helper()
	id, err := h.c.Open(h.t.Context(), &app.OpenInput{
		Actor: administrator, Type: "incident", Title: "checkout fails", Source: "person",
		Routing: map[string]string{"service": "checkout"}, Principal: "sso:carol", Responders: []string{"sso:erin"},
	})
	require.NoError(h.t, err)
	return id
}

// as is an act by actor under key, or its preview when key is empty.
func as(actor app.Actor, key string) *app.Act {
	return &app.Act{Actor: actor, Key: key, Preview: key == ""}
}

// openingAs is a person's opening of an incident by actor, for principal and
// responders.
func openingAs(actor app.Actor, principal string, responders ...string) *app.OpenInput {
	return &app.OpenInput{
		Actor: actor, Type: "incident", Title: "checkout fails", Source: "person",
		Routing: map[string]string{"service": "checkout"}, Principal: principal, Responders: responders,
	}
}

// May decides every act for every role (R76, R77; design D5): the kill
// switch's on and an opening need a responder or an admin, and a viewer's is
// refused responder_only; the off needs an admin, admin_only; an act on an
// item is a member's, whatever the member's role, as its principal or one of
// its responders, and otherwise needs Admin, an admin override, and is
// refused not_item_responder without it. An admin who is not fresh is a
// responder alone.
func TestMayDecidesEveryActForEveryRole(t *testing.T) {
	o := &item.Opened{Principal: "sso:p", Responders: []string{"sso:r"}}
	roles := map[string]app.Actor{
		"a viewer":                  {},
		"a responder":               {Responder: true},
		"a fresh admin":             {Responder: true, Admin: true},
		"an admin who is not fresh": {Responder: true},
		"the loopback admin":        {Admin: true},
	}
	relations := map[string]string{"its principal": "sso:p", "its responder": "sso:r", "a stranger": "sso:s"}
	for role, base := range roles {
		for relation, subject := range relations {
			a := base
			a.Subject = subject
			member := relation != "a stranger"
			for _, k := range []app.ActKind{app.ActOpen, app.ActPromote, app.ActClose, app.ActReopen, app.ActExtend, app.ActSwitchOn, app.ActSwitchOff} {
				name := fmt.Sprintf("%s, %s, %s", role, relation, k)
				var want app.Grounds
				code := ""
				switch k {
				case app.ActOpen, app.ActSwitchOn:
					if a.Responder || a.Admin {
						want = app.Unrestricted
					} else {
						code = app.CodeResponderOnly
					}
				case app.ActSwitchOff:
					if a.Admin {
						want = app.Unrestricted
					} else {
						code = app.CodeAdminOnly
					}
				default:
					switch {
					case member:
						want = app.Member
					case a.Admin:
						want = app.AdminOverride
					default:
						code = app.CodeNotItemResponder
					}
				}
				on := o
				if k == app.ActOpen {
					on = nil
				}

				got, err := app.May(a, k, on)

				if code != "" {
					assert.Equal(t, code, refusal(err), name)
					assert.Zero(t, got, "%s: no grounds", name)
					continue
				}
				require.NoError(t, err, name)
				assert.Equal(t, want, got, name)
			}
		}
	}

	_, err := app.May(freshAdmin, app.ActClose, nil)
	require.ErrorIs(t, err, app.ErrInvalid, "an act on an item names its item")
	_, err = app.May(freshAdmin, app.ActKind("delete"), o)
	require.ErrorIs(t, err, app.ErrInvalid, "an act of no kind")
	_, err = app.May(app.Actor{}, app.ActClose, &item.Opened{})
	assert.Equal(t, app.CodeNotItemResponder, refusal(err), "no subject is the principal of an item that names none")
}

// The actor the web builds for an admin who is not fresh, a responder alone
// (R76; I1), turns the kill switch on and opens an item for themselves, with
// no fresh authentication.
func TestAResponderActorOpensForThemselvesAndTurnsTheSwitchOn(t *testing.T) {
	h := newHarness(t)
	reason := incident

	out, err := h.c.SetKillSwitch(t.Context(), true, &app.Act{Actor: notFreshAdmin, Reason: &reason})

	require.NoError(t, err)
	assert.True(t, out.On)
	assert.Equal(t, int64(1), h.controls.rows(t))

	id, err := h.c.Open(t.Context(), openingAs(notFreshAdmin, notFreshAdmin.Subject))

	require.NoError(t, err)
	assert.Equal(t, notFreshAdmin.Subject, h.state(id).Opened.Principal)
}

// A viewer's on is refused, responder_only, before anything is written or
// previewed, its preview included (R79): no row, no consequences, and the
// switch stays off.
func TestAViewersOnIsRefusedResponderOnly(t *testing.T) {
	h := newHarness(t)
	h.open("checkout fails")
	reason := incident
	for name, a := range map[string]*app.Act{
		"an on":       {Actor: viewer, Reason: &reason},
		"its preview": {Actor: viewer, Preview: true},
	} {
		out, err := h.c.SetKillSwitch(t.Context(), true, a)

		assert.Equal(t, app.CodeResponderOnly, refusal(err), name)
		assert.Equal(t, app.KillSwitchOutcome{}, out, "%s: no answer, and no consequences", name)
	}
	assert.Zero(t, h.controls.rows(t), "no row")
	assert.False(t, h.c.Lifecycle().KillSwitch.On, "the switch stays off")
}

// A viewer's on during a drain, through RecordKillSwitchOn, is refused
// responder_only and writes no row (R79; N4).
func TestAViewersOnDuringADrainIsRefusedAndWritesNoRow(t *testing.T) {
	h := newHarness(t)
	h.open("checkout fails")
	h.c.Fence()
	reason := incident

	out, err := h.c.RecordKillSwitchOn(t.Context(), h.epoch, &app.Act{Actor: viewer, Reason: &reason})

	assert.Equal(t, app.CodeResponderOnly, refusal(err))
	assert.Equal(t, app.KillSwitchOutcome{}, out)
	assert.Empty(t, h.controls.writes.noted(), "no write")
	assert.Zero(t, h.controls.rows(t), "no row")
}

// drainingLeader is a leader that holds the lease and drains, under epoch.
type drainingLeader struct{ epoch int64 }

func (l drainingLeader) Status() (epoch int64, held, draining bool) { return l.epoch, true, true }

// switchCalls is a coordinator that notes its calls to the kill switch.
type switchCalls struct {
	c     *app.Coordinator
	calls []string
}

func (s *switchCalls) SetKillSwitch(ctx context.Context, on bool, a *app.Act) (app.KillSwitchOutcome, error) {
	s.calls = append(s.calls, "SetKillSwitch")
	return s.c.SetKillSwitch(ctx, on, a)
}

func (s *switchCalls) RecordKillSwitchOn(ctx context.Context, epoch int64, a *app.Act) (app.KillSwitchOutcome, error) {
	s.calls = append(s.calls, "RecordKillSwitchOn")
	return s.c.RecordKillSwitchOn(ctx, epoch, a)
}

// A viewer's on during a drain, sent through the shared routing as either
// front end sends it, reaches RecordKillSwitchOn, which refuses it
// responder_only, 409, and writes no row (R79, R81; n12): the routing's
// drain path checks the role as the coordinator's on does. It sits in app's
// tests, importing switching, for the harness's real coordinator: a fake one
// would test only itself.
func TestAViewersOnThroughRecordOnIsRefused(t *testing.T) {
	h := newHarness(t)
	h.open("checkout fails")
	h.c.Fence()
	reason := incident
	c := &switchCalls{c: h.c}

	got := switching.Set(t.Context(), switching.Ports{Coordinator: c, Leader: drainingLeader{epoch: h.epoch}}, true, &app.Act{Actor: viewer, Reason: &reason})

	require.NotNil(t, got.Refusal)
	assert.Equal(t, app.CodeResponderOnly, got.Refusal.Code)
	assert.Equal(t, http.StatusConflict, got.Refusal.Status)
	assert.Nil(t, got.Switched)
	assert.Equal(t, []string{"SetKillSwitch", "RecordKillSwitchOn"}, c.calls, "through the drain's path")
	assert.Empty(t, h.controls.writes.noted(), "no write")
	assert.Zero(t, h.controls.rows(t), "no row")
}

// A responder who is neither an item's principal nor one of its responders
// cannot promote, close or reopen it, nor preview any of them: each is
// refused not_item_responder and records nothing (R76; m11). Its responder,
// a member, closes it.
func TestAStrangerResponderCannotPromoteCloseOrReopen(t *testing.T) {
	h := newHarness(t)
	id := owned(h)
	acts := map[string]func(*app.Act) (app.Outcome, error){
		"a promotion": func(a *app.Act) (app.Outcome, error) { return h.c.Promote(t.Context(), id, a) },
		"a close":     func(a *app.Act) (app.Outcome, error) { return h.c.Close(t.Context(), id, a) },
		"a reopen":    func(a *app.Act) (app.Outcome, error) { return h.c.Reopen(t.Context(), id, a) },
	}
	refusedAll := func(stage string) {
		kinds := h.kinds(id)
		for name, act := range acts {
			for _, key := range []string{"stranger-1", ""} {
				what := fmt.Sprintf("%s, %s (key %q)", stage, name, key)

				out, err := act(as(responder, key))

				assert.Equal(t, app.CodeNotItemResponder, refusal(err), what)
				assert.Equal(t, app.Outcome{}, out, what)
				assert.Equal(t, kinds, h.kinds(id), "%s records nothing", what)
			}
		}
	}

	refusedAll("open")
	_, err := h.c.Close(t.Context(), id, as(app.Actor{Subject: "sso:erin", Responder: true}, "close-1"))
	require.NoError(t, err, "its responder closes it")
	require.Equal(t, item.KindClosed, lastKind(h, id))
	refusedAll("closed")
}

// A responder who is neither an item's principal nor one of its responders
// cannot extend its run, nor preview it (D9, D17): refused
// not_item_responder, recording nothing, while it runs and once it is
// closed, where the role's refusal comes before the state's (D23), so that a
// stranger is never told the item is closed. Its responder extends it.
func TestAStrangerResponderCannotExtend(t *testing.T) {
	h := newHarness(t)
	id := owned(h)
	refused := func(stage string) {
		kinds := h.kinds(id)
		for _, key := range []string{"stranger-1", ""} {
			what := fmt.Sprintf("%s (key %q)", stage, key)

			out, err := h.c.Extend(t.Context(), id, tokens(1_000), as(responder, key))

			assert.Equal(t, app.CodeNotItemResponder, refusal(err), what)
			assert.Equal(t, app.Outcome{}, out, what)
			assert.Equal(t, kinds, h.kinds(id), "%s records nothing", what)
		}
	}

	refused("running")
	erin := app.Actor{Subject: "sso:erin", Responder: true}
	out, err := h.c.Extend(t.Context(), id, tokens(1_000), as(erin, "extend-1"))
	require.NoError(t, err, "its responder extends it")
	require.True(t, out.Changed)
	assert.Equal(t, erin.Subject, lastRecord(h, id).Body.Extended.By, "the record keeps who extended it")
	_, err = h.c.Close(t.Context(), id, as(erin, "close-1"))
	require.NoError(t, err)
	require.Equal(t, item.KindClosed, lastKind(h, id))
	refused("closed")
}

// An admin's act on an item where they are neither principal nor responder
// needs Admin, which the web gives only while fresh (R76; N3): with it, the
// act goes on, on the grounds of an admin override; without it, the act is
// refused not_item_responder and records nothing.
func TestAnAdminsItemActNeedsAdmin(t *testing.T) {
	h := newHarness(t)
	id := owned(h)
	kinds := h.kinds(id)

	_, err := h.c.Close(t.Context(), id, as(notFreshAdmin, "close-1"))

	assert.Equal(t, app.CodeNotItemResponder, refusal(err))
	assert.Equal(t, kinds, h.kinds(id), "nothing recorded")

	grounds, err := app.May(freshAdmin, app.ActClose, h.state(id).Opened)
	require.NoError(t, err)
	assert.Equal(t, app.AdminOverride, grounds)
	out, err := h.c.Close(t.Context(), id, as(freshAdmin, "close-2"))

	require.NoError(t, err)
	assert.True(t, out.Changed)
	assert.Equal(t, item.KindClosed, lastKind(h, id))
	assert.Equal(t, freshAdmin.Subject, h.state(id).Closed.By)
}

// The loopback admin acts as before (R-d): it opens an item that names no
// principal, closes and reopens it, and turns the kill switch on and off.
// A guard.
func TestTheLoopbackAdminActsAsBefore(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")

	_, err := h.c.Close(t.Context(), id, closing("close-1"))
	require.NoError(t, err)
	_, err = h.c.Reopen(t.Context(), id, reopening("reopen-1"))
	require.NoError(t, err)
	_, err = h.c.SetKillSwitch(t.Context(), true, killing(0))
	require.NoError(t, err)
	out, err := h.c.SetKillSwitch(t.Context(), false, turningOff(1))
	require.NoError(t, err)
	assert.False(t, out.On)
	for _, k := range []app.ActKind{app.ActPromote, app.ActClose, app.ActReopen, app.ActExtend} {
		grounds, err := app.May(administrator, k, h.state(id).Opened)
		require.NoError(t, err, k)
		assert.Equal(t, app.AdminOverride, grounds, k)
	}
}

// An opening records who opened it, the actor's subject (R78); a body of an
// older build, which holds no by, folds as before.
func TestOpenRecordsWhoOpened(t *testing.T) {
	h := newHarness(t)

	id, err := h.c.Open(t.Context(), openingAs(responder, responder.Subject))

	require.NoError(t, err)
	assert.Equal(t, responder.Subject, h.state(id).Opened.By)

	var b item.Body
	require.NoError(t, json.Unmarshal([]byte(`{"v":1,"opened":{"type":"incident","title":"t","source":"person","arm":"visible","how":"no_window"}}`), &b))
	s, err := item.Fold([]item.Record{{Seq: 1, ID: "r1", Item: "i1", Body: b}})
	require.NoError(t, err)
	assert.Empty(t, s.Opened.By)
	assert.Equal(t, "incident", s.Opened.Type)
}

// A responder opens an item only as its principal (R78): naming another
// principal, or none, is refused, and so is an opening whose actor names no
// subject, which its record keeps as its by; nothing is recorded. An admin
// names another.
func TestAResponderOpensOnlyAsTheirOwnPrincipal(t *testing.T) {
	h := newHarness(t)
	for name, principal := range map[string]string{"another principal": "sso:bob", "none": ""} {
		_, err := h.c.Open(t.Context(), openingAs(responder, principal))

		require.ErrorIs(t, err, app.ErrInvalid, name)
	}
	_, err := h.c.Open(t.Context(), openingAs(app.Actor{Responder: true}, ""))
	require.ErrorIs(t, err, app.ErrInvalid, "an actor who names no subject is no principal")
	requireNothingRecorded(h)

	_, err = h.c.Open(t.Context(), openingAs(freshAdmin, "sso:bob"))

	require.NoError(t, err, "an admin names another principal")
}

// An actor with no role learns nothing from an opening under a key that
// opened an item (m10): the same request, and another, are each refused
// responder_only, as under a key that opened none.
func TestAnActorWithNoRoleLearnsNothingFromARepeatedKey(t *testing.T) {
	h := newHarness(t)
	id := item.ID("item-retry-1")
	first := byKey(id, "sha256:one", "checkout fails")
	_, err := h.c.Open(t.Context(), first)
	require.NoError(t, err)

	for name, in := range map[string]*app.OpenInput{
		"the same request":       byKey(id, "sha256:one", "checkout fails"),
		"another request":        byKey(id, "sha256:two", "checkout fails"),
		"a key that opened none": byKey("item-retry-2", "sha256:one", "checkout fails"),
	} {
		in.Actor = viewer

		got, err := h.c.Open(t.Context(), in)

		assert.Equal(t, app.CodeResponderOnly, refusal(err), name)
		assert.Empty(t, got, name)
	}
}

// refusedOpenings opens, as the loopback admin, each opening of cases, and
// checks that each is refused ErrInvalid without quoting the value it names,
// and that nothing is recorded.
func refusedOpenings(t *testing.T, h *harness, cases map[string]*app.OpenInput, quoted func(*app.OpenInput) []string) {
	t.Helper()
	for name, in := range cases {
		_, err := h.c.Open(t.Context(), in)

		require.ErrorIs(t, err, app.ErrInvalid, name)
		for _, v := range quoted(in) {
			if v = strings.TrimSpace(v); v != "" {
				assert.NotContains(t, err.Error(), v, "%s: the refusal never quotes the value", name)
			}
		}
	}
	requireNothingRecorded(h)
}

// people are the values an opening names: its principal and its responders.
func people(in *app.OpenInput) []string { return append([]string{in.Principal}, in.Responders...) }

// A principal or a responder that holds a character a reader could miss is
// refused (R80): it would show as another line on the item's views. One
// whose only such characters lead or trail it is trimmed, and opens.
func TestOpenRefusesAPrincipalOrResponderWithUnseenCharacters(t *testing.T) {
	h := newHarness(t)
	refusedOpenings(t, h, map[string]*app.OpenInput{
		"a principal": openingAs(administrator, "sso:car\u200bol"),
		"a responder": openingAs(administrator, "sso:carol", "sso:erin", "sso:e\trin"),
	}, people)

	id, err := h.c.Open(t.Context(), openingAs(administrator, " sso:carol\n", "\tsso:erin "))

	require.NoError(t, err)
	assert.Equal(t, "sso:carol", h.state(id).Opened.Principal)
	assert.Equal(t, []string{"sso:erin"}, h.state(id).Opened.Responders)
}

// A principal or a responder holds 1 to 256 characters once trimmed (R80):
// one that is empty when present, or longer, is refused.
func TestOpenRefusesAPrincipalOrResponderOverLong(t *testing.T) {
	h := newHarness(t)
	long := "sso:" + strings.Repeat("é", 253)
	refusedOpenings(t, h, map[string]*app.OpenInput{
		"a long principal":      openingAs(administrator, long),
		"a long responder":      openingAs(administrator, "sso:carol", long),
		"a principal of spaces": openingAs(administrator, "   "),
		"an empty responder":    openingAs(administrator, "sso:carol", ""),
	}, people)

	_, err := h.c.Open(t.Context(), openingAs(administrator, long[:len(long)-2], long[:len(long)-2]))

	require.NoError(t, err, "256 characters")
}

// An item has at most 16 responders (R80).
func TestOpenRefusesAPrincipalOrResponderSeventeenResponders(t *testing.T) {
	h := newHarness(t)
	responders := make([]string, 17)
	for i := range responders {
		responders[i] = fmt.Sprintf("sso:r%d", i)
	}
	refusedOpenings(t, h, map[string]*app.OpenInput{
		"17 responders": openingAs(administrator, "sso:carol", responders...),
	}, func(*app.OpenInput) []string { return nil })

	_, err := h.c.Open(t.Context(), openingAs(administrator, "sso:carol", responders[:16]...))

	require.NoError(t, err, "16 responders")
}

// No one is named the bare admin (R80): it is the loopback token's subject,
// whose acts no person makes.
func TestOpenRefusesAPrincipalOrResponderBareAdmin(t *testing.T) {
	h := newHarness(t)
	refusedOpenings(t, h, map[string]*app.OpenInput{
		"a principal":         openingAs(administrator, "admin"),
		"a responder":         openingAs(administrator, "sso:carol", "admin"),
		"a principal, padded": openingAs(administrator, " admin "),
	}, func(*app.OpenInput) []string { return nil })
}

// A principal or a responder that the rules that apply always, of the
// current revision, would change, or whose escapes may hide what they
// remove, is refused (R80), as routing is: it is recorded as given.
func TestOpenRefusesAPrincipalOrResponderThatTheCurrentRevisionsRulesWouldChange(t *testing.T) {
	h := newHarness(t)
	redactsEmails(h)
	refusedOpenings(t, h, map[string]*app.OpenInput{
		"a principal":          openingAs(administrator, "sso:alice@example.com"),
		"a responder":          openingAs(administrator, "sso:carol", "sso:bob@example.com"),
		"an escaped principal": openingAs(administrator, "sso:sk-\\u0061bcdefgh"),
	}, people)

	_, err := h.c.Open(t.Context(), openingAs(administrator, "sso:alice"))

	require.NoError(t, err, "a value no rule reads")
}

// redactsEmails makes the harness's current revision one whose rule that
// applies always removes the addresses at example.com.
func redactsEmails(h *harness) {
	h.revs.set(h.revision(strings.Repeat("e", 40), func(files map[string][]byte) {
		files["redaction-rules/emails.yaml"] = []byte(`kind: RedactionRule
metadata: {name: emails, owner: security@example.com}
spec:
  always: true
  patterns: ['[a-z]+@example\.com']
`)
	}))
}

// An opener whose own subject the rules that apply always would change, as
// an email under an email rule, is refused with a reason of its own (I1):
// it names the subject claim as what to change, and never quotes the
// value. Another person's value so changed keeps the general reason.
func TestOpenSaysWhenTheOpenersOwnSubjectIsTextTheRulesRemove(t *testing.T) {
	h := newHarness(t)
	redactsEmails(h)
	alice := app.Actor{Subject: "sso:alice@example.com", Responder: true}
	aliceAdmin := app.Actor{Subject: "sso:alice@example.com", Responder: true, Admin: true}
	for name, in := range map[string]*app.OpenInput{
		"a responder as their own principal": openingAs(alice, alice.Subject),
		"an admin naming themselves":         openingAs(aliceAdmin, "sso:carol", aliceAdmin.Subject),
		"an admin as their own principal":    openingAs(aliceAdmin, " "+aliceAdmin.Subject),
	} {
		_, err := h.c.Open(t.Context(), in)

		require.ErrorIs(t, err, app.ErrInvalid, name)
		require.ErrorIs(t, err, app.ErrOwnSubjectRedacted, name)
		assert.NotContains(t, err.Error(), "alice", name)
		assert.Contains(t, err.Error(), "subject claim", name)
	}
	_, err := h.c.Open(t.Context(), openingAs(aliceAdmin, "sso:bob@example.com"))
	require.ErrorIs(t, err, app.ErrInvalid)
	require.NotErrorIs(t, err, app.ErrOwnSubjectRedacted, "another person's value keeps the general reason")
	requireNothingRecorded(h)
}

// An opening that names a responder twice, as given or once trimmed, is
// refused, never quoting the value (m10): it would be recorded, and shown,
// twice. A principal also named as a responder opens: the two are shown
// apart.
func TestOpenRefusesARespondersNamedTwice(t *testing.T) {
	h := newHarness(t)
	refusedOpenings(t, h, map[string]*app.OpenInput{
		"the same responder":          openingAs(administrator, "sso:carol", "sso:erin", "sso:erin"),
		"the same responder, trimmed": openingAs(administrator, "sso:carol", "sso:erin", " sso:erin\t"),
	}, people)

	id, err := h.c.Open(t.Context(), openingAs(administrator, "sso:carol", "sso:carol", "sso:erin"))

	require.NoError(t, err, "a principal named as a responder too")
	assert.Equal(t, []string{"sso:carol", "sso:erin"}, h.state(id).Opened.Responders)
}
