package check

import (
	"fmt"
	"strings"
)

// systemPrompt is the platform's rules of the board in a check, followed by
// the agent's skill.
func systemPrompt(entryLimit int, skill string) string {
	return fmt.Sprintf(`You are one of several agents investigating an incident. In this check each agent works alone: you cannot see what the others post, and they cannot see what you post. Hypotheses with the same key are combined across agents.

How the board works:
- Read with the read tools. Every read's result starts with its call_id. The data ends at the alert.
- post_evidence records one finding: a short claim, the call_id of the read it comes from, and a quote copied exactly from that read's output. Evidence whose quote is not in the output it cites is refused. It returns the evidence's id.
- post_hypothesis proposes a cause: the service where the cause lies, the failure mode, one sentence, and the ids that post_evidence returned for your evidence that supports it. Use "other" when no listed value fits.
- You can post at most %d entries, evidence and hypotheses together.
- Call end_turn when you are done.

%s`, entryLimit, skill)
}

// taskPrompt is all that an agent learns of a case: the alert, the services,
// the failure modes and what the servers say of themselves.
func taskPrompt(set *Set, t *Task, instructions []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The alert:\n%s\n\n", strings.TrimSpace(t.Alert))
	fmt.Fprintf(&b, "Services: %s\n\nFailure modes:\n", strings.Join(t.Services, ", "))
	for _, m := range set.FailureModes {
		fmt.Fprintf(&b, "- %s: %s\n", m.Name, m.Meaning)
	}
	if len(instructions) > 0 {
		b.WriteString("\nRead servers:\n")
		for _, in := range instructions {
			fmt.Fprintf(&b, "- %s\n", in)
		}
	}
	b.WriteString("\nFind the cause of the alert.")
	return b.String()
}
