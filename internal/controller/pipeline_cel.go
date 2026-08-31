package controller

import (
	"fmt"
	"sync"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
)

// PipelineCELEvaluator evaluates CEL expressions for pipeline stage conditions.
// It caches compiled programs to avoid recompilation on repeated reconciles.
type PipelineCELEvaluator struct {
	env   *cel.Env
	cache sync.Map // map[string]cel.Program
}

// NewPipelineCELEvaluator creates a CEL evaluator configured with the pipeline
// stage context schema.
func NewPipelineCELEvaluator() (*PipelineCELEvaluator, error) {
	env, err := cel.NewEnv(
		cel.Variable("stages", cel.MapType(cel.StringType, cel.MapType(cel.StringType, cel.AnyType))),
	)
	if err != nil {
		return nil, fmt.Errorf("create CEL environment: %w", err)
	}
	return &PipelineCELEvaluator{env: env}, nil
}

// Evaluate runs a CEL expression against the provided stage contexts.
// Returns true if the predicate passes, false if it does not.
// Returns an error if the expression is invalid or does not produce a boolean.
func (e *PipelineCELEvaluator) Evaluate(expr string, stageContexts map[string]StageContext) (bool, error) {
	prg, err := e.getOrCompile(expr)
	if err != nil {
		return false, err
	}

	// Build the activation map: stages → map[name]map[field]value
	stagesMap := make(map[string]interface{}, len(stageContexts))
	for name, ctx := range stageContexts {
		stageData := map[string]interface{}{
			"results": ctx.Results,
			"phase":   ctx.Phase,
		}
		stagesMap[name] = stageData
	}

	out, _, err := prg.Eval(map[string]interface{}{
		"stages": stagesMap,
	})
	if err != nil {
		return false, fmt.Errorf("evaluate expression %q: %w", expr, err)
	}

	b, ok := out.(ref.Val)
	if !ok {
		return false, fmt.Errorf("expression %q did not produce a value", expr)
	}
	if b.Type() != types.BoolType {
		return false, fmt.Errorf("expression %q produced %s, expected bool", expr, b.Type())
	}
	return b.Value().(bool), nil
}

func (e *PipelineCELEvaluator) getOrCompile(expr string) (cel.Program, error) {
	if cached, ok := e.cache.Load(expr); ok {
		return cached.(cel.Program), nil
	}

	ast, issues := e.env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("compile expression %q: %w", expr, issues.Err())
	}

	prg, err := e.env.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("program expression %q: %w", expr, err)
	}

	e.cache.Store(expr, prg)
	return prg, nil
}
