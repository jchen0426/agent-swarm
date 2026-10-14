package config

import (
	"fmt"
	"slices"
	"time"

	"github.com/jchen0426/agent-swarm/internal/registry/selector"
	"github.com/jchen0426/agent-swarm/internal/registry/tmpl"
)

// Defaults of spec Section 15 for trigger mappings.
const (
	defaultMaxAge = 5 * time.Minute
	defaultWithin = 30 * time.Minute
)

// MaxAgeOrDefault returns how old a signed request may be.
func (a Authentication) MaxAgeOrDefault() time.Duration {
	if a.MaxAge == nil {
		return defaultMaxAge
	}
	return *a.MaxAge
}

// WithinOrDefault returns the correlation window.
func (c *Correlation) WithinOrDefault() time.Duration {
	if c.Within == nil {
		return defaultWithin
	}
	return *c.Within
}

// FiringValues returns the source's status values that mean firing.
func (s StatusMapping) FiringValues() []string {
	if len(s.Firing) == 0 {
		return []string{"firing"}
	}
	return s.Firing
}

// ResolvedValues returns the source's status values that mean resolved.
func (s StatusMapping) ResolvedValues() []string {
	if len(s.Resolved) == 0 {
		return []string{"resolved"}
	}
	return s.Resolved
}

// checkTriggerMappings applies the rules of spec Section 3.6: one mapping per
// source, authenticated requests, and routing, ordering and identity from
// trusted fields only.
func (ix *index) checkTriggerMappings(cc ConditionChecker, ps *problems) {
	sources := map[string]string{}
	for _, name := range sortedKeys(ix.mappings) {
		m := ix.mappings[name]
		f, sp := m.file, &m.Spec
		if !objectName.MatchString(sp.Source) {
			ps.add(f, "spec.source", "must be a lowercase DNS label, got %q", sp.Source)
		} else if other, dup := sources[sp.Source]; dup {
			ps.add(f, "spec.source", "is already mapped by %q; a source has one mapping", other)
		}
		sources[sp.Source] = name
		checkAuthentication(f, sp.Authentication, ps)
		if sp.Split != "" {
			if _, err := selector.Parse(sp.Split); err != nil {
				ps.add(f, "spec.split", "%v", err)
			}
		}
		trusted := checkTrustedFields(f, sp.TrustedFields, ps)
		routed := func(field, text string, required bool) {
			checkRouted(f, field, text, trusted, required, ps)
		}
		routed("spec.eventId", sp.EventID, false)
		routed("spec.status.value", sp.Status.Value, true)
		checkStatusValues(f, sp.Status, ps)
		routed("spec.occurredAt.firing", sp.OccurredAt.Firing, true)
		routed("spec.occurredAt.resolved", sp.OccurredAt.Resolved, true)
		routed("spec.dedupKey", sp.DedupKey, true)
		routed("spec.instanceId", sp.InstanceID, true)
		if c := sp.Correlate; c != nil {
			routed("spec.correlate.key", c.Key, true)
			positive(f, "spec.correlate.within", c.Within, defaultWithin, ps)
		}
		if sp.Title != "" {
			if _, err := tmpl.Parse(sp.Title); err != nil {
				ps.add(f, "spec.title", "%v", err)
			}
		}
		if ix.workTypes[sp.WorkItemType] == nil {
			ps.add(f, "spec.workItemType", "names no work item type %q", sp.WorkItemType)
		}
		ix.checkIdentityRead(f, "spec.principal", sp.Principal, trusted, ps)
		ix.checkIdentityRead(f, "spec.responders", sp.Responders, trusted, ps)
		if fp := sp.FallbackPrincipal; fp != nil && !token.MatchString(fp.Group) {
			ps.add(f, "spec.fallbackPrincipal.group", "must be a group name, got %q", fp.Group)
		}
		ix.checkRedactNames(f, "spec.redact", sp.Redact, ps)
		for i, c := range sp.Checks {
			if err := cc.Check(EnvCheck, c); err != nil {
				ps.add(f, fmt.Sprintf("spec.checks[%d]", i), "%v", err)
			}
		}
	}
}

// checkAuthentication requires exactly one scheme. A signed body carries a
// signed timestamp, so only it has a maximum age.
func checkAuthentication(f string, a Authentication, ps *problems) {
	switch {
	case (a.HMACSecretRef == "") == (a.BearerSecretRef == ""):
		ps.add(f, "spec.authentication", "needs exactly one of hmacSecretRef and bearerSecretRef")
	case a.HMACSecretRef != "" && !secretRef.MatchString(a.HMACSecretRef):
		ps.add(f, "spec.authentication.hmacSecretRef", "must be secret://<path>, got %q", a.HMACSecretRef)
	case a.BearerSecretRef != "" && !secretRef.MatchString(a.BearerSecretRef):
		ps.add(f, "spec.authentication.bearerSecretRef", "must be secret://<path>, got %q", a.BearerSecretRef)
	}
	if a.MaxAge != nil {
		if a.HMACSecretRef == "" {
			ps.add(f, "spec.authentication.maxAge", "applies to signed requests only")
		}
		positive(f, "spec.authentication.maxAge", a.MaxAge, defaultMaxAge, ps)
	}
}

// checkTrustedFields checks the trusted field names and returns them as a set.
func checkTrustedFields(f string, fields []string, ps *problems) map[string]bool {
	if len(fields) == 0 {
		ps.add(f, "spec.trustedFields", "lists no fields; routing needs trusted fields")
	}
	set := map[string]bool{}
	for i, name := range fields {
		tpl, err := tmpl.Parse("{" + name + "}")
		if err != nil || len(tpl.Names()) != 1 || set[name] {
			ps.add(f, fmt.Sprintf("spec.trustedFields[%d]", i), "must be a field path such as labels.service, each once, got %q", name)
		}
		set[name] = true
	}
	return set
}

// checkRouted checks a template that routes, orders or identifies events: it
// must use at least one field, and only trusted ones.
func checkRouted(f, field, text string, trusted map[string]bool, required bool, ps *problems) {
	if text == "" {
		if required {
			ps.add(f, field, "is required")
		}
		return
	}
	tpl, err := tmpl.Parse(text)
	if err != nil {
		ps.add(f, field, "%v", err)
		return
	}
	if len(tpl.Names()) == 0 {
		ps.add(f, field, "uses no field")
	}
	for _, hole := range tpl.Names() {
		if !trusted[hole] {
			ps.add(f, field, "uses {%s}, which is not a trusted field; routing uses trusted fields only", hole)
		}
	}
}

func checkStatusValues(f string, s StatusMapping, ps *problems) {
	firing, resolved := s.FiringValues(), s.ResolvedValues()
	for i, v := range firing {
		if v == "" || slices.Contains(resolved, v) {
			ps.add(f, fmt.Sprintf("spec.status.firing[%d]", i), "must be a value that does not also mean resolved, got %q", v)
		}
	}
	for i, v := range resolved {
		if v == "" {
			ps.add(f, fmt.Sprintf("spec.status.resolved[%d]", i), "is empty")
		}
	}
}

// checkIdentityRead checks the read that resolves a principal or responders:
// a registered identity source, with arguments from trusted fields only.
func (ix *index) checkIdentityRead(f, field string, r *PlatformRead, trusted map[string]bool, ps *problems) {
	if r == nil {
		ps.add(f, field, "is required")
		return
	}
	ix.checkRead(f, field, "", r, func(hole string) string {
		if trusted[hole] {
			return ""
		}
		return fmt.Sprintf("uses {%s}, which is not a trusted field; identity reads use trusted fields only", hole)
	}, ps)
	if ref, err := ParseToolRef(r.Tool); err == nil && ix.readTool(ref) == "" && ix.identitySourceFor(ref) == nil {
		ps.add(f, field+".tool", "names %s, which is not registered as an identity source", ref)
	}
}
