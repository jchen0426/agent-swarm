package board

// StatusWord is the word a page shows for a hypothesis's standing.
type StatusWord string

// The status words, in the order StatusOf checks them.
const (
	StatusConverged  StatusWord = "converged"
	StatusConfirming StatusWord = "confirming"
	StatusRejected   StatusWord = "rejected"
	StatusRefuted    StatusWord = "refuted"
	StatusLeading    StatusWord = "leading"
	StatusTrailing   StatusWord = "trailing"
)

// StatusOf is the status word of the assessment's i-th hypothesis, in its
// ranked order (spec Section 10.2): converged when it converged, and
// confirming then when it is wrapUp, the hypothesis the open wrap-up round
// names (empty while no wrap-up round is open); else rejected when it is;
// else refuted when a refutation of it stands; else leading at rank 0; else
// trailing. It gives the empty word for a rank the assessment does not hold.
func StatusOf(a *Assessment, i int, wrapUp EntryID) StatusWord {
	if i < 0 || i >= len(a.Hypotheses) {
		return ""
	}
	h := &a.Hypotheses[i]
	switch {
	case h.Converged:
		if wrapUp != "" && h.ID == wrapUp {
			return StatusConfirming
		}
		return StatusConverged
	case h.Rejected:
		return StatusRejected
	case len(h.StandingRefutations) > 0:
		return StatusRefuted
	case i == 0:
		return StatusLeading
	}
	return StatusTrailing
}

// Superseded is spec 6.3's mark of a suggestion whose hypothesis "was
// rejected or lost convergence", read as written: h is rejected now, or h is
// not converged now and wasConverged, converged at some round's end. The
// caller reads wasConverged from the item's records, every round's
// item.RoundEnded.Converged, with no replay.
func Superseded(h *HypothesisView, wasConverged bool) bool {
	return h.Rejected || (!h.Converged && wasConverged)
}
