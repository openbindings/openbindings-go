package openbindingstest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/openbindings/openbindings-go"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// recorder is a testing.TB that records what the kit reports rather than
// failing the test running it.
type recorder struct {
	testing.TB
	mu       sync.Mutex
	reported []string
}

func (r *recorder) Helper()                           {}
func (r *recorder) Log(...any)                        {}
func (r *recorder) Logf(string, ...any)               {}
func (r *recorder) Error(args ...any)                 { r.record(fmt.Sprint(args...)) }
func (r *recorder) Errorf(format string, args ...any) { r.record(fmt.Sprintf(format, args...)) }

func (r *recorder) record(message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reported = append(r.reported, message)
}

func (r *recorder) found(fragment string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, message := range r.reported {
		if strings.Contains(message, fragment) {
			return true
		}
	}
	return false
}

// naive evaluates with santhosh-tekuri/jsonschema/v6 as it comes, with a
// loader, a compiled schema, and an answer each fault may replace.
type naive struct {
	loader   jsonschema.URLLoader
	compile  func(bundle openbindings.SchemaBundle, compiled openbindings.CompiledSchema) (openbindings.CompiledSchema, error)
	validate func(ctx context.Context, value any, answer error) error
}

func (e naive) Compile(ctx context.Context, bundle openbindings.SchemaBundle) (openbindings.CompiledSchema, error) {
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(bundle.Document))
	if err != nil {
		return nil, err
	}
	id, _ := document.(map[string]any)["$id"].(string)
	c := jsonschema.NewCompiler()
	c.UseLoader(e.loader)
	if e.loader == nil {
		c.UseLoader(failing{})
	}
	if err := c.AddResource(id, document); err != nil {
		return nil, err
	}
	schema, err := c.Compile(id)
	if err != nil {
		return nil, fmt.Errorf("naive: %w", err)
	}
	var compiled openbindings.CompiledSchema = naiveSchema{schema, e.validate}
	if e.compile != nil {
		return e.compile(bundle, compiled)
	}
	return compiled, nil
}

type failing struct{}

func (failing) Load(url string) (any, error) { return nil, fmt.Errorf("%s is not obtained", url) }

type naiveSchema struct {
	schema   *jsonschema.Schema
	validate func(ctx context.Context, value any, answer error) error
}

func (s naiveSchema) Validate(ctx context.Context, value any) error {
	answer := s.schema.Validate(value)
	var invalid *jsonschema.ValidationError
	if errors.As(answer, &invalid) {
		answer = &openbindings.MismatchError{Problems: []openbindings.SchemaProblem{{InstanceLocation: "", Message: invalid.Error()}}, Cause: answer}
	}
	if s.validate != nil {
		return s.validate(ctx, value, answer)
	}
	return answer
}

// The kit catches each fault an evaluator can have: it reports the one
// injected, whatever else the naive evaluator gets wrong.
func TestKitCatchesFaults(t *testing.T) {
	var cached sync.Map
	for name, c := range map[string]struct {
		evaluator openbindings.SchemaEvaluator
		options   Options
		reported  string
	}{
		"a network request": {naive{loader: fetching{}}, Options{}, "made a network request"},
		"core's refusal vocabulary": {naive{validate: func(_ context.Context, _ any, answer error) error {
			if answer != nil {
				return fmt.Errorf("naive: %w", errors.Join(answer, openbindings.ErrUndefined))
			}
			return nil
		}}, Options{}, "refusal sentinels"},
		"its own deadline": {naive{validate: func(context.Context, any, error) error { return context.DeadlineExceeded }}, Options{}, "context error while its ctx is live"},
		"a spelling dependence": {naive{compile: func(bundle openbindings.SchemaBundle, compiled openbindings.CompiledSchema) (openbindings.CompiledSchema, error) {
			if bytes.Contains(bundle.Document, []byte("contract.invalid")) {
				return answering{nil}, nil
			}
			return compiled, nil
		}}, Options{}, "must not depend on core's spellings"},
		"state between bundles": {naive{compile: func(bundle openbindings.SchemaBundle, compiled openbindings.CompiledSchema) (openbindings.CompiledSchema, error) {
			key := string(bundle.Document[:bytes.Index(bundle.Document, []byte(`"$ref"`))+1])
			first, _ := cached.LoadOrStore(key, compiled)
			return first.(openbindings.CompiledSchema), nil
		}}, Options{}, "concurrency and isolation"},
		"a changed bundle": {naive{compile: func(bundle openbindings.SchemaBundle, compiled openbindings.CompiledSchema) (openbindings.CompiledSchema, error) {
			// Outside the concurrency invariant, whose goroutines share a
			// bundle, where this fault would race as well as fail.
			if len(bundle.Document) > 0 && !bytes.Contains(bundle.Document, []byte("kit.invalid/shared")) {
				bundle.Document[len(bundle.Document)-1] = ' '
			}
			return compiled, nil
		}}, Options{}, "changed the bundle's Document"},
		"a changed value": {naive{validate: func(_ context.Context, value any, answer error) error {
			if object, ok := value.(map[string]any); ok {
				object["added"] = true
			}
			return answer
		}}, Options{}, "Validate changed the value"},
		"a panic":         {naive{validate: func(context.Context, any, error) error { panic("naive") }}, Options{}, "panicked"},
		"a wrong verdict": {naive{validate: func(context.Context, any, error) error { return nil }}, Options{}, "want mismatch"},
		"a mismatch from Compile": {naive{compile: func(openbindings.SchemaBundle, openbindings.CompiledSchema) (openbindings.CompiledSchema, error) {
			return nil, &openbindings.MismatchError{}
		}}, Options{}, "Compile error holds a *MismatchError"},
		"wrong paths":                      {naive{}, Options{}, "problem paths"},
		"an unresolved reference as valid": {naive{loader: permissive{}}, Options{}, "invariant, unresolved references"},
		"a stale exemption":                {naive{}, Options{Undecided: map[string]string{"adversarial/no-such-case": "none"}}, "which is no case"},
	} {
		r := &recorder{TB: t}
		check(r, c.evaluator, c.options)
		if !r.found(c.reported) {
			t.Errorf("%s: the kit did not report %q", name, c.reported)
		}
	}
}

// answering answers the same whatever the value.
type answering struct{ answer error }

func (a answering) Validate(context.Context, any) error { return a.answer }

// fetching obtains a URL it is asked for.
type fetching struct{}

func (fetching) Load(url string) (any, error) {
	response, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	return nil, response.Body.Close()
}

// permissive answers every URL with a schema every value satisfies.
type permissive struct{}

func (permissive) Load(string) (any, error) { return true, nil }
