// Package schemaeval is the OpenBindings project's schema evaluator: an
// openbindings.SchemaEvaluator over santhosh-tekuri/jsonschema/v6, with
// patterns matched as ECMA-262 regular expressions with the u flag by
// dlclark/regexp2.
//
//	compiler, err := openbindings.NewValueContractCompiler(schemaeval.New(schemaeval.Options{}))
//
// It evaluates a bundle as JSON Schema 2020-12 with format an annotation. It
// gives no verdict, only where evaluation reaches it, for a pattern its
// engine does not match as ECMA-262 does (a Unicode property escape, a
// backreference to a group inside a quantified atom), a pattern match that
// meets Options.PatternMatchTimeout, a const or enum holding a number beyond
// its numeric limits (4096 characters, an exponent within ±10000), and a
// $dynamicRef applied to property names, which the library checks without
// the dynamic scope. Comparison and count keywords are decided exactly
// whatever their numbers. A value holding a number beyond the limits gets no
// verdict when the bundle compares numbers anywhere, reached or not, and is
// otherwise validated with a stand-in number. Where a
// schema's type fails, the library reports none of that schema's other
// failing keywords, so its problems can be fewer than the contract's. The
// library (v6.0.3) records a failed propertyNames' instance location without
// copying it, so for an object below the value's top level a later sibling
// can overwrite it, and which one depends on the order the library walks the
// object's members, which is not fixed; so a failed propertyNames is located
// at the root of the value, whatever object it applies to, with each name's
// message in sorted order. openbindingstest checks it against the evaluator
// contract.
//
// # Adapting another library
//
// This module's approach adapts other JSON Schema libraries too:
//   - Compile each bundle with a compiler of its own, so no bundle affects
//     another's, and read JSON numbers as json.Number, never float64.
//   - Give the library a loader that fails for every URI. Every reference
//     resolves within the bundle, so the loader is never needed; if the
//     library can finish compiling with a reference unresolved, check that
//     it resolved every one and fail Compile if not.
//   - Where the library cannot decide a keyword, give no verdict only where
//     evaluation reaches it: this module replaces such a keyword with a
//     format of its own that stops evaluation when applied.
//   - Project the library's errors onto the contract's problem locations
//     (see openbindings.SchemaEvaluator). Libraries commonly evaluate a
//     missing required property's schema against null, report the failed
//     branches of a passing applicator, locate propertyNames at the name or
//     once per name, or report unevaluatedItems once for all items.
//   - Run openbindingstest.TestSchemaEvaluator under -race, and name in its
//     Options, with a reason, each case the library cannot decide or
//     locate.
package schemaeval

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/openbindings/openbindings-go"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Options configure an Evaluator.
type Options struct {
	// PatternMatchTimeout bounds one match of a pattern against one string;
	// a match that meets it gives no verdict. Zero means 100 milliseconds.
	PatternMatchTimeout time.Duration
}

// Evaluator is safe for concurrent use.
type Evaluator struct {
	patterns patternEngine
}

var _ openbindings.SchemaEvaluator = (*Evaluator)(nil)

// New returns an evaluator.
func New(o Options) *Evaluator {
	timeout := o.PatternMatchTimeout
	if timeout <= 0 {
		timeout = defaultPatternTimeout
	}
	return &Evaluator{patterns: patternEngine{timeout: timeout}}
}

// Compile compiles a bundle with a compiler of its own, so no bundle affects
// another. It obtains nothing: a reference the library cannot resolve within
// the bundle fails the compile.
func (e *Evaluator) Compile(ctx context.Context, bundle openbindings.SchemaBundle) (openbindings.CompiledSchema, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(bundle.Document))
	if err != nil {
		return nil, fmt.Errorf("schemaeval: the bundle is not JSON: %w", err)
	}
	p := prepare(document)
	root, _ := p.document.(map[string]any)
	id, _ := root["$id"].(string)
	c := jsonschema.NewCompiler()
	c.UseLoader(obtainNothing{})
	c.UseRegexpEngine(e.patterns.compile)
	c.AssertFormat()
	for name, s := range p.sentinels {
		c.RegisterFormat(&jsonschema.Format{Name: name, Validate: s.validate})
	}
	for name, b := range p.bounds {
		c.RegisterFormat(&jsonschema.Format{Name: name, Validate: b.validate})
	}
	if err := c.AddResource(id, p.document); err != nil {
		return nil, fmt.Errorf("schemaeval: the library does not take the bundle: %w", err)
	}
	schema, err := c.Compile(id)
	if err != nil {
		return nil, fmt.Errorf("schemaeval: the library cannot compile the bundle: %w", err)
	}
	return &compiled{schema: schema, compares: p.compares}, nil
}

// validate stops evaluation on an instance of one of the sentinel's types.
func (s sentinelFormat) validate(v any) error {
	if s.types == nil || slices.Contains(s.types, jsonType(v)) {
		panic(sentinel{s.reason})
	}
	return nil
}

func jsonType(v any) string {
	switch v.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case nil:
		return "null"
	}
	return "number"
}

type obtainNothing struct{}

func (obtainNothing) Load(url string) (any, error) {
	return nil, fmt.Errorf("schemaeval obtains nothing, and %s is not in the bundle", url)
}

type compiled struct {
	schema   *jsonschema.Schema
	compares bool
}

// Validate validates a JSON value. It runs to completion once started: the
// library takes no context.
func (c *compiled) Validate(ctx context.Context, value any) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	checked := substitution{Value: value}
	if at, limit := numericLimits(value); limit != nil {
		if c.compares {
			return fmt.Errorf("schemaeval: the value holds, at %q, %w, and the schema compares numbers", at, limit)
		}
		checked = substitute(value)
	}
	defer func() {
		if r := recover(); r != nil {
			s, ok := r.(sentinel)
			if !ok {
				panic(r)
			}
			err = errors.New("schemaeval: evaluation reached what this evaluator cannot decide: " + s.reason)
		}
	}()
	result := c.schema.Validate(checked.Value)
	var invalid *jsonschema.ValidationError
	switch {
	case result == nil:
		return nil
	case !errors.As(result, &invalid) || refCycle(result):
		return fmt.Errorf("schemaeval: %w", result)
	}
	return &openbindings.MismatchError{Problems: problems(invalid, checked.standsFor), Cause: result}
}
