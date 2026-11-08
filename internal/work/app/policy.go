package app

import (
	"fmt"
	"slices"

	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// ActKind names an act a person asks of the coordinator, as May decides it.
type ActKind string

// The acts May decides: an opening; a promotion, a close, a reopen and an
// extension, each an act on an item; and the kill switch's on and off.
const (
	ActOpen      ActKind = "open"
	ActPromote   ActKind = "promote"
	ActClose     ActKind = "close"
	ActReopen    ActKind = "reopen"
	ActExtend    ActKind = "extend"
	ActSwitchOn  ActKind = "switch_on"
	ActSwitchOff ActKind = "switch_off"
)

// Grounds is why May allows an act (R77): the coordinator uses May's error,
// and the web layer its grounds, to show the buttons a row offers and to ask
// for fresh authentication where an act is an admin's override. The zero
// value is no grounds, May's answer with its error.
type Grounds int

// The grounds of an allowed act: Member, the actor is the item's principal or
// one of its responders; AdminOverride, the actor is neither, and acts as an
// admin (spec Section 9.1); Unrestricted, the act is not an item's, and the
// actor's role allows it.
const (
	Member Grounds = iota + 1
	AdminOverride
	Unrestricted
)

// May decides whether a acts k on the item o opened (R76, R77; design D5),
// with no state but its arguments:
//   - the kill switch's on, and an opening, need a responder or an admin,
//     and are refused CodeResponderOnly otherwise; o is not read, and an
//     opening's checks of its principal are Open's, which reads its input;
//   - the off needs an admin, and is refused CodeAdminOnly otherwise;
//   - a promotion, a close, a reopen and an extension are a member's, the
//     item's principal or one of its responders, whatever the member's role;
//     else an admin's override; and refused CodeNotItemResponder otherwise
//     (D9). They need o, and answer ErrInvalid without it.
//
// A kind it does not know answers ErrInvalid. A refusal is a *Refused, and
// comes with no grounds.
func May(a Actor, k ActKind, o *item.Opened) (Grounds, error) {
	switch k {
	case ActOpen, ActSwitchOn:
		if a.Responder || a.Admin {
			return Unrestricted, nil
		}
		return 0, &Refused{Code: CodeResponderOnly}
	case ActSwitchOff:
		if a.Admin {
			return Unrestricted, nil
		}
		return 0, &Refused{Code: CodeAdminOnly}
	case ActPromote, ActClose, ActReopen, ActExtend:
		switch {
		case o == nil:
			return 0, fmt.Errorf("%w: an act on an item names its item", ErrInvalid)
		case a.Subject != "" && (a.Subject == o.Principal || slices.Contains(o.Responders, a.Subject)):
			return Member, nil
		case a.Admin:
			return AdminOverride, nil
		}
		return 0, &Refused{Code: CodeNotItemResponder}
	}
	return 0, fmt.Errorf("%w: no act of the kind %q", ErrInvalid, k)
}
