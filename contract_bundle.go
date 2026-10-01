package openbindings

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/openbindings/openbindings-go/internal/jsonpointer"
	"github.com/openbindings/openbindings-go/internal/schemacompiler"
)

// schemaDepthLimit bounds how deeply a copied schema nests subschemas (see
// schemaDepth). Checking a schema against the meta-schemas grows faster than
// linearly with that depth, and schemas never nest near this deep, so a
// deeper one is core's own limit met (§10.4), not evidence about the schema.
const schemaDepthLimit = 256

// bundleSpelling chooses the spellings of what core generates in a bundle:
// its namespace, identifiers, and $defs keys. None is part of the evaluator
// contract, and the conformance kit varies them so that an evaluator
// depending on one fails.
type bundleSpelling struct{ variant int }

// unitKey is what a bundle copies whole: an OBI position, or a supplied
// resource or meta-schema ("").
type unitKey struct {
	doc      *schemaDoc
	location string
}

// unitOf returns the copied unit holding a schema: in the OBI document, the
// OBI position its location begins with, which is its first two reference
// tokens under schemas and its first three under operations.
func unitOf(key schemaKey) unitKey {
	if key.doc.kind != obiDocument {
		return unitKey{key.doc, ""}
	}
	tokens := 3
	if strings.HasPrefix(key.location, "/schemas/") {
		tokens = 2
	}
	end := 0
	for range tokens {
		next := strings.IndexByte(key.location[end+1:], '/')
		if next < 0 {
			return unitKey(key)
		}
		end += 1 + next
	}
	return unitKey{key.doc, key.location[:end]}
}

// unitProblem is what a copied unit holds that refuses any contract copying
// it: always, or, for a problem the specification leaves undefined, as an
// undefined result when evaluation reaches the schema it lies in and as
// core's conservative policy when it only copies it.
type unitProblem struct {
	located
	// schema is the schema the problem lies in.
	schema string
	always bool
}

// unitProblems returns, once per unit, what it holds that refuses a contract
// copying it: nesting past core's limit; a schema invalid against the 2020-12
// meta-schemas; a pattern that is not ECMA-262 with the u flag; a resource
// whose dialect is other than 2020-12; a resource whose $id gives it no
// identifier; and a plain name one resource of it declares more than once.
func (s *schemaSpace) unitProblems(unit unitKey) []unitProblem {
	type memoKey struct {
		doc      *schemaDoc
		location string
	}
	if cached, ok := s.checked.Load(memoKey(unit)); ok {
		return cached.([]unitProblem)
	}
	problems := checkUnit(unit)
	s.checked.Store(memoKey(unit), problems)
	return problems
}

func checkUnit(unit unitKey) []unitProblem {
	d, held := unit.doc, unit.doc.units[unit.location]
	value, _ := jsonpointer.Resolve(d.value, unit.location)
	problem := func(kind failureKind, schema, location, reason string, always bool) unitProblem {
		return unitProblem{located{failure{kind, reason}, locationOf(d, location)}, schema, always}
	}
	if held.deep {
		return []unitProblem{problem(missingCapability, unit.location, unit.location, fmt.Sprintf("it nests subschemas deeper than %d levels, a limit of this SDK", schemaDepthLimit), true)}
	}
	var out []unitProblem
	if d.kind != metaDocument {
		problems, err := checkAgainstMetaSchema(value)
		if err != nil {
			out = append(out, problem(missingCapability, unit.location, unit.location, fmt.Sprintf("it could not be checked against the 2020-12 meta-schemas: %v", err), true))
		}
		for _, p := range problems {
			at := unit.location + jsonpointer.Format(p.Location...)
			out = append(out, problem(undefinedResult, schemaHolding(d, at), at, "it is not a well-formed JSON Schema 2020-12 schema: "+p.Message, false))
		}
	}
	for _, location := range held.schemas {
		object, _ := mustResolve(d.value, location).(map[string]any)
		patterns := sortedKeys(asObject(object["patternProperties"]))
		if pattern, ok := object["pattern"].(string); ok {
			patterns = append(patterns, pattern)
		}
		for _, pattern := range patterns {
			switch err := schemacompiler.CheckPattern(pattern); {
			case errors.Is(err, schemacompiler.ErrPatternNesting):
				out = append(out, problem(missingCapability, location, location, fmt.Sprintf("its pattern %q meets %v", pattern, err), true))
			case err != nil:
				out = append(out, problem(undefinedResult, location, location, fmt.Sprintf("its pattern %q is not an ECMA-262 regular expression with the u flag (OBI-T-08): %v", pattern, err), false))
			}
		}
	}
	for _, r := range held.resources {
		if r.idProblem != "" {
			out = append(out, problem(r.idKind, r.location, r.location, r.idProblem, r.idKind != undefinedResult))
		}
		// Dialects go by resource (§5.2, JSON Schema Core §9.3.2): only the
		// $schema at a resource's root declares one, and a resource without
		// one takes its enclosing resource's, which lies in the same unit
		// unless it is the document resource, whose dialect is 2020-12. A
		// supplied document's root without one is read as 2020-12, the
		// choice JSON Schema leaves to the implementation (§5.2). So a root
		// naming another dialect is the only way a unit holds one.
		root, _ := mustResolve(d.value, r.location).(map[string]any)
		if dialect, present := root["$schema"]; present && dialect != draft202012URI && dialect != draft202012URI+"#" {
			out = append(out, problem(missingCapability, r.location, r.location, fmt.Sprintf("it declares the dialect %s, which this SDK does not evaluate, for its resource and every resource inheriting it (§5.2)", describeJSON(dialect)), true))
		}
	}
	type named struct {
		resource *docResource
		name     string
	}
	declared := map[named][]string{}
	var names []named
	for _, a := range held.anchors {
		key := named{a.resource, a.name}
		if declared[key] == nil {
			names = append(names, key)
		}
		declared[key] = append(declared[key], a.location)
	}
	for _, key := range names {
		if within := declared[key]; len(within) > 1 {
			out = append(out, problem(conservativePolicy, within[0], within[1], fmt.Sprintf("%s declares the plain name %q more than once", describeResource(key.resource), key.name), true))
		}
	}
	return out
}

// schemaHolding returns the innermost schema at or above a location.
func schemaHolding(d *schemaDoc, location string) string {
	for {
		if _, ok := d.schemas[location]; ok || location == "" {
			return location
		}
		location = location[:strings.LastIndexByte(location, '/')]
	}
}

// bundle builds the bundle core hands the evaluator for the value contract at
// entry, or the refusal every value validated against it gets.
func (s *schemaSpace) bundle(entry string, spelling bundleSpelling) (json.RawMessage, *NoVerdictError) {
	r := s.reach(entry)
	units := r.copiedUnits()
	refusals := slices.Clone(r.failures)
	for _, unit := range units {
		for _, p := range s.unitProblems(unit) {
			switch {
			case p.always || p.kind != undefinedResult:
				refusals = append(refusals, p.located)
			case r.reached[schemaKey{unit.doc, p.schema}]:
				refusals = append(refusals, p.located)
			default:
				p.kind, p.reason = conservativePolicy, p.reason+", in a schema the contract copies though evaluation does not reach it"
				refusals = append(refusals, p.located)
			}
		}
	}
	refusals = append(refusals, collisions(units)...)
	if len(refusals) > 0 {
		first := slices.MinFunc(refusals, func(a, b located) int {
			return cmp.Or(cmp.Compare(a.kind, b.kind), cmp.Compare(a.location, b.location), cmp.Compare(a.reason, b.reason))
		})
		cause := errors.New(first.reason)
		if first.kind == undefinedResult {
			cause = &coreReason{sentinel: ErrUndefined, detail: first.reason}
		}
		return nil, &NoVerdictError{Location: first.location, Cause: cause}
	}
	w := newBundleWriter(s, r, units, spelling)
	return w.write(entry), nil
}

// copiedUnits returns the units holding a reached schema, in order.
func (r *contractReach) copiedUnits() []unitKey {
	set := map[unitKey]bool{}
	for key := range r.reached {
		set[unitOf(key)] = true
	}
	return slices.SortedFunc(maps.Keys(set), func(a, b unitKey) int {
		return cmp.Or(compareDocs(a.doc, b.doc), strings.Compare(a.location, b.location))
	})
}

// collisions refuses two copied resources that share a name in normal form
// (core's conservative policy; OBI-D-13 compares characters).
func collisions(units []unitKey) []located {
	carrier := map[string]*docResource{}
	var out []located
	for _, unit := range units {
		for _, r := range unit.doc.units[unit.location].resources {
			for _, name := range r.names {
				normal := normalURI(name)
				if other, taken := carrier[normal]; taken && other != r {
					out = append(out, located{failure{conservativePolicy, fmt.Sprintf("it and %s both carry the name %s in normal form", describeResource(other), normal)}, locationOf(r.doc, r.location)})
					continue
				}
				carrier[normal] = r
			}
		}
	}
	return out
}

// bundleWriter writes a value contract's bundle: a JSON Schema 2020-12
// compound document whose root declares an absolute $id in a namespace under
// .invalid no identifier or reference of the space uses, $refs the entry, and
// holds every copied unit in $defs. Every resource root carries an absolute
// $id in normal form (an OBI position of the document resource one core
// generates), every $ref and $dynamicRef names its target canonically by
// the $id of the resource holding it, a reference to what the bundle does not
// hold as a schema names a false placeholder, the legacy definitions and
// schema values of dependencies move into their resource's $defs, and the
// root declares a scope wrapper for each $dynamicAnchor of the document
// resource a reached $dynamicRef can look up.
type bundleWriter struct {
	space    *schemaSpace
	reach    *contractReach
	units    []unitKey
	spelling bundleSpelling
	root     string
	// unitKeys holds each unit's $defs key in the root.
	unitKeys map[unitKey]string
	// ids holds the identifier core generates for each OBI position of the
	// document resource that roots an object schema.
	ids map[unitKey]string
	// addresses holds where the bundle holds each copied schema: the
	// identifier of the resource it lies in (or the root's, for a boolean
	// unit), and the reference tokens from that resource's root.
	addresses map[schemaKey]bundleAddress
	// moved holds the $defs key a legacy schema moves to, and defsKeys the
	// keys each resource root's $defs holds, moved ones included.
	moved    map[schemaKey]string
	defsKeys map[schemaKey]map[string]bool
	// movedOut holds, per resource root, the moved schemas written for its
	// $defs.
	movedOut map[schemaKey]map[string]any
	// placeholder and wrapperKeys are the root's keys for the placeholder
	// and the scope wrappers; usesPlaceholder whether a reference names it.
	placeholder     string
	wrapperKeys     map[string]string
	usesPlaceholder bool
}

type bundleAddress struct {
	base   string
	tokens []string
}

func (a bundleAddress) String() string {
	if len(a.tokens) == 0 {
		return a.base
	}
	return a.base + "#" + fragmentPointer(a.tokens)
}

func newBundleWriter(s *schemaSpace, r *contractReach, units []unitKey, spelling bundleSpelling) *bundleWriter {
	w := &bundleWriter{
		space: s, reach: r, units: units, spelling: spelling,
		unitKeys: map[unitKey]string{}, ids: map[unitKey]string{},
		addresses: map[schemaKey]bundleAddress{}, moved: map[schemaKey]string{}, defsKeys: map[schemaKey]map[string]bool{},
		movedOut:    map[schemaKey]map[string]any{},
		wrapperKeys: map[string]string{},
	}
	w.root = w.namespace()
	for i, unit := range units {
		w.unitKeys[unit] = w.generated("unit", i)
	}
	w.placeholder = w.generated("placeholder", 0)
	for i, name := range slices.Sorted(maps.Keys(r.wrappers)) {
		w.wrapperKeys[name] = w.generated("wrapper", i)
	}
	for _, unit := range units {
		w.plan(unit)
	}
	return w
}

// namespace returns the root's $id: under a host in .invalid (RFC 2606) no
// identifier or reference of the OBI document or a supplied resource
// mentions, compared in normal form. It depends only on the space and the
// spelling, so each space chooses it once per spelling.
func (w *bundleWriter) namespace() string {
	if chosen, ok := w.space.namespaces.Load(w.spelling); ok {
		return chosen.(string)
	}
	chosen := w.chooseNamespace()
	w.space.namespaces.Store(w.spelling, chosen)
	return chosen
}

func (w *bundleWriter) chooseNamespace() string {
	var words []string
	for _, d := range append([]*schemaDoc{w.space.obi}, w.space.supplied.documents()...) {
		words = append(words, d.words...)
		words = append(words, spelledForNamespace(d.uri))
	}
	for k := 0; ; k++ {
		host := fmt.Sprintf("bundle-%d.openbindings.invalid", k)
		if w.spelling.variant == 1 {
			host = fmt.Sprintf("x%d.contract.invalid", k)
		}
		used := slices.ContainsFunc(words, func(word string) bool { return strings.Contains(word, host) })
		if !used {
			if w.spelling.variant == 1 {
				return "https://" + host + "/root/"
			}
			return "https://" + host + "/"
		}
	}
}

// spelledForNamespace spells an identifier, reference, or URI as the
// namespace choice compares it: lowercased, with percent-encoded unreserved
// characters decoded, so every spelling whose normal form names a host
// shows that host literally (a generated host is all unreserved characters).
func spelledForNamespace(word string) string {
	return strings.ToLower(normalPercents(word))
}

// generated returns a name core generates, spelled as the bundle's spelling
// says.
func (w *bundleWriter) generated(kind string, i int) string {
	if w.spelling.variant == 1 {
		return fmt.Sprintf("%d_%s", i, strings.ToUpper(kind[:1]))
	}
	return fmt.Sprintf("%s%d", kind, i)
}

// plan records where the bundle holds each schema of a unit.
func (w *bundleWriter) plan(unit unitKey) {
	d := unit.doc
	value, _ := jsonpointer.Resolve(d.value, unit.location)
	root := schemaKey{d, unit.location}
	switch v := value.(type) {
	case bool:
		w.addresses[root] = bundleAddress{w.root, []string{"$defs", w.unitKeys[unit]}}
		return
	case map[string]any:
		resource := d.schemas[unit.location]
		base := ""
		switch {
		case resource.document:
			base = w.root + w.unitKeys[unit]
			w.ids[unit] = base
		default:
			base = normalURI(resource.id)
		}
		w.planSchema(v, root, bundleAddress{base, nil}, root)
	}
}

// planSchema records the address of a schema and what it holds. resourceRoot
// is the root of the bundle resource it lies in, whose $defs receives the
// legacy schemas moved out of it.
func (w *bundleWriter) planSchema(value any, at schemaKey, address bundleAddress, resourceRoot schemaKey) {
	object, isObject := value.(map[string]any)
	if isObject && at != resourceRoot && at.doc.schemas[at.location].location == at.location {
		resource := at.doc.schemas[at.location]
		address, resourceRoot = bundleAddress{normalURI(resource.id), nil}, at
	}
	w.addresses[at] = address
	if !isObject {
		return
	}
	if at == resourceRoot {
		w.defsKeys[at] = map[string]bool{}
		for name := range asObject(object["$defs"]) {
			w.defsKeys[at][name] = true
		}
	}
	forEachDescribedSubschema(object, func(child any, tokens ...string) {
		key := schemaKey{at.doc, at.location + jsonpointer.Format(tokens...)}
		if _, isSchema := at.doc.schemas[key.location]; !isSchema {
			return
		}
		childAddress := bundleAddress{address.base, append(slices.Clip(address.tokens), tokens...)}
		if tokens[0] == "definitions" || tokens[0] == "dependencies" {
			moved := w.freshDefsKey(resourceRoot)
			w.moved[key] = moved
			childAddress = bundleAddress{w.addresses[resourceRoot].base, append(slices.Clip(w.addresses[resourceRoot].tokens), "$defs", moved)}
		}
		w.planSchema(child, key, childAddress, resourceRoot)
	})
}

// freshDefsKey returns a $defs key for a moved legacy schema that the
// resource's $defs does not hold.
func (w *bundleWriter) freshDefsKey(resourceRoot schemaKey) string {
	taken := w.defsKeys[resourceRoot]
	for i := 0; ; i++ {
		key := w.generated("moved", i)
		if !taken[key] {
			taken[key] = true
			return key
		}
	}
}

// write writes the bundle.
func (w *bundleWriter) write(entry string) json.RawMessage {
	defs := map[string]any{}
	for _, unit := range w.units {
		value, _ := jsonpointer.Resolve(unit.doc.value, unit.location)
		root := schemaKey(unit)
		written := w.writeSchema(value, root, root)
		if object, ok := written.(map[string]any); ok && w.ids[unit] != "" {
			object["$id"] = w.ids[unit]
		}
		defs[w.unitKeys[unit]] = written
	}
	for name, declared := range w.reach.wrappers {
		defs[w.wrapperKeys[name]] = map[string]any{
			"$dynamicAnchor": name,
			"$ref":           w.addresses[schemaKey{w.space.obi, declared}].String(),
		}
	}
	document := map[string]any{
		"$schema": draft202012URI,
		"$id":     w.root,
		"$ref":    w.addresses[schemaKey{w.space.obi, entry}].String(),
	}
	if w.usesPlaceholder {
		defs[w.placeholder] = false
	}
	document["$defs"] = defs
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(document); err != nil {
		panic(fmt.Sprintf("openbindings: a bundle does not encode: %v", err))
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n"))
}

// writeSchema writes a copied schema: identities and references canonical,
// legacy positions moved, a $schema that declares no dialect left out, and
// everything else as written.
func (w *bundleWriter) writeSchema(value any, at, resourceRoot schemaKey) any {
	object, isObject := value.(map[string]any)
	if !isObject {
		return value
	}
	resource := at.doc.schemas[at.location]
	if at != resourceRoot && resource.location == at.location {
		resourceRoot = at
	}
	out := make(map[string]any, len(object))
	var movedHere map[string]any
	moveOut := func(child any, tokens ...string) {
		key := schemaKey{at.doc, at.location + jsonpointer.Format(tokens...)}
		w.movedInto(resourceRoot)[w.moved[key]] = w.writeSchema(child, key, resourceRoot)
	}
	for keyword, member := range object {
		switch {
		case keyword == "$id" && at == resourceRoot && !resource.document:
			out[keyword] = normalURI(resource.id)
		case keyword == "$schema" && (at != resourceRoot || resource.document):
			// A $schema in the document resource, or below a resource's
			// root, declares no dialect (§5.2). Core gives each OBI position
			// of the document resource an $id, which would make one at such
			// a position declare its unit's dialect, so it writes none.
		case (keyword == "$ref" || keyword == "$dynamicRef") && isString(member):
			out[keyword] = w.reference(member.(string), resource, keyword)
		case keyword == "definitions":
			for _, name := range sortedKeys(asObject(member)) {
				moveOut(asObject(member)[name], keyword, name)
			}
		case keyword == "dependencies":
			kept := map[string]any{}
			for name, entry := range asObject(member) {
				if _, isSchema := at.doc.schemas[at.location+jsonpointer.Format(keyword, name)]; isSchema {
					moveOut(entry, keyword, name)
				} else {
					kept[name] = cloneJSON(entry)
				}
			}
			if len(kept) > 0 {
				out[keyword] = kept
			}
		case schemaMapKeywords[keyword]:
			entries := asObject(member)
			written := make(map[string]any, len(entries))
			for name, entry := range entries {
				written[name] = w.writeSchema(entry, schemaKey{at.doc, at.location + jsonpointer.Format(keyword, name)}, resourceRoot)
			}
			if keyword == "$defs" && at == resourceRoot {
				movedHere = written
			}
			out[keyword] = written
		case arraySchemaKeywords[keyword]:
			entries, _ := member.([]any)
			written := make([]any, len(entries))
			for i, entry := range entries {
				written[i] = w.writeSchema(entry, schemaKey{at.doc, at.location + jsonpointer.Format(keyword, fmt.Sprint(i))}, resourceRoot)
			}
			out[keyword] = written
		case singleSchemaKeywords[keyword]:
			out[keyword] = w.writeSchema(member, schemaKey{at.doc, at.location + "/" + keyword}, resourceRoot)
		default:
			out[keyword] = cloneJSON(member)
		}
	}
	if at == resourceRoot && !resource.document && object["$id"] == nil {
		// A supplied resource's root without $id carries its URI.
		out["$id"] = normalURI(resource.id)
	}
	if at == resourceRoot {
		if moved := w.movedOut[at]; len(moved) > 0 {
			if movedHere == nil {
				movedHere = map[string]any{}
				out["$defs"] = movedHere
			}
			maps.Copy(movedHere, moved)
		}
	}
	return out
}

// movedInto returns the moved schemas written for a resource root's $defs.
func (w *bundleWriter) movedInto(resourceRoot schemaKey) map[string]any {
	if w.movedOut[resourceRoot] == nil {
		w.movedOut[resourceRoot] = map[string]any{}
	}
	return w.movedOut[resourceRoot]
}

// reference writes a reference canonically: to a resource's root, its $id;
// within a resource, its $id and a JSON Pointer; to a plain name, its
// resource's $id and the name; and to what the bundle does not hold as a
// schema, the placeholder.
func (w *bundleWriter) reference(ref string, holder *docResource, keyword string) string {
	target, f := w.space.resolve(ref, holder)
	address, held := w.addresses[schemaKey{target.doc, target.location}]
	if f != nil || !held {
		w.usesPlaceholder = true
		return bundleAddress{w.root, []string{"$defs", w.placeholder}}.String()
	}
	if target.name != "" {
		return address.base + "#" + target.name
	}
	return address.String()
}

func isString(value any) bool {
	_, ok := value.(string)
	return ok
}

func asObject(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

// mustResolve returns the value at a location known to exist.
func mustResolve(view any, location string) any {
	value, _ := jsonpointer.Resolve(view, location)
	return value
}
