package board

// StatusWord is the word a page shows for a hypothesis's standing.
type StatusWord string

// The status words, in the order StatusOf checks them.
const (
	StatusConverged StatusWord = "converged"
	StatusRejected  StatusWord = "rejected"
	StatusRefuted   StatusWord = "refuted"
	StatusLeading   StatusWord = "leading"
	StatusTrailing  StatusWord = "trailing"
)

// StatusOf is the status word of the assessment's i-th hypothesis, in its
// ranked order: converged when it converged; else rejected when it is; else
// refuted when a refutation of it stands; else leading at rank 0; else
// trailing. It gives the empty word for a rank the assessment does not hold.
func StatusOf(a *Assessment, i int) StatusWord {
	if i < 0 || i >= len(a.Hypotheses) {
		return ""
	}
	h := &a.Hypotheses[i]
	switch {
	case h.Converged:
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
