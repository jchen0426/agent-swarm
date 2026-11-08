package pages

import (
	"github.com/jchen0426/agent-swarm/internal/admin/switching"
	"github.com/jchen0426/agent-swarm/internal/work/app"
)

// words are the web's own words of each refusal's code (m4; R81), the
// coordinator's and the routing's. The admin API keeps its own texts, which
// carry the API's instructions; a person on a page reads these.
var words = map[string]string{
	switching.CodeInvalid:          "The request is not one that can be acted on.",
	switching.CodeNoSuchItem:       "There is no such item.",
	switching.CodeKeyReused:        "This form was already used for another act. Open the act again for a new form.",
	switching.CodeStale:            "The kill switch changed since this page was loaded. The page now shows its state; decide again.",
	switching.CodeHeldByLaterBuild: "This item is held by a later build of the platform, which this instance cannot act on.",
	switching.CodeNotHeld:          "This instance does not lead, so it does not act. Acts are taken on the leader.",
	switching.CodeDraining:         "This instance is shutting down and takes no new acts. Try again in a minute, when the next leader has taken over.",
	switching.CodeTakingOver:       "This instance is taking over as the leader. Please try again shortly.",
	switching.CodeUnavailable:      "This could not be done just now, and it may or may not have happened. Send the same form again: it is the same act, and is never done twice.",

	app.CodeClosed:             "The item is closed. Reopen it first.",
	app.CodeReopenWindowPassed: "The item closed too long ago to be reopened. Open a new item instead.",
	app.CodeLimitReached:       "As many items are investigating as the limit allows. Close or wait for one first.",
	app.CodeKillSwitchOn:       "The kill switch is on, so no item starts. Turn it off first.",
	app.CodeDailyCapReached:    "The day's spend has reached the daily cap, so no item starts today.",
	app.CodeAdminOnly:          "Only an admin may do this.",
	app.CodeReasonRequired:     "This needs a reason. Choose one.",
	app.CodeNotItemResponder:   "Only the item's principal, its responders, or an admin may act on this item.",
	app.CodeResponderOnly:      "Only a responder or an admin may do this.",
}

// Words are the web's words of a refusal's code, and whether it has any.
func Words(code string) (string, bool) {
	w, ok := words[code]
	return w, ok
}
