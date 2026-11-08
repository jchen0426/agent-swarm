package pages

import (
	"github.com/jchen0426/agent-swarm/internal/admin/switching"
	"github.com/jchen0426/agent-swarm/internal/work/app"
)

// words are the web's own words of each refusal's code (m4; R81), the
// coordinator's and the routing's. The admin API keeps its own texts, which
// carry the API's instructions; a person on a page reads these.
// standbyWords answer an act asked of a standby, which the web refuses before
// it reaches the coordinator: nothing was tried, so no outcome is in doubt,
// unlike a not_held answer that comes back after an act was sent.
const standbyWords = "This instance does not lead now, so it did not try this. Nothing was done here. Acts are taken on the leader."

var words = map[string]string{
	switching.CodeInvalid:          "The request is not one that can be acted on.",
	switching.CodeNoSuchItem:       "There is no such item.",
	switching.CodeKeyReused:        "This form was already used for another act. Open the act again for a new form.",
	switching.CodeStale:            "The kill switch changed since this page was loaded. The page now shows its state; decide again.",
	switching.CodeHeldByLaterBuild: "This item is held by a later build of the platform, which this instance cannot act on.",
	switching.CodeNotHeld:          "This instance does not lead now, so the outcome of an act just sent is not known here: check the items list to see whether it took effect. Acts are taken on the leader.",
	switching.CodeDraining:         "This instance is shutting down, so the outcome of an act just sent is not known here. In a minute, when the next leader has taken over, check the items list to see whether it took effect.",
	switching.CodeTakingOver:       "This instance is taking over as the leader. Please try again shortly.",
	switching.CodeUnavailable:      "This could not be done just now, and it may or may not have happened. Send the same form again: it is the same act, and is never done twice.",
	switching.CodeStopping:         "The run is stopping right now. Send the same form again in a moment.",

	app.CodeClosed:             "The item is closed. Reopen it first.",
	app.CodeReopenWindowPassed: "The item closed too long ago to be reopened. Open a new item instead.",
	app.CodeLimitReached:       "As many items are investigating as the limit allows. Close or wait for one first.",
	app.CodeKillSwitchOn:       "The kill switch is on, so no item starts. Turn it off first.",
	app.CodeDailyCapReached:    "The day's spend has reached the daily cap, so no item starts today.",
	app.CodeAdminOnly:          "Only an admin may do this.",
	app.CodeReasonRequired:     "This needs a reason. Choose one.",
	app.CodeNotItemResponder:   "Only the item's principal, its responders, or an admin may act on this item.",
	app.CodeResponderOnly:      "Only a responder or an admin may do this.",
	app.CodeNotStarted:         "This item has no run yet, so there is nothing to extend. Promote it first.",
	app.CodeNoAgentsLeft:       "The run has no agent left to take part, so it cannot resume.",
	app.CodeWouldStopAtOnce:    "The run has used the budget this extension would give it, so it would stop at once. Extend it by more.",
}

// Words are the web's words of a refusal's code, and whether it has any.
func Words(code string) (string, bool) {
	w, ok := words[code]
	return w, ok
}
