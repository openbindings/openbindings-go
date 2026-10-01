package openbindings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/openbindings/openbindings-go/internal/jsonpointer"
)

// ValueContractCompiler compiles value contracts (§3; OBI-T-08) with an
// evaluator the application supplies. It is safe for concurrent use.
type ValueContractCompiler struct {
	evaluator SchemaEvaluator
	resources *suppliedResources
}

// Resource is a schema document the application supplies for references to
// schemas the OBI document does not embed (§7.4); core fetches nothing.
type Resource struct {
	// URI is the resource's retrieval URI: absolute, with no fragment (an
	// empty one is removed). It is the base of the resource's root, and
	// references name the resource by it and by its root's $id.
	URI string
	// Document is read as a JSON Schema document. A schema embedded in
	// another format (an OpenAPI document's components, say) is not a schema
	// position of it, so a reference into one gets no verdict.
	Document json.RawMessage
}

// NewValueContractCompiler returns a compiler using e; it panics if e is nil.
// resources are schema documents the application supplies for references to
// schemas the OBI document does not embed; core copies them, and returns an
// error for one that is not JSON, whose URI is not absolute or carries a
// non-empty fragment, or whose URI another resource's URI equals in normal
// form.
func NewValueContractCompiler(e SchemaEvaluator, resources ...Resource) (*ValueContractCompiler, error) {
	if e == nil {
		panic("openbindings: NewValueContractCompiler needs an evaluator")
	}
	supplied, err := newSuppliedResources(resources)
	if err != nil {
		return nil, err
	}
	return &ValueContractCompiler{evaluator: e, resources: supplied}, nil
}

// Resolve resolves a document's schemas once (§7), calling no evaluator,
// and returns a snapshot: later changes to the Document do not affect it.
//
// A document declaring a well-formed version outside the supported set
// returns a *VersionRefusalError (OBI-T-04), and one declaring no valid
// version an error (OBI-D-09); either way it is not interpreted. A
// document that cannot be encoded returns an error, as does a done ctx.
func (c *ValueContractCompiler) Resolve(ctx context.Context, doc *Document) (*ValueContracts, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, errors.New("openbindings: the document is nil")
	}
	if refusal := versionRefusalOf(doc.OpenBindings); refusal != nil {
		return nil, refusal
	}
	if !IsValidSemver(doc.OpenBindings) {
		return nil, fmt.Errorf("openbindings: the document declares no valid version (%q is not SemVer 2.0.0, OBI-D-09), so it is not interpreted", doc.OpenBindings)
	}
	view, err := documentView(*doc)
	if err != nil {
		return nil, err
	}
	contracts := &ValueContracts{
		compiler: c,
		space:    newSchemaSpace(view, c.resources),
		keys:     map[string]string{},
		states:   map[string][2]bool{},
	}
	for key, operation := range doc.Operations {
		contracts.states[key] = [2]bool{operation.Input != nil, operation.Output != nil}
		// A name more than one operation carries resolves to none.
		for _, name := range append([]string{key}, operation.Aliases...) {
			if resolved, _, found := doc.ResolveOperation(name); found {
				contracts.keys[name] = resolved
			}
		}
	}
	return contracts, nil
}

// ValueContracts is a document's value contracts, resolved. It is safe for
// concurrent use.
type ValueContracts struct {
	compiler *ValueContractCompiler
	space    *schemaSpace
	// keys maps each operation identifier to its operation's key (OBI-T-07),
	// and states holds whether each operation states an input and an output
	// contract.
	keys   map[string]string
	states map[string][2]bool
}

// CompileInput and CompileOutput compile an operation's input or output
// contract with the evaluator; the operation is named by its key or an alias
// (OBI-T-07). Every call compiles anew: keep the *ValueContract for as long
// as you validate against it. The error matches ErrOperationNotFound, or is
// the ctx's error; otherwise the *ValueContract is never nil.
//
// They run the evaluator's Compile in the calling goroutine and wait for it,
// past the ctx's end if the evaluator does not stop sooner. They return the
// ctx's error, and no value contract, when the ctx is done before core calls
// the evaluator, or when Compile fails while the ctx is done, so a value
// contract's standing refusal (ValueContract.Err) never comes from a failure
// a done ctx may have caused. A *ValueContract holds no caller's ctx.
func (c *ValueContracts) CompileInput(ctx context.Context, operation string) (*ValueContract, error) {
	return c.compile(ctx, operation, "input")
}

// CompileOutput compiles an operation's output contract, as CompileInput
// compiles its input contract.
func (c *ValueContracts) CompileOutput(ctx context.Context, operation string) (*ValueContract, error) {
	return c.compile(ctx, operation, "output")
}

func (c *ValueContracts) compile(ctx context.Context, operation, direction string) (*ValueContract, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key, found := c.keys[operation]
	if !found {
		return nil, fmt.Errorf("%w: %q", ErrOperationNotFound, operation)
	}
	entry := jsonpointer.Format("operations", key, direction)
	stated := c.states[key][0]
	if direction == "output" {
		stated = c.states[key][1]
	}
	if !stated {
		return &ValueContract{refusal: &NoVerdictError{Location: locationOf(c.space.obi, entry), Cause: ErrNoValueContract}}, nil
	}
	document, refusal := c.space.bundle(entry, bundleSpelling{})
	if refusal != nil {
		return &ValueContract{refusal: refusal}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	compiled, err := compileWith(ctx, c.compiler.evaluator, SchemaBundle{Document: document})
	if err != nil {
		if done := ctx.Err(); done != nil {
			return nil, done
		}
		return &ValueContract{refusal: &NoVerdictError{Cause: err, hidden: !keepsVocabulary(ctx, err, false)}}, nil
	}
	return &ValueContract{compiled: compiled}, nil
}

// ValueContract is one operation's input or output contract, compiled. It is
// safe for concurrent use.
type ValueContract struct {
	compiled CompiledSchema
	refusal  *NoVerdictError
}

// Err returns the *NoVerdictError every value will get (no schema, a core
// refusal, or the evaluator's refusal to compile), or nil.
func (c *ValueContract) Err() error {
	if refusal := c.standingRefusal(); refusal != nil {
		return refusal
	}
	return nil
}

// standingRefusal returns a copy of the refusal every value gets, or nil.
func (c *ValueContract) standingRefusal() *NoVerdictError {
	switch {
	case c == nil || (c.compiled == nil && c.refusal == nil):
		return &NoVerdictError{Cause: errors.New("the value contract was not compiled")}
	case c.refusal != nil:
		refusal := *c.refusal
		return &refusal
	}
	return nil
}

// Validate validates a Go value, read as encoding/json encodes it. A
// top-level byte slice that encoding/json writes as base64 ([]byte, or a
// named byte slice that does not marshal itself) is refused, naming
// ValidateJSON; one nested in a value is a base64 string, as encoding/json
// writes it.
//
// It returns nil when the value satisfies the value contract, a
// *MismatchError when it does not, and a *NoVerdictError when no verdict was
// reached: the operation states no value contract here (ErrNoValueContract),
// the result is undefined (ErrUndefined), core or the evaluator lacks a
// capability, the value cannot be read exactly, or the ctx is done. Any other
// error says the value is not a JSON value. No returned error matches both
// ErrMismatch and ErrNoVerdict; one matches ErrUndefined only when the
// specification determines the refusal, ErrNoValueContract only when there is
// no schema, and a context error only when it is the ctx's own, after the ctx
// is done.
func (c *ValueContract) Validate(ctx context.Context, value any) error {
	if refusal := c.standingRefusal(); refusal != nil {
		return refusal
	}
	if err := ctx.Err(); err != nil {
		return &NoVerdictError{Cause: err}
	}
	read, err := readGoValue(value)
	if err != nil {
		return err
	}
	return validateWith(ctx, c.compiled, read)
}

// ValidateJSON validates JSON text, read exactly, as Validate validates a Go
// value. Text that is not one JSON value of valid UTF-8 returns an error
// saying so; text this SDK cannot read exactly (a repeated member name, an
// escaped lone UTF-16 surrogate, nesting past the decoder) gets no verdict.
func (c *ValueContract) ValidateJSON(ctx context.Context, data []byte) error {
	if refusal := c.standingRefusal(); refusal != nil {
		return refusal
	}
	if err := ctx.Err(); err != nil {
		return &NoVerdictError{Cause: err}
	}
	read, err := readJSONText(data)
	if err != nil {
		return err
	}
	return validateWith(ctx, c.compiled, read)
}
