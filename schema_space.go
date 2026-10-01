package openbindings

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/openbindings/openbindings-go/internal/jsonpointer"
)

// A value contract's schemas live in one schema space: the OBI document's
// schemas (§7), the schema documents the application supplies, and the JSON
// Schema 2020-12 meta-schemas core embeds. References resolve across the
// space by the identifiers its resources carry, matched character for
// character (§7.4, OBI-D-13); normal form (normalURI) is only for writing and
// for refusing collisions.

// docKind is where a schema document comes from.
type docKind int

const (
	obiDocument docKind = iota
	suppliedDocument
	metaDocument
)

// schemaDoc is a JSON document holding schemas: the OBI document, a supplied
// resource, or an embedded meta-schema.
type schemaDoc struct {
	kind docKind
	// uri is the retrieval URI of a supplied resource or a meta-schema, with
	// an empty fragment removed; "" for the OBI document.
	uri   string
	value any
	// schemas maps the location of every schema the document holds (its
	// schema positions, the legacy definitions and schema values of
	// dependencies included) to the resource it lies in, down to the depth
	// limit (docUnit.deep).
	schemas map[string]*docResource
	// resources are the document's schema resources in walk order; the OBI
	// document's first is the document resource.
	resources []*docResource
	// units maps the location of each unit a bundle copies whole to what it
	// holds: the OBI positions (an operation's input or output, an entry of
	// schemas) of the OBI document, and "" for any other document.
	units map[string]*docUnit
	// words holds every $id, $ref, and $dynamicRef string the document's
	// schemas hold, spelled for the namespace choice (spelledForNamespace),
	// which a bundle's namespace must avoid.
	words []string
}

// docResource is a schema resource: a schema that declares $id, the root of
// a supplied resource or meta-schema, or the OBI document resource (§7.2).
type docResource struct {
	doc      *schemaDoc
	location string
	// document marks the OBI document resource: every schema at an OBI
	// position that no $id encloses. Its base is unique to the document and
	// named nowhere (§7.2), so it has no id.
	document bool
	// id is the identifier the resource carries, resolved character for
	// character against its base, without fragment; "" when it has none.
	// idProblem says why a declared $id gives it none, and idKind how that
	// refuses a contract copying it.
	id        string
	idProblem string
	idKind    failureKind
	// names are the identifiers references name the resource by: its id and,
	// for a supplied resource's root, its URI.
	names []string
	// anchors maps each plain name the resource declares to every schema
	// declaring it, by $anchor or $dynamicAnchor, once per declaration;
	// dynamicAnchors maps the names declared by $dynamicAnchor.
	anchors, dynamicAnchors map[string][]string
}

// docUnit is what one unit holds, recorded as the walk passes it, so checking
// a unit reads only what lies in it.
type docUnit struct {
	// schemas are the locations of the schemas it holds, and resources the
	// resources declared within it, in walk order.
	schemas   []string
	resources []*docResource
	// anchors are its schemas' plain-name declarations, once per $anchor and
	// per $dynamicAnchor, in walk order.
	anchors []declaredAnchor
	// deep marks a unit nesting subschemas deeper than schemaDepthLimit.
	// What lies below the limit is not indexed, and a contract copying the
	// unit is refused (checkUnit).
	deep bool
}

// declaredAnchor is one declaration of a plain name: the resource it names a
// schema in, and the schema.
type declaredAnchor struct {
	resource       *docResource
	name, location string
}

// indexDocument walks a schema document. A supplied resource's or
// meta-schema's root is a resource named by its URI and its own $id.
func indexDocument(kind docKind, uri string, value any) *schemaDoc {
	d := &schemaDoc{kind: kind, uri: uri, value: value, schemas: map[string]*docResource{}, units: map[string]*docUnit{}}
	if kind == obiDocument {
		document := d.newResource("")
		document.document = true
		root, _ := value.(map[string]any)
		var units []string
		schemas, _ := root["schemas"].(map[string]any)
		for _, key := range sortedKeys(schemas) {
			units = append(units, jsonpointer.Format("schemas", key))
		}
		operations, _ := root["operations"].(map[string]any)
		for _, key := range sortedKeys(operations) {
			operation, _ := operations[key].(map[string]any)
			for _, direction := range []string{"input", "output"} {
				if _, present := operation[direction]; present {
					units = append(units, jsonpointer.Format("operations", key, direction))
				}
			}
		}
		for _, location := range units {
			unit := &docUnit{}
			d.units[location] = unit
			value, _ := jsonpointer.Resolve(value, location)
			d.walk(value, location, document, unit, 0)
		}
		return d
	}
	unit := &docUnit{}
	d.units[""] = unit
	root := d.newResource("")
	unit.resources = append(unit.resources, root)
	root.id, root.names = uri, []string{uri}
	if object, ok := value.(map[string]any); ok {
		if raw, present := object["$id"]; present {
			root.declare(raw, uri, false)
			if root.id == "" {
				root.id = uri
			} else if root.id != uri {
				root.names = []string{root.id, uri}
			}
		}
	}
	d.walk(value, "", root, unit, 0)
	return d
}

func (d *schemaDoc) newResource(location string) *docResource {
	r := &docResource{doc: d, location: location, anchors: map[string][]string{}, dynamicAnchors: map[string][]string{}}
	d.resources = append(d.resources, r)
	return r
}

// walk records the schemas a schema holds, and the resources and names they
// declare, in the unit holding it. depth is how deeply the unit nests the
// schema, counted as schemaDepth counts: past schemaDepthLimit, the walk
// marks the unit deep and records nothing more, so indexing a unit takes
// space linear in it however deeply it nests.
func (d *schemaDoc) walk(value any, location string, resource *docResource, unit *docUnit, depth int) {
	if depth > schemaDepthLimit {
		unit.deep = true
		return
	}
	switch v := value.(type) {
	case bool:
		d.schemas[location] = resource
		unit.schemas = append(unit.schemas, location)
	case map[string]any:
		if raw, present := v["$id"]; present && !(d.kind != obiDocument && location == "") {
			parent := resource
			resource = d.newResource(location)
			resource.declare(raw, parent.id, parent.document)
			if resource.id != "" {
				resource.names = []string{resource.id}
			}
			unit.resources = append(unit.resources, resource)
		}
		d.schemas[location] = resource
		unit.schemas = append(unit.schemas, location)
		for _, keyword := range []string{"$anchor", "$dynamicAnchor"} {
			if name, ok := v[keyword].(string); ok {
				resource.anchors[name] = append(resource.anchors[name], location)
				unit.anchors = append(unit.anchors, declaredAnchor{resource, name, location})
				if keyword == "$dynamicAnchor" {
					resource.dynamicAnchors[name] = append(resource.dynamicAnchors[name], location)
				}
			}
		}
		for _, keyword := range []string{"$id", "$ref", "$dynamicRef"} {
			if word, ok := v[keyword].(string); ok {
				d.words = append(d.words, spelledForNamespace(word))
			}
		}
		forEachDescribedSubschema(v, func(child any, tokens ...string) {
			d.walk(child, location+jsonpointer.Format(tokens...), resource, unit, depth+1)
		})
	}
}

// declare gives a resource the identifier its $id declares against base, or
// records why it gives none. atPosition marks a resource at an OBI position,
// whose base is the document's own, named nowhere (§7.2).
func (r *docResource) declare(raw any, base string, atPosition bool) {
	id, isString := raw.(string)
	wellFormed, absolute := uriReference(id)
	trimmed := strings.TrimSuffix(id, "#")
	switch {
	case !isString:
		r.idProblem, r.idKind = "its $id is not a string", undefinedResult
	case !wellFormed:
		r.idProblem, r.idKind = fmt.Sprintf("its $id %q is not a URI-reference (RFC 3986 §4.1)", id), undefinedResult
	case strings.Contains(trimmed, "#"):
		r.idProblem, r.idKind = fmt.Sprintf("its $id %q carries a fragment", id), undefinedResult
	case trimmed == "":
		// The $id resolves to its base, a URI what encloses it already has
		// (JSON Schema Core §8.2.1).
		r.idProblem, r.idKind = fmt.Sprintf("its $id %q gives it no URI of its own", id), undefinedResult
	case !absolute && atPosition:
		// Its resolved $id would depend on the document's base, which §7.2
		// makes unique to the document without naming it; OBI-D-05 already
		// makes such a document non-conformant.
		r.idProblem, r.idKind = fmt.Sprintf("its $id %q is relative, at an OBI position, whose base the document names nowhere (§7.2, OBI-D-05)", id), conservativePolicy
	case !absolute && base == "":
		r.idProblem, r.idKind = fmt.Sprintf("its $id %q is relative, and what encloses it has no URI to resolve it against", id), conservativePolicy
	default:
		r.id, _ = resolveExact(trimmed, base)
	}
}

// failureKind is how a refusal labels a value contract's no-verdict.
type failureKind int

const (
	// undefinedResult: the specification leaves the result undefined
	// (OBI-T-08), so the refusal matches ErrUndefined.
	undefinedResult failureKind = iota
	// missingCapability: core lacks a capability or meets its own limit.
	missingCapability
	// conservativePolicy: core's conservative policy.
	conservativePolicy
)

// failure is why a reference, or a copied schema, refuses a contract.
type failure struct {
	kind   failureKind
	reason string
}

// schemaTarget is the schema a reference names.
type schemaTarget struct {
	doc      *schemaDoc
	location string
	// name is the plain name the reference used, or "".
	name string
}

// schemaSpace is the OBI document's schemas and the supplied resources, with
// the meta-schemas core embeds.
type schemaSpace struct {
	obi *schemaDoc
	// document is what OBI-D-12 looks same-document references in the
	// document resource up in.
	document documentSchemas
	// supplied is nil in a space no application supplies resources to
	// (Document.References).
	supplied *suppliedResources
	// byName maps each exact name to the resources carrying it, and byNormal
	// each normal-form name, across the OBI document and the supplied
	// resources.
	byName, byNormal map[string][]*docResource
	// checked memoizes what each copied unit holds that can refuse a
	// contract (unitProblems), keyed by unitKey, and namespaces each bundle
	// spelling's namespace (bundleWriter.namespace).
	checked, namespaces sync.Map
}

func newSchemaSpace(view any, supplied *suppliedResources) *schemaSpace {
	s := &schemaSpace{obi: indexDocument(obiDocument, "", view), document: collectDocumentSchemas(view), supplied: supplied, byName: map[string][]*docResource{}, byNormal: map[string][]*docResource{}}
	for _, d := range append([]*schemaDoc{s.obi}, supplied.documents()...) {
		for _, r := range d.resources {
			for _, name := range r.names {
				s.byName[name] = append(s.byName[name], r)
				normal := normalURI(name)
				if !slices.Contains(s.byNormal[normal], r) {
					s.byNormal[normal] = append(s.byNormal[normal], r)
				}
			}
		}
	}
	return s
}

// resolve resolves a reference a schema in holder holds, as §7 and JSON
// Schema 2020-12 resolve it (OBI-T-06). In the document resource, a
// same-document reference is looked up as OBI-D-12 looks it up (§7.2); any
// other reference resolves against its resource's identifier and names the
// resource carrying the result character for character, or a meta-schema
// core embeds when nothing in the space carries it.
func (s *schemaSpace) resolve(ref string, holder *docResource) (schemaTarget, *failure) {
	if wellFormed, _ := uriReference(ref); !wellFormed {
		return schemaTarget{}, &failure{undefinedResult, fmt.Sprintf("%q is not a URI-reference (RFC 3986 §4.1), which JSON Schema leaves undefined", ref)}
	}
	_, absolute := uriReference(ref)
	if holder.document && !absolute {
		if ref != "" && !strings.HasPrefix(ref, "#") {
			return schemaTarget{}, &failure{undefinedResult, fmt.Sprintf("%q is relative, in the document resource, whose base the document names nowhere (§7.2, OBI-D-05)", ref)}
		}
		return s.resolveInDocument(ref)
	}
	if !absolute && holder.id == "" {
		return schemaTarget{}, &failure{conservativePolicy, fmt.Sprintf("%q is relative, and its resource has no URI to resolve it against", ref)}
	}
	name, raw := resolveExact(ref, holder.id)
	fragment, ok := decodeFragment(raw)
	if !ok {
		return schemaTarget{}, &failure{undefinedResult, fmt.Sprintf("%q has a fragment that does not decode to UTF-8", ref)}
	}
	carriers := s.byName[name]
	switch {
	case len(carriers) > 1 || len(s.byNormal[normalURI(name)]) > 1:
		return schemaTarget{}, &failure{conservativePolicy, fmt.Sprintf("%q names %s, which more than one resource carries in normal form", ref, name)}
	case len(carriers) == 1:
		return s.resolveWithin(carriers[0], fragment, ref)
	}
	if meta := embeddedMetaSchemas()[name]; meta != nil {
		return s.resolveWithin(meta, fragment, ref)
	}
	if s.supplied == nil {
		return schemaTarget{}, &failure{missingCapability, fmt.Sprintf("%q names %s, a resource the document does not embed (§7.4)", ref, name)}
	}
	return schemaTarget{}, &failure{missingCapability, fmt.Sprintf("%q names %s, a resource the document does not embed and the application did not supply (§7.4)", ref, name)}
}

// resolveInDocument resolves a same-document reference in the document
// resource by OBI-D-12's own lookup (§7.2).
func (s *schemaSpace) resolveInDocument(ref string) (schemaTarget, *failure) {
	found := s.document.lookUpSameDocument(ref, s.obi.value)
	switch {
	case found.declaredTwice:
		return schemaTarget{}, &failure{undefinedResult, fmt.Sprintf("%q names a plain name the document resource declares more than once, which JSON Schema leaves undefined (Core §8.2.2)", ref)}
	case found.why != "":
		return schemaTarget{}, &failure{undefinedResult, fmt.Sprintf("%q %s (§7.3)", ref, found.why)}
	}
	if _, indexed := s.obi.schemas[found.location]; !indexed {
		return schemaTarget{}, s.obi.unindexed(found.location, fmt.Sprintf("%q identifies the schema at %s", ref, found.location))
	}
	return schemaTarget{doc: s.obi, location: found.location, name: found.name}, nil
}

// resolveWithin resolves a decoded fragment within a resource: the resource
// itself, a JSON Pointer from its root, or a plain name it declares.
func (s *schemaSpace) resolveWithin(r *docResource, fragment, ref string) (schemaTarget, *failure) {
	if fragment == "" {
		if _, isSchema := r.doc.schemas[r.location]; !isSchema {
			return schemaTarget{}, r.doc.notSchema(fmt.Sprintf("%q names %s, whose root is not a schema", ref, describeResource(r)))
		}
		return schemaTarget{doc: r.doc, location: r.location}, nil
	}
	if strings.HasPrefix(fragment, "/") {
		tokens, ok := jsonpointer.Parse(fragment)
		location := r.location + jsonpointer.Format(tokens...)
		if !ok {
			return schemaTarget{}, r.doc.notSchema(fmt.Sprintf("%q is not a JSON Pointer", ref))
		}
		if _, isSchema := r.doc.schemas[location]; !isSchema {
			return schemaTarget{}, r.doc.unindexed(location, fmt.Sprintf("%q reaches no schema in %s", ref, describeResource(r)))
		}
		return schemaTarget{doc: r.doc, location: location}, nil
	}
	return s.resolveName(r, fragment, ref)
}

func (s *schemaSpace) resolveName(r *docResource, name, ref string) (schemaTarget, *failure) {
	switch declared := r.anchors[name]; len(declared) {
	case 0:
		return schemaTarget{}, r.doc.unindexed(r.location, fmt.Sprintf("%q names a plain name %s does not declare", ref, describeResource(r)))
	case 1:
		return schemaTarget{doc: r.doc, location: declared[0], name: name}, nil
	}
	return schemaTarget{}, &failure{undefinedResult, fmt.Sprintf("%q names a plain name %s declares more than once, which JSON Schema leaves undefined (Core §8.2.2)", ref, describeResource(r))}
}

// notSchema labels a reference that reaches no schema in a document: in the
// OBI document, JSON Schema leaves it undefined (§7.4); in a supplied
// resource, which may be another format that embeds schemas, it is a
// capability core lacks.
func (d *schemaDoc) notSchema(reason string) *failure {
	if d.kind == obiDocument {
		return &failure{undefinedResult, reason}
	}
	return &failure{missingCapability, reason}
}

// unindexed labels a lookup that finds no schema at or below a location: in a
// unit nesting deeper than schemaDepthLimit, below which nothing is indexed,
// as core's limit met; elsewhere as notSchema does.
func (d *schemaDoc) unindexed(location, reason string) *failure {
	if unit := d.units[unitOf(schemaKey{d, location}).location]; unit != nil && unit.deep {
		return &failure{missingCapability, fmt.Sprintf("%s, in a unit nesting subschemas deeper than %d levels, which this SDK does not index", reason, schemaDepthLimit)}
	}
	return d.notSchema(reason)
}

// decodeFragment percent-decodes a fragment once, reporting false when it
// does not decode to valid UTF-8.
func decodeFragment(fragment string) (string, bool) {
	decoded, err := url.PathUnescape(fragment)
	if err != nil || !utf8.ValidString(decoded) {
		return "", false
	}
	return decoded, true
}

// describeResource names a resource for a message.
func describeResource(r *docResource) string {
	switch {
	case r.document:
		return "the document resource"
	case r.id != "":
		return r.id
	}
	return "the schema resource at " + locationOf(r.doc, r.location)
}

// locationOf writes a location in a schema document as a URI-reference, as
// NoVerdictError.Location does: "#" and a JSON Pointer in the OBI document,
// or a supplied resource's URI, "#", and a JSON Pointer.
func locationOf(d *schemaDoc, location string) string {
	tokens, _ := jsonpointer.Parse(location)
	return d.uri + "#" + fragmentPointer(tokens)
}

// metaSchemaFiles are the dialect meta-schema and the seven vocabulary
// meta-schemas OBI-D-10 names.
//
//go:embed metaschemas/draft2020-12
var metaSchemaFiles embed.FS

// embeddedMetaSchemas maps each embedded meta-schema's URI to its root
// resource.
var embeddedMetaSchemas = sync.OnceValue(func() map[string]*docResource {
	out := map[string]*docResource{}
	err := fs.WalkDir(metaSchemaFiles, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := metaSchemaFiles.ReadFile(path)
		if err != nil {
			return err
		}
		var value any
		if err := unmarshalJSON(data, &value); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		id, _ := value.(map[string]any)["$id"].(string)
		d := indexDocument(metaDocument, id, value)
		out[id] = d.resources[0]
		return nil
	})
	if err != nil {
		panic(fmt.Sprintf("openbindings: embedded meta-schemas: %v", err))
	}
	return out
})

// suppliedResources are the schema documents an application supplies,
// indexed once.
type suppliedResources struct {
	docs []*schemaDoc
}

// documents returns the supplied documents, of which a nil
// *suppliedResources has none.
func (r *suppliedResources) documents() []*schemaDoc {
	if r == nil {
		return nil
	}
	return r.docs
}

// newSuppliedResources reads and indexes supplied resources, refusing one
// whose document is not JSON, whose URI is not absolute or carries a
// non-empty fragment, or whose URI another's equals in normal form.
func newSuppliedResources(resources []Resource) (*suppliedResources, error) {
	out := &suppliedResources{}
	seen := map[string]string{}
	for _, resource := range resources {
		wellFormed, absolute := uriReference(resource.URI)
		uri := strings.TrimSuffix(resource.URI, "#")
		if !wellFormed || !absolute || strings.Contains(uri, "#") {
			return nil, fmt.Errorf("openbindings: a resource's URI must be an absolute URI with no fragment; got %q", resource.URI)
		}
		normal := normalURI(uri)
		if other, taken := seen[normal]; taken {
			return nil, fmt.Errorf("openbindings: the resources %q and %q have one URI in normal form", other, resource.URI)
		}
		seen[normal] = resource.URI
		value, err := readJSONText(resource.Document)
		if err != nil {
			// A configuration error, which neither matches nor reads as a
			// value's outcome (ErrNoVerdict, ErrUndefined): the cause, by %v.
			var refusal *NoVerdictError
			if errors.As(err, &refusal) {
				err = refusal.Cause
			}
			return nil, fmt.Errorf("openbindings: the resource %q is not JSON core can read exactly: %v", resource.URI, err)
		}
		out.docs = append(out.docs, indexDocument(suppliedDocument, uri, value))
	}
	slices.SortFunc(out.docs, func(a, b *schemaDoc) int { return strings.Compare(a.uri, b.uri) })
	return out, nil
}

// cloneJSON copies a JSON value deeply, so a bundle never shares a container
// with the document it copies from.
func cloneJSON(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, member := range v {
			out[key] = cloneJSON(member)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = cloneJSON(item)
		}
		return out
	}
	return value
}
