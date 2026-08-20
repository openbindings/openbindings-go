package openapi

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/oasdiff/yaml"
)

// rawRefSiblingNormalizer preserves the source edition's reference semantics
// before kin-openapi resolves the document into typed values. This boundary is
// intentional: SchemaRef keeps 3.1 siblings privately and overlays them onto
// the target, so a conflicting target keyword cannot be recovered afterward.
//
// Targets are recorded with their OpenAPI position. That lets external
// fragments be normalized as schemas, responses, path items, and so on while
// leaving examples, extensions, and other application data completely opaque.
type rawRefSiblingNormalizer struct {
	mu              sync.Mutex
	fallbackVersion string
	targets         map[string]map[rawRefTarget]struct{}
	join            func(*url.URL, *url.URL) *url.URL
	schemaOverlays  *rawSchemaOverlayCollector
}

type rawRefTarget struct {
	fragment string
	kind     rawRefTargetKind
}

type rawRefTargetKind uint8

const (
	rawSchemaTarget rawRefTargetKind = iota
	rawPathItemTarget
	rawParameterTarget
	rawHeaderTarget
	rawRequestBodyTarget
	rawResponseTarget
	rawCallbackTarget
	rawSecuritySchemeTarget
)

type rawRefSemantics uint8

const referenceMetadataMarker = "x-openbindings-internal-reference-metadata"

const (
	rawRefSemanticsUnknown rawRefSemantics = iota
	rawRefSemanticsIgnore
	rawRefSemanticsCompose
)

func newRawRefSiblingNormalizer(join func(*url.URL, *url.URL) *url.URL) *rawRefSiblingNormalizer {
	return &rawRefSiblingNormalizer{
		targets: map[string]map[rawRefTarget]struct{}{},
		join:    join,
	}
}

// normalizeResource parses one raw root or external resource, rewrites only
// objects known to occupy Schema Object positions, and returns the original
// bytes when no rewrite was necessary. Returning the original bytes preserves
// loader origin information for the overwhelmingly common no-sibling case.
func (n *rawRefSiblingNormalizer) normalizeResource(data []byte, resource *url.URL) ([]byte, error) {
	return n.normalizeResourceAt(data, resource, resource)
}

// normalizeResourceAt separates the URI used to identify an already-recorded
// target from the resource's final retrieval URI. Redirects retain the former
// for target lookup, while JSON Schema base-URI and nested-reference semantics
// must use the latter.
func (n *rawRefSiblingNormalizer) normalizeResourceAt(data []byte, requested, retrieval *url.URL) ([]byte, error) {
	var root any
	if _, err := yaml.Unmarshal(data, &root, yaml.DecodeOpts{DisableTimestamps: true}); err != nil {
		// Preserve kin-openapi's own combined JSON/YAML diagnostic for malformed
		// artifacts; this pass is semantic normalization, not a second parser API.
		return data, nil
	}

	object, _ := root.(map[string]any)
	version := ""
	if object != nil {
		version, _ = object["openapi"].(string)
	}
	if version != "" {
		n.rememberFallbackVersion(version)
	}
	if version == "" {
		version = n.currentFallbackVersion()
	}
	semantics := rawSemanticsForOpenAPIVersion(version)
	if object != nil {
		if declared, ok := object["openapi"].(string); ok && declared != "" {
			semantics = rawDocumentSchemaSemantics(object, declared)
		} else if dialect, present := object["$schema"]; present {
			if dialectString, ok := dialect.(string); ok && supportedComposingDialect(dialectString) {
				semantics = rawRefSemanticsCompose
			} else {
				semantics = rawRefSemanticsUnknown
			}
		}
	}
	changed := false
	// The boolean-schema lift is a kin-openapi REPRESENTATION bridge, and it
	// runs on both accepted lines for different reasons. On the 3.1 line
	// boolean schemas are the dialect's own, so every Schema Object position
	// is lifted. On the 3.0 line they are not in the dialect at all: the lift
	// is scoped to the positions the acceptance floor classifies as a
	// dialect JSON-type violation (a `properties` member, D15), so the source
	// loads far enough for that instrument's per-unit verdict to reach the
	// consumer instead of the whole artifact being refused over one defective
	// unit (F-O1-13, ruled 2026-08-20). Every other boolean spelling on that
	// line keeps the loader's own refusal.
	if semantics == rawRefSemanticsCompose || semantics == rawRefSemanticsIgnore {
		var lifted bool
		var err error
		root, lifted, err = n.liftBooleanSchemas(root, requested, retrieval, semantics == rawRefSemanticsIgnore)
		if err != nil {
			return nil, err
		}
		changed = changed || lifted
		object, _ = root.(map[string]any)
	}

	if object != nil {
		if declared, ok := object["openapi"].(string); ok && declared != "" {
			normalized, err := n.normalizeOpenAPIDocument(object, semantics, retrieval)
			if err != nil {
				return nil, err
			}
			changed = changed || normalized
		} else if dialect, ok := object["$schema"].(string); ok && supportedComposingDialect(dialect) {
			normalized, err := n.normalizeSchema(object, rawRefSemanticsCompose, retrieval)
			if err != nil {
				return nil, err
			}
			changed = changed || normalized
		}
	}

	// Processing a target may discover another same-resource target. Iterate
	// until the position-aware reference closure for this resource is stable.
	schemaScopes := rawSchemaResourceScopes(root, retrieval)
	resourceKeys := map[string]bool{
		artifactResourceKey(requested): true,
		artifactResourceKey(retrieval): true,
	}
	for key := range schemaScopes {
		resourceKeys[key] = true
	}
	type scopedTarget struct {
		resourceKey string
		target      rawRefTarget
	}
	processed := map[scopedTarget]bool{}
	for {
		progress := false
		for resourceKey := range resourceKeys {
			for _, target := range n.targetsForKey(resourceKey) {
				scoped := scopedTarget{resourceKey: resourceKey, target: target}
				if processed[scoped] {
					continue
				}
				processed[scoped] = true
				progress = true
				var node any
				var targetBase *url.URL
				var ok bool
				if target.kind == rawSchemaTarget {
					if scope := schemaScopes[resourceKey]; scope != nil {
						node, targetBase, ok = scope.fragment(target.fragment)
					}
				} else if resourceKey == artifactResourceKey(requested) || resourceKey == artifactResourceKey(retrieval) {
					node, ok = rawFragmentTarget(root, target.fragment, target.kind)
				}
				if !ok {
					continue // kin-openapi owns the eventual unresolved-ref diagnostic
				}
				if targetBase == nil {
					targetBase = retrieval
				}
				targetChanged, err := n.normalizeTarget(node, target.kind, semantics, targetBase)
				if err != nil {
					return nil, err
				}
				changed = changed || targetChanged
			}
		}
		if !progress {
			break
		}
	}

	// Synthesis may ask for a raw-presence sidecar. Mark only Schema Objects
	// reached through known OpenAPI positions, and do it after reference
	// normalization so OAS 3.0's ignored $ref siblings have already vanished.
	// The injected markers live solely in this loader's private bytes and are
	// consumed from typed objects before InternalizeRefs.
	if n.schemaOverlays != nil {
		seenSchemas := map[uintptr]bool{}
		mark := func(schema any, _ string) {
			changed = n.schemaOverlays.markRawSchemaTree(schema, seenSchemas) || changed
		}
		if object != nil {
			if declared, ok := object["openapi"].(string); ok && declared != "" {
				rawForEachSchemaRoot(root, mark)
			}
		}
		for resourceKey := range resourceKeys {
			for _, target := range n.targetsForKey(resourceKey) {
				var node any
				var ok bool
				if target.kind == rawSchemaTarget {
					if scope := schemaScopes[resourceKey]; scope != nil {
						node, _, ok = scope.fragment(target.fragment)
					}
				} else if resourceKey == artifactResourceKey(requested) || resourceKey == artifactResourceKey(retrieval) {
					node, ok = rawFragmentTarget(root, target.fragment, target.kind)
				}
				if ok {
					rawForEachSchemaInTarget(node, target.kind, "", mark)
				}
			}
		}
	}

	if !changed {
		return data, nil
	}
	encoded, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("marshal normalized OpenAPI resource: %w", err)
	}
	return encoded, nil
}

// rawBooleanLiftState carries the lift's SCOPE. On the 3.1 line every Schema
// Object position may hold a boolean, so every one is lifted. On the 3.0 line
// a boolean is not a Schema Object at all, and the lift there is a LOAD BRIDGE
// for a defect the acceptance floor already owns rather than an
// accommodation: it runs at exactly the positions that instrument classifies
// (a `properties` member, D15 -- see isFloorSchemaValued), so the source loads
// far enough for the unit's verdict to be what a consumer sees. A boolean
// anywhere else on that line keeps kin-openapi's own whole-source refusal,
// which is the honest answer while nothing classifies it: silently admitting
// an unclassified invalid spelling is the failure this scope exists to avoid.
type rawBooleanLiftState struct {
	// propertiesOnly restricts lifting to `properties` members (the 3.0 line).
	propertiesOnly bool
}

// liftBooleanSchemas bridges a kin-openapi representation gap. OAS 3.1 uses
// full JSON Schema, including literal true/false schemas, while kin-openapi's
// SchemaRef accepts only objects. We replace only values reached through known
// Schema Object positions with an internal object sentinel. The typed engine
// can then resolve and route the artifact normally; synthesis restores the
// literal boolean before the schema crosses the OpenBindings boundary.
func (n *rawRefSiblingNormalizer) liftBooleanSchemas(root any, requested, retrieval *url.URL, propertiesOnly bool) (any, bool, error) {
	state := &rawBooleanLiftState{propertiesOnly: propertiesOnly}
	changed := false
	apply := func(schema any, pointer string) error {
		lifted, didChange, err := state.schema(schema, false)
		if err != nil {
			return err
		}
		if !didChange {
			return nil
		}
		changed = true
		if _, directBoolean := schema.(bool); directBoolean {
			if err := replaceRawJSONPointer(&root, pointer, lifted); err != nil {
				return err
			}
		}
		return nil
	}

	object, _ := root.(map[string]any)
	if object != nil {
		if declared, _ := object["openapi"].(string); declared != "" {
			var visitErr error
			rawForEachSchemaRoot(root, func(schema any, pointer string) {
				if visitErr == nil {
					visitErr = apply(schema, pointer)
				}
			})
			return root, changed, visitErr
		}
		if dialect, _ := object["$schema"].(string); supportedComposingDialect(dialect) {
			if err := apply(root, ""); err != nil {
				return nil, false, err
			}
		}
	}

	for _, target := range n.targetsForResources(requested, retrieval) {
		node, ok := rawFragmentTarget(root, target.fragment, target.kind)
		if !ok {
			continue
		}
		pointer := target.fragment
		if pointer != "" && !strings.HasPrefix(pointer, "/") {
			// An anchor necessarily names an object schema. Nested boolean
			// positions mutate through that object; no root replacement is needed.
			pointer = ""
		}
		var visitErr error
		rawForEachSchemaInTarget(node, target.kind, pointer, func(schema any, schemaPointer string) {
			if visitErr == nil {
				visitErr = apply(schema, schemaPointer)
			}
		})
		if visitErr != nil {
			return nil, false, visitErr
		}
	}
	return root, changed, nil
}

// schema lifts boolean schemas within one Schema Object. atPropertiesMember
// says whether `value` is a member of a `properties` map, which is the only
// position the 3.0-line scope lifts at.
func (s *rawBooleanLiftState) schema(value any, atPropertiesMember bool) (any, bool, error) {
	if literal, ok := value.(bool); ok {
		if s.propertiesOnly && !atPropertiesMember {
			return value, false, nil
		}
		// Use ordinary, semantics-equivalent JSON Schema rather than a private
		// extension sentinel: anyOf[true,false] is true; allOf[true,false] is
		// false. An author who writes the same structure gets the same meaning,
		// so there is no reserved-name collision or observable mutation.
		keyword := "anyOf"
		if !literal {
			keyword = "allOf"
		}
		lifted := map[string]any{keyword: []any{map[string]any{}, map[string]any{"not": map[string]any{}}}}
		return lifted, true, nil
	}
	object, _ := value.(map[string]any)
	if object == nil {
		return value, false, nil
	}
	changed := false
	for key, child := range object {
		switch {
		case rawSchemaMapKeywords[key]:
			children, _ := child.(map[string]any)
			for name, nested := range children {
				lifted, didChange, err := s.schema(nested, key == "properties")
				if err != nil {
					return nil, false, err
				}
				if didChange {
					children[name] = lifted
					changed = true
				}
			}
		case rawSchemaArrayKeywords[key]:
			children, _ := child.([]any)
			for index, nested := range children {
				lifted, didChange, err := s.schema(nested, false)
				if err != nil {
					return nil, false, err
				}
				if didChange {
					children[index] = lifted
					changed = true
				}
			}
		case rawSchemaSingleKeywords[key]:
			lifted, didChange, err := s.schema(child, false)
			if err != nil {
				return nil, false, err
			}
			if didChange {
				object[key] = lifted
				changed = true
			}
		}
	}
	return object, changed, nil
}

func replaceRawJSONPointer(root *any, pointer string, replacement any) error {
	if pointer == "" {
		*root = replacement
		return nil
	}
	segments := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	current := *root
	for index, encoded := range segments {
		name := unescapeJSONPointerSegment(encoded)
		last := index == len(segments)-1
		switch node := current.(type) {
		case map[string]any:
			if last {
				node[name] = replacement
				return nil
			}
			current = node[name]
		case []any:
			position, err := strconv.Atoi(name)
			if err != nil || position < 0 || position >= len(node) {
				return fmt.Errorf("replace normalized boolean schema at %q: invalid array index", pointer)
			}
			if last {
				node[position] = replacement
				return nil
			}
			current = node[position]
		default:
			return fmt.Errorf("replace normalized boolean schema at %q: parent is not a container", pointer)
		}
	}
	return fmt.Errorf("replace normalized boolean schema at %q: target not found", pointer)
}

func rawDocumentSchemaSemantics(root map[string]any, version string) rawRefSemantics {
	semantics := rawSemanticsForOpenAPIVersion(version)
	if semantics != rawRefSemanticsCompose {
		return semantics
	}
	dialect, present := root["jsonSchemaDialect"]
	if !present {
		return rawRefSemanticsCompose // the OAS 3.1 base dialect
	}
	dialectString, ok := dialect.(string)
	if ok && supportedComposingDialect(dialectString) {
		return rawRefSemanticsCompose
	}
	return rawRefSemanticsUnknown
}

func (n *rawRefSiblingNormalizer) rememberFallbackVersion(version string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.fallbackVersion == "" {
		n.fallbackVersion = version
	}
}

func (n *rawRefSiblingNormalizer) currentFallbackVersion() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.fallbackVersion
}

func rawSemanticsForOpenAPIVersion(version string) rawRefSemantics {
	switch {
	case version == "3.0" || strings.HasPrefix(version, "3.0."):
		return rawRefSemanticsIgnore
	case version == "3.1" || strings.HasPrefix(version, "3.1."):
		return rawRefSemanticsCompose
	default:
		return rawRefSemanticsUnknown
	}
}

func supportedComposingDialect(dialect string) bool {
	dialect = strings.TrimSuffix(dialect, "#")
	return dialect == "https://json-schema.org/draft/2020-12/schema" ||
		dialect == "https://spec.openapis.org/oas/3.1/dialect/base"
}

func (n *rawRefSiblingNormalizer) normalizeOpenAPIDocument(root map[string]any, semantics rawRefSemantics, base *url.URL) (bool, error) {
	changed := false
	components, _ := root["components"].(map[string]any)
	if components != nil {
		for _, item := range rawMapValues(components["schemas"]) {
			c, err := n.normalizeTarget(item, rawSchemaTarget, semantics, base)
			if err != nil {
				return false, err
			}
			changed = changed || c
		}
		componentKinds := []struct {
			key  string
			kind rawRefTargetKind
		}{
			{"parameters", rawParameterTarget},
			{"headers", rawHeaderTarget},
			{"requestBodies", rawRequestBodyTarget},
			{"responses", rawResponseTarget},
			{"callbacks", rawCallbackTarget},
			{"pathItems", rawPathItemTarget},
			{"securitySchemes", rawSecuritySchemeTarget},
		}
		for _, entry := range componentKinds {
			for _, item := range rawMapValues(components[entry.key]) {
				c, err := n.normalizeTarget(item, entry.kind, semantics, base)
				if err != nil {
					return false, err
				}
				changed = changed || c
			}
		}
	}
	for _, key := range []string{"paths", "webhooks"} {
		for _, item := range rawMapValues(root[key]) {
			c, err := n.normalizeTarget(item, rawPathItemTarget, semantics, base)
			if err != nil {
				return false, err
			}
			changed = changed || c
		}
	}
	return changed, nil
}

func rawMapValues(value any) []any {
	object, _ := value.(map[string]any)
	if object == nil {
		return nil
	}
	out := make([]any, 0, len(object))
	for _, item := range object {
		out = append(out, item)
	}
	return out
}

func (n *rawRefSiblingNormalizer) normalizeTarget(value any, kind rawRefTargetKind, semantics rawRefSemantics, base *url.URL) (bool, error) {
	if kind == rawSchemaTarget {
		return n.normalizeSchema(value, semantics, base)
	}
	object, _ := value.(map[string]any)
	if object == nil {
		return false, nil
	}
	changed := false
	if ref, ok := object["$ref"].(string); ok {
		n.recordTarget(base, ref, kind)
		// OAS 3.0 Reference Objects ignore siblings. Path Item Objects have a
		// $ref field but are not Reference Objects, so their other fields retain
		// the artifact-defined (and historically implementation-defined) meaning.
		if semantics == rawRefSemanticsIgnore && kind != rawPathItemTarget && len(object) > 1 {
			for key := range object {
				if key != "$ref" {
					delete(object, key)
				}
			}
			return true, nil
		}
		if kind != rawPathItemTarget {
			if semantics == rawRefSemanticsCompose && referenceMetadataCanMutateTarget(kind) {
				moved, err := moveReferenceMetadataToMarker(object)
				if err != nil {
					return false, err
				}
				changed = changed || moved
			}
			return changed, nil
		}
		// A Path Item's $ref is a fixed field rather than a Reference Object.
		// Continue through any authored sibling operations so their schema
		// positions receive the same edition-aware normalization.
	}

	apply := func(child any, childKind rawRefTargetKind) error {
		c, err := n.normalizeTarget(child, childKind, semantics, base)
		changed = changed || c
		return err
	}
	applyMany := func(container any, childKind rawRefTargetKind) error {
		for _, child := range rawMapValues(container) {
			if err := apply(child, childKind); err != nil {
				return err
			}
		}
		return nil
	}

	switch kind {
	case rawPathItemTarget:
		if parameters, ok := object["parameters"].([]any); ok {
			for _, parameter := range parameters {
				if err := apply(parameter, rawParameterTarget); err != nil {
					return false, err
				}
			}
		}
		for _, method := range httpMethods {
			operation, _ := object[method].(map[string]any)
			if operation == nil {
				continue
			}
			if parameters, ok := operation["parameters"].([]any); ok {
				for _, parameter := range parameters {
					if err := apply(parameter, rawParameterTarget); err != nil {
						return false, err
					}
				}
			}
			if err := apply(operation["requestBody"], rawRequestBodyTarget); err != nil {
				return false, err
			}
			if err := applyMany(operation["responses"], rawResponseTarget); err != nil {
				return false, err
			}
			if err := applyMany(operation["callbacks"], rawCallbackTarget); err != nil {
				return false, err
			}
		}
	case rawParameterTarget, rawHeaderTarget:
		if err := apply(object["schema"], rawSchemaTarget); err != nil {
			return false, err
		}
		c, err := n.normalizeContent(object["content"], semantics, base)
		if err != nil {
			return false, err
		}
		changed = changed || c
	case rawRequestBodyTarget:
		c, err := n.normalizeContent(object["content"], semantics, base)
		if err != nil {
			return false, err
		}
		changed = changed || c
	case rawResponseTarget:
		if err := applyMany(object["headers"], rawHeaderTarget); err != nil {
			return false, err
		}
		c, err := n.normalizeContent(object["content"], semantics, base)
		if err != nil {
			return false, err
		}
		changed = changed || c
	case rawCallbackTarget:
		if err := applyMany(object, rawPathItemTarget); err != nil {
			return false, err
		}
	}
	return changed, nil
}

func referenceMetadataCanMutateTarget(kind rawRefTargetKind) bool {
	switch kind {
	case rawParameterTarget, rawHeaderTarget, rawRequestBodyTarget, rawResponseTarget, rawSecuritySchemeTarget:
		return true
	default:
		return false
	}
}

func moveReferenceMetadataToMarker(object map[string]any) (bool, error) {
	metadata := map[string]any{}
	for _, key := range []string{"summary", "description"} {
		if value, present := object[key]; present {
			metadata[key] = value
			delete(object, key)
		}
	}
	if len(metadata) == 0 {
		return false, nil
	}
	if _, collision := object[referenceMetadataMarker]; collision {
		return false, fmt.Errorf("OpenAPI reference uses processor-reserved extension %q", referenceMetadataMarker)
	}
	object[referenceMetadataMarker] = metadata
	return true, nil
}

func (n *rawRefSiblingNormalizer) normalizeContent(value any, semantics rawRefSemantics, base *url.URL) (bool, error) {
	changed := false
	for _, mediaValue := range rawMapValues(value) {
		media, _ := mediaValue.(map[string]any)
		if media == nil {
			continue
		}
		c, err := n.normalizeTarget(media["schema"], rawSchemaTarget, semantics, base)
		if err != nil {
			return false, err
		}
		changed = changed || c
		for _, encodingValue := range rawMapValues(media["encoding"]) {
			encoding, _ := encodingValue.(map[string]any)
			if encoding == nil {
				continue
			}
			if _, present := encoding["allowReserved"]; present {
				if _, hasStyle := encoding["style"]; !hasStyle {
					if _, hasExplode := encoding["explode"]; !hasExplode {
						// Explicit allowReserved=false is otherwise erased by the typed
						// model. Materialize its implicit form style, which is
						// semantically identical and preserves branch presence without
						// any private artifact extension.
						encoding["style"] = openapi3.SerializationForm
					}
				}
				changed = true
			}
			for _, header := range rawMapValues(encoding["headers"]) {
				c, err := n.normalizeTarget(header, rawHeaderTarget, semantics, base)
				if err != nil {
					return false, err
				}
				changed = changed || c
			}
		}
	}
	return changed, nil
}

var rawSchemaMapKeywords = map[string]bool{
	"properties": true, "patternProperties": true, "$defs": true,
	"definitions": true, "dependentSchemas": true,
}

var rawSchemaArrayKeywords = map[string]bool{
	"oneOf": true, "anyOf": true, "allOf": true, "prefixItems": true,
}

var rawSchemaSingleKeywords = map[string]bool{
	"items": true, "additionalProperties": true, "not": true,
	"if": true, "then": true, "else": true, "propertyNames": true,
	"contains": true, "contentSchema": true, "unevaluatedItems": true,
	"unevaluatedProperties": true,
}

func (n *rawRefSiblingNormalizer) normalizeSchema(value any, inherited rawRefSemantics, base *url.URL) (bool, error) {
	object, _ := value.(map[string]any)
	if object == nil { // boolean schemas have no reference siblings
		return false, nil
	}
	semantics := inherited
	if dialect, ok := object["$schema"].(string); ok {
		if supportedComposingDialect(dialect) {
			semantics = rawRefSemanticsCompose
		} else {
			semantics = rawRefSemanticsUnknown
		}
	}
	changed := false
	schemaBase := base
	if id, ok := object["$id"].(string); ok {
		if parsed, err := url.Parse(id); err == nil {
			if schemaBase != nil {
				parsed = schemaBase.ResolveReference(parsed)
			}
			schemaBase = parsed
			if semantics == rawRefSemanticsCompose && parsed.IsAbs() && parsed.String() != id {
				object["$id"] = parsed.String()
				changed = true
			}
		}
	}

	if ref, ok := object["$ref"].(string); ok {
		// kin-openapi resolves relative schema references from the containing
		// OpenAPI document but does not apply a nested JSON Schema `$id` base.
		// Lift that standard base-URI rule into the normalized artifact so the
		// vanilla loader follows the same target, especially after redirects.
		if semantics == rawRefSemanticsCompose && schemaBase != nil && !strings.HasPrefix(ref, "#") {
			if parsed, err := url.Parse(ref); err == nil && !parsed.IsAbs() {
				resolved := schemaBase.ResolveReference(parsed)
				if n.join != nil {
					resolved = n.join(schemaBase, parsed)
				}
				ref = resolved.String()
				object["$ref"] = ref
				changed = true
			}
		}
		n.recordTarget(schemaBase, ref, rawSchemaTarget)
		if len(object) > 1 {
			switch semantics {
			case rawRefSemanticsIgnore:
				for key := range object {
					if key != "$ref" {
						delete(object, key)
					}
				}
				return true, nil
			case rawRefSemanticsCompose:
				delete(object, "$ref")
				refSchema := map[string]any{"$ref": ref}
				if allOf, present := object["allOf"]; present {
					items, ok := allOf.([]any)
					if !ok {
						return false, fmt.Errorf("OpenAPI Schema Object with $ref siblings declares non-array allOf")
					}
					object["allOf"] = append([]any{refSchema}, items...)
				} else {
					object["allOf"] = []any{refSchema}
				}
				changed = true
			case rawRefSemanticsUnknown:
				// Leave custom-dialect semantics to the artifact-native engine.
				// Portable synthesis rejects only an operation that actually
				// projects this schema, after reference resolution.
			}
		}
	}

	for key, child := range object {
		switch {
		case rawSchemaMapKeywords[key]:
			for _, nested := range rawMapValues(child) {
				c, err := n.normalizeSchema(nested, semantics, schemaBase)
				if err != nil {
					return false, err
				}
				changed = changed || c
			}
		case rawSchemaArrayKeywords[key]:
			items, _ := child.([]any)
			for _, nested := range items {
				c, err := n.normalizeSchema(nested, semantics, schemaBase)
				if err != nil {
					return false, err
				}
				changed = changed || c
			}
		case rawSchemaSingleKeywords[key]:
			c, err := n.normalizeSchema(child, semantics, schemaBase)
			if err != nil {
				return false, err
			}
			changed = changed || c
		}
	}
	return changed, nil
}

func (n *rawRefSiblingNormalizer) recordTarget(base *url.URL, ref string, kind rawRefTargetKind) {
	parsed, err := url.Parse(ref)
	if err != nil {
		return
	}
	var resolved *url.URL
	if strings.HasPrefix(ref, "#") {
		resolved = cloneURL(base)
		if resolved == nil {
			resolved = &url.URL{}
		}
		resolved.Fragment = parsed.Fragment
	} else if base != nil {
		if n.join != nil {
			resolved = n.join(base, parsed)
		} else {
			resolved = base.ResolveReference(parsed)
		}
	} else {
		resolved = parsed
	}
	if resolved == nil {
		return
	}
	fragment := resolved.Fragment
	resolved = cloneURL(resolved)
	key := artifactResourceKey(resolved)
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.targets[key] == nil {
		n.targets[key] = map[rawRefTarget]struct{}{}
	}
	n.targets[key][rawRefTarget{fragment: fragment, kind: kind}] = struct{}{}
}

func (n *rawRefSiblingNormalizer) targetsFor(resource *url.URL) []rawRefTarget {
	return n.targetsForKey(artifactResourceKey(resource))
}

func (n *rawRefSiblingNormalizer) targetsForKey(key string) []rawRefTarget {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]rawRefTarget, 0, len(n.targets[key]))
	for target := range n.targets[key] {
		out = append(out, target)
	}
	return out
}

func (n *rawRefSiblingNormalizer) targetsForResources(resources ...*url.URL) []rawRefTarget {
	seen := map[rawRefTarget]bool{}
	var out []rawRefTarget
	for _, resource := range resources {
		for _, target := range n.targetsFor(resource) {
			if !seen[target] {
				seen[target] = true
				out = append(out, target)
			}
		}
	}
	return out
}

func rawFragmentTarget(root any, fragment string, kind rawRefTargetKind) (any, bool) {
	if fragment == "" {
		return root, true
	}
	if strings.HasPrefix(fragment, "/") {
		current := root
		for _, encoded := range strings.Split(strings.TrimPrefix(fragment, "/"), "/") {
			segment := unescapeJSONPointerSegment(encoded)
			switch node := current.(type) {
			case map[string]any:
				var ok bool
				current, ok = node[segment]
				if !ok {
					return nil, false
				}
			case []any:
				index, err := strconv.Atoi(segment)
				if err != nil || index < 0 || index >= len(node) {
					return nil, false
				}
				current = node[index]
			default:
				return nil, false
			}
		}
		return current, true
	}
	if kind != rawSchemaTarget {
		return nil, false
	}
	return rawSchemaAnchorTarget(root, fragment)
}

func rawSchemaAnchorTarget(root any, anchor string) (any, bool) {
	object, _ := root.(map[string]any)
	if object == nil || object["openapi"] == nil {
		return rawSchemaAnchorInSchema(root, anchor)
	}
	components, _ := object["components"].(map[string]any)
	if components != nil {
		for _, schema := range rawMapValues(components["schemas"]) {
			if found, ok := rawSchemaAnchorInSchema(schema, anchor); ok {
				return found, true
			}
		}
		for _, entry := range []struct {
			key  string
			kind rawRefTargetKind
		}{{"parameters", rawParameterTarget}, {"headers", rawHeaderTarget}, {"requestBodies", rawRequestBodyTarget}, {"responses", rawResponseTarget}, {"callbacks", rawCallbackTarget}, {"pathItems", rawPathItemTarget}} {
			for _, target := range rawMapValues(components[entry.key]) {
				if found, ok := rawSchemaAnchorInTarget(target, entry.kind, anchor); ok {
					return found, true
				}
			}
		}
	}
	for _, key := range []string{"paths", "webhooks"} {
		for _, pathItem := range rawMapValues(object[key]) {
			if found, ok := rawSchemaAnchorInTarget(pathItem, rawPathItemTarget, anchor); ok {
				return found, true
			}
		}
	}
	return nil, false
}

func rawSchemaAnchorInSchema(value any, anchor string) (any, bool) {
	object, _ := value.(map[string]any)
	if object == nil {
		return nil, false
	}
	if object["$anchor"] == anchor || object["$dynamicAnchor"] == anchor {
		return object, true
	}
	for key, child := range object {
		switch {
		case rawSchemaMapKeywords[key]:
			for _, nested := range rawMapValues(child) {
				if found, ok := rawSchemaAnchorInSchema(nested, anchor); ok {
					return found, true
				}
			}
		case rawSchemaArrayKeywords[key]:
			items, _ := child.([]any)
			for _, nested := range items {
				if found, ok := rawSchemaAnchorInSchema(nested, anchor); ok {
					return found, true
				}
			}
		case rawSchemaSingleKeywords[key]:
			if found, ok := rawSchemaAnchorInSchema(child, anchor); ok {
				return found, true
			}
		}
	}
	return nil, false
}

func rawSchemaAnchorInTarget(value any, kind rawRefTargetKind, anchor string) (any, bool) {
	if kind == rawSchemaTarget {
		return rawSchemaAnchorInSchema(value, anchor)
	}
	object, _ := value.(map[string]any)
	if object == nil {
		return nil, false
	}
	searchContent := func(value any) (any, bool) {
		for _, mediaValue := range rawMapValues(value) {
			media, _ := mediaValue.(map[string]any)
			if media != nil {
				if found, ok := rawSchemaAnchorInSchema(media["schema"], anchor); ok {
					return found, true
				}
			}
		}
		return nil, false
	}
	switch kind {
	case rawParameterTarget, rawHeaderTarget:
		if found, ok := rawSchemaAnchorInSchema(object["schema"], anchor); ok {
			return found, true
		}
		return searchContent(object["content"])
	case rawRequestBodyTarget:
		return searchContent(object["content"])
	case rawResponseTarget:
		for _, header := range rawMapValues(object["headers"]) {
			if found, ok := rawSchemaAnchorInTarget(header, rawHeaderTarget, anchor); ok {
				return found, true
			}
		}
		return searchContent(object["content"])
	case rawCallbackTarget:
		for _, pathItem := range rawMapValues(object) {
			if found, ok := rawSchemaAnchorInTarget(pathItem, rawPathItemTarget, anchor); ok {
				return found, true
			}
		}
	case rawPathItemTarget:
		for _, parameter := range rawSliceValues(object["parameters"]) {
			if found, ok := rawSchemaAnchorInTarget(parameter, rawParameterTarget, anchor); ok {
				return found, true
			}
		}
		for _, method := range httpMethods {
			operation, _ := object[method].(map[string]any)
			if operation == nil {
				continue
			}
			for _, parameter := range rawSliceValues(operation["parameters"]) {
				if found, ok := rawSchemaAnchorInTarget(parameter, rawParameterTarget, anchor); ok {
					return found, true
				}
			}
			if found, ok := rawSchemaAnchorInTarget(operation["requestBody"], rawRequestBodyTarget, anchor); ok {
				return found, true
			}
			for _, response := range rawMapValues(operation["responses"]) {
				if found, ok := rawSchemaAnchorInTarget(response, rawResponseTarget, anchor); ok {
					return found, true
				}
			}
			for _, callback := range rawMapValues(operation["callbacks"]) {
				if found, ok := rawSchemaAnchorInTarget(callback, rawCallbackTarget, anchor); ok {
					return found, true
				}
			}
		}
	}
	return nil, false
}

func rawSliceValues(value any) []any {
	items, _ := value.([]any)
	return items
}

type rawSchemaResourceScope struct {
	root         any
	anchors      map[string]any
	base         *url.URL
	pointerBases map[string]*url.URL
}

func (s *rawSchemaResourceScope) fragment(fragment string) (any, *url.URL, bool) {
	if s == nil {
		return nil, nil, false
	}
	if fragment == "" || strings.HasPrefix(fragment, "/") {
		value, found := rawFragmentTarget(s.root, fragment, rawSchemaTarget)
		if !found {
			return nil, nil, false
		}
		base := s.base
		longest := -1
		for pointer, candidate := range s.pointerBases {
			if (pointer == "" || fragment == pointer || strings.HasPrefix(fragment, pointer+"/")) && len(pointer) > longest {
				base = candidate
				longest = len(pointer)
			}
		}
		return value, cloneURL(base), true
	}
	value, found := s.anchors[fragment]
	return value, cloneURL(s.base), found && value != nil
}

// rawSchemaResourceScopes indexes anchors by their JSON Schema resource URI,
// not merely by physical document. A nested absolute/relative `$id` starts a
// new resource: its anchors must not satisfy parent-document `#anchor` refs,
// while refs addressed to that nested resource must still be normalized even
// though no second HTTP retrieval occurs.
func rawSchemaResourceScopes(root any, retrieval *url.URL) map[string]*rawSchemaResourceScope {
	scopes := map[string]*rawSchemaResourceScope{}
	rootKey := artifactResourceKey(retrieval)
	scopes[rootKey] = &rawSchemaResourceScope{root: root, anchors: map[string]any{}, base: cloneURL(retrieval), pointerBases: map[string]*url.URL{"": cloneURL(retrieval)}}

	var visitSchema func(any, *url.URL, string, string, string)
	visitSchema = func(value any, base *url.URL, resourceKey, resourcePointer, physicalPointer string) {
		object, _ := value.(map[string]any)
		if object == nil {
			return
		}
		schemaBase := base
		if id, ok := object["$id"].(string); ok {
			if parsed, err := url.Parse(id); err == nil {
				if schemaBase != nil {
					parsed = schemaBase.ResolveReference(parsed)
				}
				schemaBase = parsed
				resourceKey = artifactResourceKey(parsed)
				if scopes[resourceKey] == nil {
					scopes[resourceKey] = &rawSchemaResourceScope{root: object, anchors: map[string]any{}, base: cloneURL(parsed), pointerBases: map[string]*url.URL{}}
				}
				resourcePointer = ""
			}
		}
		scope := scopes[resourceKey]
		scope.pointerBases[resourcePointer] = cloneURL(schemaBase)
		if physical := scopes[rootKey]; physical != nil {
			physical.pointerBases[physicalPointer] = cloneURL(schemaBase)
		}
		for _, keyword := range []string{"$anchor", "$dynamicAnchor"} {
			if anchor, ok := object[keyword].(string); ok && anchor != "" {
				if _, exists := scope.anchors[anchor]; exists {
					scope.anchors[anchor] = nil // ambiguous source: never choose by map iteration
				} else {
					scope.anchors[anchor] = object
				}
			}
		}
		for key, child := range object {
			physicalChild := physicalPointer + "/" + escapeJSONPointerSegment(key)
			resourceChild := resourcePointer + "/" + escapeJSONPointerSegment(key)
			switch {
			case rawSchemaMapKeywords[key]:
				children, _ := child.(map[string]any)
				for name, nested := range children {
					segment := "/" + escapeJSONPointerSegment(name)
					visitSchema(nested, schemaBase, resourceKey, resourceChild+segment, physicalChild+segment)
				}
			case rawSchemaArrayKeywords[key]:
				for index, nested := range rawSliceValues(child) {
					segment := "/" + strconv.Itoa(index)
					visitSchema(nested, schemaBase, resourceKey, resourceChild+segment, physicalChild+segment)
				}
			case rawSchemaSingleKeywords[key]:
				visitSchema(child, schemaBase, resourceKey, resourceChild, physicalChild)
			}
		}
	}

	rawForEachSchemaRoot(root, func(schema any, pointer string) { visitSchema(schema, retrieval, rootKey, pointer, pointer) })
	return scopes
}

func rawForEachSchemaRoot(root any, visit func(any, string)) {
	object, _ := root.(map[string]any)
	if object == nil || object["openapi"] == nil {
		visit(root, "")
		return
	}
	components, _ := object["components"].(map[string]any)
	if components != nil {
		if schemas, ok := components["schemas"].(map[string]any); ok {
			for name, schema := range schemas {
				visit(schema, "/components/schemas/"+escapeJSONPointerSegment(name))
			}
		}
		for _, entry := range []struct {
			key  string
			kind rawRefTargetKind
		}{{"parameters", rawParameterTarget}, {"headers", rawHeaderTarget}, {"requestBodies", rawRequestBodyTarget}, {"responses", rawResponseTarget}, {"callbacks", rawCallbackTarget}, {"pathItems", rawPathItemTarget}} {
			if targets, ok := components[entry.key].(map[string]any); ok {
				for name, target := range targets {
					rawForEachSchemaInTarget(target, entry.kind, "/components/"+escapeJSONPointerSegment(entry.key)+"/"+escapeJSONPointerSegment(name), visit)
				}
			}
		}
	}
	for _, key := range []string{"paths", "webhooks"} {
		if targets, ok := object[key].(map[string]any); ok {
			for name, pathItem := range targets {
				rawForEachSchemaInTarget(pathItem, rawPathItemTarget, "/"+escapeJSONPointerSegment(key)+"/"+escapeJSONPointerSegment(name), visit)
			}
		}
	}
}

func rawForEachSchemaInTarget(value any, kind rawRefTargetKind, pointer string, visit func(any, string)) {
	if kind == rawSchemaTarget {
		visit(value, pointer)
		return
	}
	object, _ := value.(map[string]any)
	if object == nil {
		return
	}
	visitContent := func(value any, contentPointer string) {
		mediaValues, _ := value.(map[string]any)
		for mediaName, mediaValue := range mediaValues {
			media, _ := mediaValue.(map[string]any)
			if media == nil {
				continue
			}
			mediaPointer := contentPointer + "/" + escapeJSONPointerSegment(mediaName)
			visit(media["schema"], mediaPointer+"/schema")
			if encodings, ok := media["encoding"].(map[string]any); ok {
				for encodingName, encodingValue := range encodings {
					encoding, _ := encodingValue.(map[string]any)
					if encoding == nil {
						continue
					}
					if headers, ok := encoding["headers"].(map[string]any); ok {
						for headerName, header := range headers {
							rawForEachSchemaInTarget(
								header,
								rawHeaderTarget,
								mediaPointer+"/encoding/"+escapeJSONPointerSegment(encodingName)+"/headers/"+escapeJSONPointerSegment(headerName),
								visit,
							)
						}
					}
				}
			}
		}
	}
	switch kind {
	case rawParameterTarget, rawHeaderTarget:
		visit(object["schema"], pointer+"/schema")
		visitContent(object["content"], pointer+"/content")
	case rawRequestBodyTarget:
		visitContent(object["content"], pointer+"/content")
	case rawResponseTarget:
		if headers, ok := object["headers"].(map[string]any); ok {
			for name, header := range headers {
				rawForEachSchemaInTarget(header, rawHeaderTarget, pointer+"/headers/"+escapeJSONPointerSegment(name), visit)
			}
		}
		visitContent(object["content"], pointer+"/content")
	case rawCallbackTarget:
		for name, pathItem := range object {
			rawForEachSchemaInTarget(pathItem, rawPathItemTarget, pointer+"/"+escapeJSONPointerSegment(name), visit)
		}
	case rawPathItemTarget:
		for index, parameter := range rawSliceValues(object["parameters"]) {
			rawForEachSchemaInTarget(parameter, rawParameterTarget, pointer+"/parameters/"+strconv.Itoa(index), visit)
		}
		for _, method := range httpMethods {
			operation, _ := object[method].(map[string]any)
			if operation == nil {
				continue
			}
			operationPointer := pointer + "/" + method
			for index, parameter := range rawSliceValues(operation["parameters"]) {
				rawForEachSchemaInTarget(parameter, rawParameterTarget, operationPointer+"/parameters/"+strconv.Itoa(index), visit)
			}
			rawForEachSchemaInTarget(operation["requestBody"], rawRequestBodyTarget, operationPointer+"/requestBody", visit)
			if responses, ok := operation["responses"].(map[string]any); ok {
				for name, response := range responses {
					rawForEachSchemaInTarget(response, rawResponseTarget, operationPointer+"/responses/"+escapeJSONPointerSegment(name), visit)
				}
			}
			if callbacks, ok := operation["callbacks"].(map[string]any); ok {
				for name, callback := range callbacks {
					rawForEachSchemaInTarget(callback, rawCallbackTarget, operationPointer+"/callbacks/"+escapeJSONPointerSegment(name), visit)
				}
			}
		}
	}
}
