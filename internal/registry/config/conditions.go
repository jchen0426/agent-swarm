package config

// Environment is where a condition runs: the variables it sees and the type
// it must produce. Values are bound to the variables, never spliced into the
// condition's text (spec Section 3.3).
type Environment string

// The environments of the configuration's conditions.
const (
	// EnvPartialWhen sees a read's decoded output as result, a dynamic value,
	// and produces a bool: true makes the output a read error.
	EnvPartialWhen Environment = "partialWhen"
	// EnvScope sees the cited hypothesis's key as hypothesis and the item's
	// trusted routing values as item, both maps of strings, and produces the
	// string that a write argument must equal.
	EnvScope Environment = "scope"
	// EnvCheck sees an inbound event's trusted fields as trusted, a map of
	// strings, and the whole event as event, a dynamic value, and produces a
	// bool: false makes the item diagnosis-only.
	EnvCheck Environment = "check"
)

// ConditionChecker checks a condition when configuration loads: that it
// parses, uses only its environment's variables and produces the
// environment's type (ADR-015). The condition language lives behind it.
type ConditionChecker interface {
	Check(env Environment, expr string) error
}
