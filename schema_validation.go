package openbindings

import (
	_ "embed"
	"errors"
	"fmt"
	json "github.com/openbindings/openbindings-go/internal/thirdparty/jsoncodec"
	neturl "net/url"
	"sort"
	"strings"

	"github.com/openbindings/openbindings-go/internal/thirdparty/jsonschema"
	"github.com/openbindings/openbindings-go/internal/thirdparty/jsonschema/kind"
	"github.com/openbindings/openbindings-go/jsonvalue"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// kindPrinter renders jsonschema/v6 ErrorKind values. The kinds implement
// LocalizedString(*message.Printer), not String(), so formatting them with
// %v would print the raw struct (e.g. `&{[customer]}` for a missing
// required property). The formatted text remains local validation evidence;
// abstract invocation failures carry only ERR_VALIDATION_FAILED.
var kindPrinter = message.NewPrinter(language.English)

func kindString(k jsonschema.ErrorKind) string {
	return k.LocalizedString(kindPrinter)
}

// openbindingsSchemaJSON is the OBI document schema (openbindings.schema.json),
// embedded at build time. Synced from the spec repo via scripts/sync-schema.sh.
//
//go:embed openbindings.schema.json
var openbindingsSchemaJSON []byte

// compiledOBISchema is the embedded OBI document schema, compiled once at init.
// Used by Validate() to enforce OBI-D-02 (the document validates against
// openbindings.schema.json).
var compiledOBISchema *jsonschema.Schema

// compiledMetaSchema is the JSON Schema 2020-12 meta-schema, compiled once at
// init from the validator library's locally embedded copy (never fetched from
// the network, per OBI-D-17's validation note). Used by Validate() to
// enforce OBI-D-17 (every schema in the document is well-formed).
var compiledMetaSchema *jsonschema.Schema

func init() {
	var doc any
	if err := json.Unmarshal(openbindingsSchemaJSON, &doc); err != nil {
		panic(fmt.Sprintf("openbindings: embedded openbindings.schema.json is not valid JSON: %v", err))
	}
	c := exactCountCompiler()
	if err := c.AddResource("openbindings:///schema", doc); err != nil {
		panic(fmt.Sprintf("openbindings: cannot register OBI schema: %v", err))
	}
	s, err := c.Compile("openbindings:///schema")
	if err != nil {
		panic(fmt.Sprintf("openbindings: cannot compile OBI schema: %v", err))
	}
	compiledOBISchema = s

	mc := exactCountCompiler()
	meta, err := mc.Compile(draft202012URI)
	if err != nil {
		panic(fmt.Sprintf("openbindings: cannot compile embedded 2020-12 meta-schema: %v", err))
	}
	compiledMetaSchema = meta
}

// validateSchemaWellFormedness records OBI-D-17 violations at one schema
// position: the value must be a JSON Schema 2020-12 schema in object or
// boolean form, and the object form must validate against the 2020-12
// meta-schemas (which cover subschemas recursively). The check is
// deliberately narrow, mirroring §5.2: unknown keywords, unparseable
// `pattern` values, and unresolvable `$ref` targets all pass — they surface
// when the schema is used, not here.
func validateSchemaWellFormedness(c *ruleChecks, prefix string, schema JSONSchema, knownValid map[string]bool) {
	switch v := schema.(type) {
	case bool:
		// Boolean schemas are always well-formed.
	case map[string]any:
		key := metaSchemaCacheKey(v)
		if key != "" && knownValid[key] {
			return
		}
		if verr := compiledMetaSchema.Validate(any(v)); verr != nil {
			for _, problem := range schemaProblems(verr) {
				c.violated("OBI-D-17", prefix+jsonPointer(problem.location...), "not a well-formed JSON Schema 2020-12 schema: "+problem.message)
			}
		} else {
			if key != "" {
				knownValid[key] = true
			}
		}
	default:
		c.violated("OBI-D-17", prefix, fmt.Sprintf("a schema is a JSON Schema 2020-12 object or boolean; got %s", jsonTypeName(v)))
	}
}

// validateAgainstOBISchema records OBI-D-02 evidence: whether the document
// validates against openbindings.schema.json. doc is a generic JSON value
// (map[string]any/[]any/scalars); without one the rule is left inconclusive.
func validateAgainstOBISchema(c *ruleChecks, doc any) {
	if doc == nil {
		c.inconclusive("OBI-D-02", "", "no generic view of the document could be produced to validate against the document schema")
		return
	}
	if verr := compiledOBISchema.Validate(doc); verr != nil {
		for _, problem := range schemaProblems(verr) {
			c.violated("OBI-D-02", jsonPointer(problem.location...), "does not validate against the document schema: "+problem.message)
		}
	}
}

func metaSchemaCacheKey(schema map[string]any) string {
	data, err := json.Marshal(schema)
	if err != nil {
		return ""
	}
	return string(data)
}

// checkExamples records OBI-D-11 evidence: every provided example value
// (including an explicit JSON null) must validate against its operation's
// corresponding schema, where that schema is specified and the schema graph
// statically reachable from it resolves entirely within the document.
//
// The rule's scope is decided per schema position. A graph that reaches an
// external resource puts that position's examples outside the rule, so they
// are neither checked nor reported (§5.1 lets a tool check them as evidence,
// never as non-conformance). A graph whose reach this validator cannot
// establish, or a schema it cannot compile, leaves the rule inconclusive there:
// neither is evidence that the examples conform.
func checkExamples(c *ruleChecks, i Interface, view func() any) {
	opKeys := make([]string, 0, len(i.Operations))
	for k, op := range i.Operations {
		if len(op.Examples) > 0 {
			opKeys = append(opKeys, k)
		}
	}
	if len(opKeys) == 0 {
		return
	}
	sort.Strings(opKeys)
	resources := embeddedSchemaResources(i)
	for _, opKey := range opKeys {
		op := i.Operations[opKey]
		exKeys := make([]string, 0, len(op.Examples))
		for ek := range op.Examples {
			exKeys = append(exKeys, ek)
		}
		sort.Strings(exKeys)
		for _, position := range []string{"input", "output"} {
			schema := op.Input
			if position == "output" {
				schema = op.Output
			}
			if schema == nil {
				continue
			}
			var provided []string
			for _, ek := range exKeys {
				ex := op.Examples[ek]
				if (position == "input" && ex.Input != nil) || (position == "output" && ex.Output != nil) {
					provided = append(provided, ek)
				}
			}
			if len(provided) == 0 {
				continue
			}
			path := jsonPointer("operations", opKey, position)
			switch schemaGraphLocality(schema, view, resources) {
			case graphReachesExternal:
				continue
			case graphUndecided:
				c.inconclusive("OBI-D-11", path, "the reach of this schema's graph could not be established, so its examples were not checked")
				continue
			}
			compiled, err := CompileOperationSchema(&i, opKey, position)
			if err != nil {
				c.inconclusive("OBI-D-11", path, fmt.Sprintf("the schema could not be compiled, so its examples were not checked: %v", err))
				continue
			}
			for _, ek := range provided {
				ex := op.Examples[ek]
				encoded := ex.Input
				if position == "output" {
					encoded = ex.Output
				}
				var value any
				if err := jsonvalue.Unmarshal(encoded, &value); err != nil {
					c.inconclusive("OBI-D-11", jsonPointer("operations", opKey, "examples", ek, position), fmt.Sprintf("the example value is not JSON, so it was not checked: %v", err))
					continue
				}
				if verr := compiled.Validate(value); verr != nil {
					examplePath := jsonPointer("operations", opKey, "examples", ek, position)
					for _, problem := range schemaProblems(verr) {
						c.violated("OBI-D-11", examplePath+jsonPointer(problem.location...), "does not validate against the operation's "+position+" schema: "+problem.message)
					}
				}
			}
		}
	}
}

type graphLocality int

const (
	graphWithinDocument graphLocality = iota
	graphUndecided
	graphReachesExternal
)

// schemaGraphLocality decides whether the schema graph statically reachable
// from root resolves entirely within the document (OBI-D-11's scope).
//
// It follows same-document JSON Pointer references at OBI positions through
// the document view, and absolute references into schema resources the
// document embeds by $id. An absolute reference to anything else reaches an
// external resource. A reference this walk cannot follow (a plain-name
// fragment, a pointer that does not resolve, a fragment into an embedded
// resource) leaves the reach undecided rather than guessed. Reaching an
// external resource dominates: such a graph is outside the rule whatever else
// it holds.
func schemaGraphLocality(root any, view func() any, resources map[string]any) graphLocality {
	result := graphWithinDocument
	visitedPointers := map[string]bool{}
	visitedResources := map[string]bool{}
	undecided := func() {
		if result == graphWithinDocument {
			result = graphUndecided
		}
	}
	var walk func(node any, base *neturl.URL)
	follow := func(ref string, base *neturl.URL) {
		if base == nil && strings.HasPrefix(ref, "#") {
			pointer := strings.TrimPrefix(ref, "#")
			if pointer != "" && !strings.HasPrefix(pointer, "/") {
				undecided()
				return
			}
			if visitedPointers[pointer] {
				return
			}
			visitedPointers[pointer] = true
			doc := view()
			if doc == nil {
				undecided()
				return
			}
			target, ok := resolveDocPointer(doc, pointer)
			if !ok {
				undecided()
				return
			}
			walk(target, nil)
			return
		}
		if base != nil && strings.HasPrefix(ref, "#") {
			// A fragment inside an embedded resource resolves within that
			// resource, whose whole subtree the walk already covers.
			return
		}
		parsed, err := neturl.Parse(ref)
		if err != nil {
			undecided()
			return
		}
		if base != nil {
			parsed = base.ResolveReference(parsed)
		}
		if !parsed.IsAbs() {
			undecided()
			return
		}
		fragment := parsed.Fragment
		parsed.Fragment = ""
		parsed.RawFragment = ""
		resourceID := parsed.String()
		resource, embedded := resources[resourceID]
		if !embedded {
			result = graphReachesExternal
			return
		}
		if fragment != "" {
			undecided()
			return
		}
		if visitedResources[resourceID] {
			return
		}
		visitedResources[resourceID] = true
		walk(resource, parsed)
	}
	walk = func(node any, base *neturl.URL) {
		if result == graphReachesExternal {
			return
		}
		object, ok := node.(map[string]any)
		if !ok {
			return
		}
		if rawID, ok := object["$id"].(string); ok {
			if parsed, err := neturl.Parse(rawID); err == nil {
				if base != nil {
					parsed = base.ResolveReference(parsed)
				}
				if parsed.IsAbs() {
					parsed.Fragment = ""
					parsed.RawFragment = ""
					base = parsed
				}
			}
		}
		if ref, ok := object["$ref"].(string); ok {
			follow(ref, base)
		}
		for keyword, child := range object {
			switch {
			case schemaMapKeywords[keyword]:
				if entries, ok := child.(map[string]any); ok {
					for _, entry := range entries {
						walk(entry, base)
					}
				}
			case singleSchemaKeywords[keyword]:
				walk(child, base)
			case arraySchemaKeywords[keyword]:
				if entries, ok := child.([]any); ok {
					for _, entry := range entries {
						walk(entry, base)
					}
				}
			}
		}
	}
	walk(root, nil)
	return result
}

// embeddedSchemaResources maps the absolute URI of every schema resource the
// document embeds by $id to that resource, nested resources included.
func embeddedSchemaResources(i Interface) map[string]any {
	resources := map[string]any{}
	var visit func(node any, base *neturl.URL)
	visit = func(node any, base *neturl.URL) {
		object, ok := node.(map[string]any)
		if !ok {
			return
		}
		if rawID, ok := object["$id"].(string); ok {
			if parsed, err := neturl.Parse(rawID); err == nil {
				if base != nil {
					parsed = base.ResolveReference(parsed)
				}
				if parsed.IsAbs() {
					parsed.Fragment = ""
					parsed.RawFragment = ""
					resources[parsed.String()] = object
					base = parsed
				}
			}
		}
		for keyword, child := range object {
			switch {
			case schemaMapKeywords[keyword]:
				if entries, ok := child.(map[string]any); ok {
					for _, entry := range entries {
						visit(entry, base)
					}
				}
			case singleSchemaKeywords[keyword]:
				visit(child, base)
			case arraySchemaKeywords[keyword]:
				if entries, ok := child.([]any); ok {
					for _, entry := range entries {
						visit(entry, base)
					}
				}
			}
		}
	}
	for _, schema := range i.Schemas {
		visit(schema, nil)
	}
	for _, operation := range i.Operations {
		visit(operation.Input, nil)
		visit(operation.Output, nil)
	}
	return resources
}

// CompileOperationSchema compiles an operation's input/output schema at its
// canonical fragment inside the complete OBI document. The document, not an
// extracted schema object, is the resolution root for same-document references
// (§7, OBI-D-16, OBI-T-16).
func CompileOperationSchema(i *Interface, operationName, position string) (*CompiledSchema, error) {
	if i == nil {
		return nil, fmt.Errorf("interface is nil")
	}
	op, ok := i.Operations[operationName]
	if !ok {
		return nil, fmt.Errorf("operation %q is not defined", operationName)
	}
	var target JSONSchema
	switch position {
	case "input":
		target = op.Input
	case "output":
		target = op.Output
	default:
		return nil, fmt.Errorf("unknown operation schema position %q", position)
	}
	if target == nil {
		return nil, fmt.Errorf("operation %q has no %s schema", operationName, position)
	}
	return compileDocumentSchema(i, "operations", operationName, position)
}

// compileDocumentSchema compiles the schema at a position of the complete OBI
// document, given as JSON Pointer reference tokens, with the document as the
// resolution root for same-document references (§7).
func compileDocumentSchema(i *Interface, tokens ...string) (*CompiledSchema, error) {
	data, err := json.Marshal(i)
	if err != nil {
		return nil, fmt.Errorf("marshal OBI document for schema compilation: %w", err)
	}
	var document map[string]any
	if err := jsonvalue.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("decode OBI document for schema compilation: %w", err)
	}
	// The OBI root is a resolution container, not a schema. Unknown fields are
	// ignored by Core and must not accidentally act as schema-control keywords
	// when the generic backend indexes the container.
	delete(document, "$id")
	delete(document, "$schema")
	delete(document, "$anchor")
	delete(document, "$dynamicAnchor")

	c := exactCountCompiler()
	const url = "openbindings:///document"
	if err := registerInterfaceSchemaResources(c, i); err != nil {
		return nil, err
	}
	if err := c.AddResource(url, document); err != nil {
		return nil, err
	}
	compiled, err := c.Compile(url + "#" + jsonPointer(tokens...))
	if err != nil {
		return nil, err
	}
	return &CompiledSchema{backend: compiled}, nil
}

// registerInterfaceSchemaResources makes absolute `$id` resources embedded at
// Core-defined schema positions visible to the generic JSON Schema compiler.
// OBI's `schemas` and `operations` fields are intentionally not JSON Schema
// keywords, so a backend cannot discover those resources merely by compiling
// the OBI document container.
func registerInterfaceSchemaResources(c *jsonschema.Compiler, i *Interface) error {
	seen := map[string]bool{}
	var visit func(any, *neturl.URL) error
	visit = func(node any, base *neturl.URL) error {
		object, ok := node.(map[string]any)
		if !ok {
			return nil
		}
		currentBase := base
		if rawID, ok := object["$id"].(string); ok {
			parsed, err := neturl.Parse(rawID)
			if err != nil {
				return fmt.Errorf("parse embedded schema $id %q: %w", rawID, err)
			}
			if base != nil {
				parsed = base.ResolveReference(parsed)
			}
			if !parsed.IsAbs() {
				return fmt.Errorf("embedded schema $id %q has no absolute base", rawID)
			}
			resourceID := parsed.String()
			if !seen[resourceID] {
				if err := c.AddResource(resourceID, object); err != nil {
					return fmt.Errorf("register embedded schema resource %q: %w", resourceID, err)
				}
				seen[resourceID] = true
			}
			// The compiler traverses this registered resource's recognized schema
			// positions and discovers any nested `$id` resources itself.
			return nil
		}

		for keyword, child := range object {
			switch {
			case schemaMapKeywords[keyword]:
				if entries, ok := child.(map[string]any); ok {
					for _, entry := range entries {
						if err := visit(entry, currentBase); err != nil {
							return err
						}
					}
				}
			case singleSchemaKeywords[keyword]:
				if err := visit(child, currentBase); err != nil {
					return err
				}
			case arraySchemaKeywords[keyword]:
				if entries, ok := child.([]any); ok {
					for _, entry := range entries {
						if err := visit(entry, currentBase); err != nil {
							return err
						}
					}
				}
			}
		}
		return nil
	}
	for _, schema := range i.Schemas {
		if err := visit(schema, nil); err != nil {
			return err
		}
	}
	for _, operation := range i.Operations {
		if err := visit(operation.Input, nil); err != nil {
			return err
		}
		if err := visit(operation.Output, nil); err != nil {
			return err
		}
	}
	return nil
}

// schemaProblem is one established schema mismatch, at the location inside
// the validated value the backend reports, as RFC 6901 reference tokens.
type schemaProblem struct {
	location []string
	message  string
}

// schemaProblems flattens a backend validation error into one problem per
// failed constraint. An anyOf or oneOf that no alternative satisfies is one
// problem at its own location, stating what each alternative lacked; a
// report of one problem per alternative would read as several defects.
func schemaProblems(err error) []schemaProblem {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return []schemaProblem{{message: err.Error()}}
	}
	return collectSchemaProblems(ve)
}

func collectSchemaProblems(ve *jsonschema.ValidationError) []schemaProblem {
	switch ve.ErrorKind.(type) {
	case *kind.AnyOf, *kind.OneOf:
		if len(ve.Causes) > 0 {
			var alternatives []string
			for _, cause := range ve.Causes {
				for _, problem := range collectSchemaProblems(cause) {
					alternatives = append(alternatives, relativeProblemText(ve.InstanceLocation, problem))
				}
			}
			return []schemaProblem{{
				location: ve.InstanceLocation,
				message:  "satisfies none of the alternatives: " + strings.Join(alternatives, "; "),
			}}
		}
	}
	if len(ve.Causes) == 0 {
		return []schemaProblem{{location: ve.InstanceLocation, message: kindString(ve.ErrorKind)}}
	}
	var out []schemaProblem
	for _, cause := range ve.Causes {
		out = append(out, collectSchemaProblems(cause)...)
	}
	return out
}

// relativeProblemText renders a problem found under base, naming its location
// relative to base when it lies deeper.
func relativeProblemText(base []string, problem schemaProblem) string {
	if len(problem.location) <= len(base) {
		return problem.message
	}
	return jsonPointer(problem.location[len(base):]...) + ": " + problem.message
}

// schemaProblemLines renders problems as "location: message" lines, with the
// location as a JSON Pointer into the validated value.
func schemaProblemLines(problems []schemaProblem) []string {
	lines := make([]string, len(problems))
	for i, problem := range problems {
		if len(problem.location) == 0 {
			lines[i] = problem.message
			continue
		}
		lines[i] = jsonPointer(problem.location...) + ": " + problem.message
	}
	return lines
}

// jsonPointer builds an RFC 6901 JSON Pointer from unescaped reference
// tokens. No tokens is the empty pointer, which addresses the whole value.
func jsonPointer(tokens ...string) string {
	var b strings.Builder
	for _, token := range tokens {
		b.WriteByte('/')
		b.WriteString(pointerTokenEscaper.Replace(token))
	}
	return b.String()
}

var pointerTokenEscaper = strings.NewReplacer("~", "~0", "/", "~1")

// ValidateAgainstSchema validates a value against a standalone JSON Schema
// 2020-12 schema, which is its own resolution root: `#` references resolve
// within the schema itself, as JSON Schema defines for a schema that is not
// embedded in another document. It suits schemas carried outside an OBI, such
// as a protocol's own schema for a value. A schema at a position of an OBI
// resolves against the whole document instead (§7); validate those with
// ValidateOperationInput, ValidateOperationOutput, or
// ValidateAgainstNamedSchema.
//
// A nil error means the value validates. A *SchemaValidationError is an
// established mismatch; a *SchemaGraphUnavailableError means the schema's
// graph could not be fully resolved, as for any reference outside the schema,
// so no verdict was reached.
func ValidateAgainstSchema(value any, schema JSONSchema) error {
	compiled, err := compileStandaloneSchema(schema)
	return validateCompiledSchema(value, compiled, err)
}

func compileStandaloneSchema(schema JSONSchema) (*jsonschema.Schema, error) {
	switch schema.(type) {
	case map[string]any, bool:
	default:
		return nil, fmt.Errorf("a schema is a JSON Schema object or boolean")
	}
	c := exactCountCompiler()
	const url = "openbindings:///schema"
	if err := c.AddResource(url, schema); err != nil {
		return nil, err
	}
	return c.Compile(url)
}

// ValidateAgainstNamedSchema validates a value against the entry name in the
// document's schemas map, with the complete OBI document as the resolution
// root for same-document references (§7).
func ValidateAgainstNamedSchema(value any, iface *Interface, name string) error {
	if iface == nil {
		return validateCompiledSchema(value, nil, fmt.Errorf("interface is nil"))
	}
	if _, ok := iface.Schemas[name]; !ok {
		return validateCompiledSchema(value, nil, fmt.Errorf("schema %q is not defined", name))
	}
	compiled, err := compileDocumentSchema(iface, "schemas", name)
	return validateCompiledSchema(value, compiled, err)
}

// ValidateOperationInput validates a value against an operation input schema
// with the complete OBI document retained as the same-document `$ref` root.
func ValidateOperationInput(value any, iface *Interface, operationName string) error {
	compiled, err := CompileOperationSchema(iface, operationName, "input")
	return validateCompiledSchema(value, compiled, err)
}

// ValidateOperationOutput validates a value against an operation output schema
// with the complete OBI document retained as the same-document `$ref` root.
func ValidateOperationOutput(value any, iface *Interface, operationName string) error {
	compiled, err := CompileOperationSchema(iface, operationName, "output")
	return validateCompiledSchema(value, compiled, err)
}

func validateCompiledSchema(value any, compiled interface{ Validate(any) error }, err error) error {
	if err != nil {
		return &SchemaGraphUnavailableError{Cause: err}
	}
	if verr := compiled.Validate(value); verr != nil {
		return projectSchemaValidationError(verr)
	}
	return nil
}

// SchemaGraphUnavailableError reports that a value verdict could not be
// reached because the governing operation schema's complete statically
// reachable graph was not available, well-formed, and evaluable. It is
// distinct from an instance mismatch, as required by OBI-T-16.
//
// Callers can use errors.As rather than parsing diagnostic text. Cause remains
// available through errors.Unwrap for validator-specific diagnostics.
type SchemaGraphUnavailableError struct {
	Cause error
}

func (e *SchemaGraphUnavailableError) Error() string {
	if e == nil || e.Cause == nil {
		return "openbindings: schema compilation failed"
	}
	return fmt.Sprintf("openbindings: schema compilation failed: %v", e.Cause)
}

func (e *SchemaGraphUnavailableError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// SchemaValidationError is an established instance mismatch. Use errors.As to
// distinguish it from a capability refusal or unavailable schema graph without
// depending on the private backend. It renders readable per-leaf diagnostics.
type SchemaValidationError struct {
	lines []string
	cause error
}

func (e *SchemaValidationError) Error() string { return strings.Join(e.lines, "; ") }
func (e *SchemaValidationError) Unwrap() error { return e.cause }

func projectSchemaValidationError(err error) error {
	var projected *SchemaValidationError
	if errors.As(err, &projected) {
		return err
	}
	var mismatch *jsonschema.ValidationError
	if errors.As(err, &mismatch) {
		return &SchemaValidationError{lines: schemaProblemLines(schemaProblems(mismatch)), cause: err}
	}
	return err
}
