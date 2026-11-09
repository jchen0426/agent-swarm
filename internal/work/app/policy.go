package app

import (
	"fmt"
	"slices"

	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// ActKind names an act a person asks of the coordinator, as May decides it.
type ActKind string

// The acts May decides: an opening; a promotion, a close, a reopen, an
// extension, a link and an unlink, each an act on an item; and the kill
// switch's on and off.
const (
	ActOpen      ActKind = "open"
	ActPromote   ActKind = "promote"
	ActClose     ActKind = "close"
	ActReopen    ActKind = "reopen"
	ActExtend    ActKind = "extend"
	ActLink      ActKind = "link"
	ActUnlink    ActKind = "unlink"
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
//   - an opening needs a responder, an admin or an inbound source (design
//     D6), and is refused CodeResponderOnly otherwise; o is not read, and an
//     opening's checks of its principal are Open's, which reads its input;
//   - the kill switch's on needs a responder or an admin, and is refused
//     CodeResponderOnly otherwise;
//   - the off needs an admin, and is refused CodeAdminOnly otherwise;
//   - a promotion, a close, a reopen, an extension, a link and an unlink are
//     a member's, the item's principal or one of its responders, whatever the
//     member's role; else an admin's override; and refused
//     CodeNotItemResponder otherwise (D9). They need o, and answer ErrInvalid
//     without it. A link is decided on each of its two items (A4; see Link).
//     An inbound source acts on no item, its own included (design D6): it is
//     refused CodeNotItemResponder whatever the item names.
//
// A kind it does not know answers ErrInvalid. A refusal is a *Refused, and
// comes with no grounds.
func May(a Actor, k ActKind, o *item.Opened) (Grounds, error) {
	switch k {
	case ActOpen:
		if a.Responder || a.Admin || a.Source {
			return Unrestricted, nil
		}
		return 0, &Refused{Code: CodeResponderOnly}
	case ActSwitchOn:
		if a.Responder || a.Admin {
			return Unrestricted, nil
		}
		return 0, &Refused{Code: CodeResponderOnly}
	case ActSwitchOff:
		if a.Admin {
			return Unrestricted, nil
		}
		return 0, &Refused{Code: CodeAdminOnly}
	case ActPromote, ActClose, ActReopen, ActExtend, ActLink, ActUnlink:
		return mayOnItem(a, o)
	}
	return 0, fmt.Errorf("%w: no act of the kind %q", ErrInvalid, k)
}

// mayOnItem decides an act on the item o opened, as May says.
func mayOnItem(a Actor, o *item.Opened) (Grounds, error) {
	switch {
	case o == nil:
		return 0, fmt.Errorf("%w: an act on an item names its item", ErrInvalid)
	case a.Source:
		return 0, &Refused{Code: CodeNotItemResponder}
	case a.Subject != "" && (a.Subject == o.Principal || slices.Contains(o.Responders, a.Subject)):
		return Member, nil
	case a.Admin:
		return AdminOverride, nil
	}
	return 0, &Refused{Code: CodeNotItemResponder}
}
