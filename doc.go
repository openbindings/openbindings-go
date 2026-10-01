// Package openbindings is the core OpenBindings SDK for Go: the OBI
// document model, which carries a document exactly, the document rules and
// their conformance report, operation resolution, validation of values
// against value contracts (OBI-T-08), and the Core-defined constants
// (versions and media type).
//
// The package covers what the core OpenBindings specification defines, and
// nothing a binding specification or a published interface defines: those
// build on it from outside.
//
// # Documents
//
//	doc, err := openbindings.ParseDocument(data) // rejects duplicate keys (OBI-D-01)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	if _, err := doc.Validate(); err != nil {
//	    log.Fatal(err)
//	}
//
// (json.Unmarshal into Document also decodes a document exactly; ParseDocument
// additionally refuses an unsupported version (OBI-T-04) and applies the
// document schema (OBI-D-02).)
//
// The document rules judge the JSON a document is: ValidateDocument judges
// the bytes, and Validate the encoding of a host object, which is what a claim
// about a value in memory is about (§10), so for the same document both reach
// the same evidence. Validate
// returns a *ValidationError listing every violation it establishes, which
// makes it a gate. A nil error is not conformance: a rule this SDK cannot
// decide is inconclusive, not violated. The report beside the error carries
// the conclusion:
//
//	doc, report, err := openbindings.ValidateDocument(data)
//	// report.Conclusion is conformant, non-conformant, or
//	// conformance-undetermined (§10.4); err is a *ValidationError when a
//	// violation was established, and a *VersionRefusalError when the declared
//	// version is outside the supported set.
//
// JSON Schema fields preserve object and boolean schema roots. Every
// OBI declares its target spec version via the top-level openbindings
// field, and a version is interpreted exactly when [IsSupportedVersion] says
// so: every version [SupportedVersions] states, which is every release of the
// 0.2 line. A document written with this SDK declares [AuthoringVersion].
//
// # Value Contracts
//
// An operation's input and output contracts (§3) govern each caller-facing
// value. Validating a value against one (OBI-T-08) takes a [SchemaEvaluator]
// the application supplies; the SDK has none of its own, and the
// openbindings-go/schemaeval module is the project's:
//
//	compiler, _ := openbindings.NewValueContractCompiler(schemaeval.New(schemaeval.Options{}))
//	contracts, err := compiler.Resolve(ctx, doc)
//	input, err := contracts.CompileInput(ctx, "tasks.create")
//	err = input.ValidateJSON(ctx, body) // nil, a *MismatchError, a *NoVerdictError, or body is not JSON
//
// Core does what the specification fixes: it resolves the document's schemas
// (§7), and the resources the application supplies, and refuses, located and
// before any evaluation, what the specification leaves undefined
// ([ErrUndefined]) as well as what core lacks the capability for or refuses
// by conservative policy. It hands the evaluator a closed JSON Schema
// 2020-12 compound document per value contract, whose every reference
// resolves within it, and reads the evaluator's answer, keeping a panic, a
// context error, or a malformed answer from becoming a verdict. The
// evaluator evaluates. The openbindingstest package checks an evaluator
// against the contract. Core keeps no compiled value contract: the
// openbindings-go/schemaeval module's examples show a service compiling the
// contracts it serves at startup, and one compiling on demand.
//
// # An Exact Document Model
//
// Re-encoding a decoded document reproduces every member:
//   - An optional member is absent exactly when its Go value is nil (a
//     json.RawMessage also when empty, since it then holds no value). Optional
//     strings and booleans are pointers (set them with [Present], read them
//     with [Value] where absence and the zero value mean the same); optional
//     collections distinguish nil (absent) from empty (present).
//   - JSON null is carried where the model has a place for it: example
//     values and source and binding content are json.RawMessage,
//     where the bytes `null` are a present null, and a schemas entry of null
//     is a nil JSONSchema.
//   - Members are matched by exact name. Members the SDK does not model,
//     case variants of modeled ones included, are kept:
//     LosslessFields.Extensions for keys beginning with x-,
//     LosslessFields.Unknown for other keys, at every OBI-defined object.
//
// A document the model cannot carry exactly fails decoding instead of being
// altered: input that is not valid UTF-8, a duplicate member name in any
// object, a string escaping a lone UTF-16 surrogate (a Go string cannot hold
// one, and encoding/json would replace it), a JSON null at a member, map
// entry, or array element the model types (null inside a schema, an example
// value, source or binding content, or a kept member is carried), a missing required
// string member, or a binding preference that is not an integer number in
// range. ValidateDocument still judges such a document in full, except input
// OBI-D-01 refuses (not UTF-8, or repeating a member name), where which values
// the document holds is not established; and a document holding a lone
// surrogate, or input nested deeper than encoding/json reads (10000 levels),
// where it decides OBI-D-01, and OBI-D-09 on the version it reads from the
// bytes, and leaves the other rules inconclusive.
//
// Encoding refuses the same inexact bytes in the members the model carries as
// raw JSON (example values, source and binding content, and kept members),
// and a string that is not UTF-8 or a value that holds itself anywhere, so
// what it writes is the value the model holds. It does not refuse what
// decoding refuses for the typed members, such as a null where the model types
// a value or a preference out of range; Validate reports those as the
// violations they are.
//
// A typed field alone states its member: an Unknown or Extensions entry
// named like a typed member is never encoded, so a nil field is absent.
//
// # Concurrency
//
// All types in this package are safe for concurrent read access. Concurrent
// writes to the same value require external synchronization. The Validate
// method is safe for concurrent use on the same Document value (read-only).
//
// JSON marshaling and unmarshaling follow standard library semantics:
// concurrent calls on different values are safe; concurrent calls on the
// same value require synchronization.
package openbindings
