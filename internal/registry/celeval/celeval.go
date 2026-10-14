// Package celeval checks and evaluates the configuration's conditions in CEL
// (ADR-015). It implements config.ConditionChecker for the loader, and gives
// the gateways compiled programs that run with a cost limit.
package celeval

import (
	"context"
	"fmt"
	"strings"

	"cel.dev/cel-go/cel"

	"github.com/jchen0426/agent-swarm/internal/registry/config"
)

// CostLimit bounds the work of one evaluation, in CEL's cost units: about
// one per operation, and per element that a macro visits.
const CostLimit = 100_000

// maxConditionBytes caps a condition's text.
const maxConditionBytes = 2048

// Conditions holds one CEL environment for each environment of
// config.Environment.
type Conditions struct {
	envs map[config.Environment]environment
}

type environment struct {
	env *cel.Env
	out *cel.Type
}

// New builds the environments. Every variable is declared with its type, so
// a condition is type-checked when configuration loads.
func New() (*Conditions, error) {
	strs := cel.MapType(cel.StringType, cel.StringType)
	defs := []struct {
		name config.Environment
		vars []cel.EnvOption
		out  *cel.Type
	}{
		{config.EnvPartialWhen, []cel.EnvOption{cel.Variable("result", cel.DynType)}, cel.BoolType},
		{config.EnvScope, []cel.EnvOption{cel.Variable("hypothesis", strs), cel.Variable("item", strs)}, cel.StringType},
		{config.EnvCheck, []cel.EnvOption{cel.Variable("trusted", strs), cel.Variable("event", cel.DynType)}, cel.BoolType},
	}
	c := &Conditions{envs: map[config.Environment]environment{}}
	for _, d := range defs {
		env, err := cel.NewEnv(d.vars...)
		if err != nil {
			return nil, fmt.Errorf("celeval: build the %s environment: %w", d.name, err)
		}
		c.envs[d.name] = environment{env: env, out: d.out}
	}
	return c, nil
}

// Check implements config.ConditionChecker.
func (c *Conditions) Check(env config.Environment, expr string) error {
	_, err := c.Compile(env, expr)
	return err
}

// Compile checks a condition and prepares it to run.
func (c *Conditions) Compile(env config.Environment, expr string) (*Program, error) {
	e, ok := c.envs[env]
	if !ok {
		return nil, fmt.Errorf("celeval: no environment %q", env)
	}
	if len(expr) > maxConditionBytes {
		return nil, fmt.Errorf("is longer than %d bytes", maxConditionBytes)
	}
	ast, iss := e.env.Compile(expr)
	if iss.Err() != nil {
		return nil, fmt.Errorf("is not a valid condition: %s", firstLine(iss.Err().Error()))
	}
	if !ast.OutputType().IsExactType(e.out) {
		return nil, fmt.Errorf("must produce %s, but produces %s", e.out, ast.OutputType())
	}
	prg, err := e.env.Program(ast, cel.CostLimit(CostLimit), cel.InterruptCheckFrequency(100))
	if err != nil {
		return nil, fmt.Errorf("cannot run: %w", err)
	}
	return &Program{prg: prg}, nil
}

// firstLine keeps the first line of CEL's error, which carries the position.
func firstLine(msg string) string {
	line, _, _ := strings.Cut(msg, "\n")
	return line
}

// Program is a checked condition, ready to run.
type Program struct {
	prg cel.Program
}

// Bool runs a condition that produces a bool. vars binds the environment's
// variables; their values are data, never condition text.
func (p *Program) Bool(ctx context.Context, vars map[string]any) (bool, error) {
	v, err := p.eval(ctx, vars)
	if err != nil {
		return false, err
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("celeval: produced %T, not a bool", v)
	}
	return b, nil
}

// String runs a condition that produces a string.
func (p *Program) String(ctx context.Context, vars map[string]any) (string, error) {
	v, err := p.eval(ctx, vars)
	if err != nil {
		return "", err
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("celeval: produced %T, not a string", v)
	}
	return s, nil
}

func (p *Program) eval(ctx context.Context, vars map[string]any) (any, error) {
	val, _, err := p.prg.ContextEval(ctx, vars)
	if err != nil {
		return nil, fmt.Errorf("celeval: %w", err)
	}
	return val.Value(), nil
}
