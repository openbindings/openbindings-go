package openapi

import (
	"crypto/rand"
	"reflect"
	"strconv"
	"strings"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"
)

// schemaOverlayMarker joins one raw Schema Object to the typed SchemaRef/
// Schema produced from it. It exists only in the loader's private normalized
// bytes and is removed before InternalizeRefs; it can therefore neither escape
// into an OBI nor affect invocation. If an artifact authors the same extension
// key, the sidecar retains and restores its original value.
const schemaOverlayMarker = "x-openbindings-internal-schema-overlay"

// rawSchemaOverlayCollector is owned by one synthesis load. kin-openapi's
// typed Schema model intentionally uses zero values for several JSON Schema
// fields, so marshaling cannot distinguish absence from an authored null,
// empty, zero, or false value. The collector keeps only those authorial
// presence facts (plus opaque x-* annotations), then binds them to typed object
// identity before InternalizeRefs rewrites reference topology.
//
// Both maps are instance-local and guarded because a loader may fetch parts of
// a closure concurrently. There is deliberately no process-global cache.
type rawSchemaOverlayCollector struct {
	mu        sync.Mutex
	namespace string
	next      uint64
	pending   map[string]map[string]any
	byRef     map[*openapi3.SchemaRef]map[string]any
	bySchema  map[*openapi3.Schema]map[string]any

	// externals records what each internalized component key was internalized
	// FROM. Internalization moves a component reached through an external
	// reference into the root document under a generated key; without this the
	// artifact's own name for it is unrecoverable, and a cut point would have
	// to be named by that generated key. See cutpoint_names.go.
	externals map[string]refIdentity
}

func newRawSchemaOverlayCollector() *rawSchemaOverlayCollector {
	return &rawSchemaOverlayCollector{
		namespace: rand.Text(),
		pending:   map[string]map[string]any{},
		byRef:     map[*openapi3.SchemaRef]map[string]any{},
		bySchema:  map[*openapi3.Schema]map[string]any{},
	}
}

// setExternalComponents records the internalization table for this load.
func (c *rawSchemaOverlayCollector) setExternalComponents(externals map[string]refIdentity) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.externals = externals
}

func (c *rawSchemaOverlayCollector) externalComponents() map[string]refIdentity {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.externals
}

// markRawSchema records the fields the typed representation would erase and
// injects a private correlation marker. It returns whether the raw resource
// changed. The caller invokes this only after OpenAPI-version-specific $ref
// sibling normalization, so ignored OAS 3.0 siblings can never be restored.
func (c *rawSchemaOverlayCollector) markRawSchema(schema map[string]any) bool {
	if c == nil || schema == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	overlay := authorialPresenceOverlay(schema)
	if len(overlay) == 0 {
		return false
	}
	c.next++
	id := c.namespace + "-" + strconv.FormatUint(c.next, 10)
	c.pending[id] = overlay
	schema[schemaOverlayMarker] = id
	return true
}

func authorialPresenceOverlay(schema map[string]any) map[string]any {
	overlay := map[string]any{}
	for key, value := range schema {
		if strings.HasPrefix(strings.ToLower(key), "x-") || erasedSchemaPresence(key, value) {
			overlay[key] = value
		}
	}
	return overlay
}

// erasedSchemaPresence is intentionally narrower than "all raw fields".
// Typed kin-openapi remains the operational authority; the raw sidecar only
// restores spellings whose values its MarshalJSON omits. In particular it
// never overlays structural $ref/allOf/oneOf content or non-empty assertion
// values from a second parser.
func erasedSchemaPresence(key string, value any) bool {
	switch key {
	case "default", "example", "const":
		return value == nil
	case "title", "format", "description", "pattern",
		"$schema", "$comment", "$id", "$anchor", "$dynamicRef", "$dynamicAnchor",
		"contentMediaType", "contentEncoding":
		text, ok := value.(string)
		return ok && text == ""
	case "uniqueItems", "nullable", "readOnly", "writeOnly", "allowEmptyValue", "deprecated":
		flag, ok := value.(bool)
		return ok && !flag
	case "minLength", "minItems", "minProperties":
		return rawNumberIsZero(value)
	case "enum", "required", "examples":
		items, ok := value.([]any)
		return ok && len(items) == 0
	case "properties", "patternProperties", "dependentSchemas", "dependentRequired", "$defs", "definitions":
		items, ok := value.(map[string]any)
		return ok && len(items) == 0
	default:
		return false
	}
}

func rawNumberIsZero(value any) bool {
	switch number := value.(type) {
	case int:
		return number == 0
	case int8:
		return number == 0
	case int16:
		return number == 0
	case int32:
		return number == 0
	case int64:
		return number == 0
	case uint:
		return number == 0
	case uint8:
		return number == 0
	case uint16:
		return number == 0
	case uint32:
		return number == 0
	case uint64:
		return number == 0
	case float32:
		return number == 0
	case float64:
		return number == 0
	default:
		return false
	}
}

// markRawSchemaTree walks only JSON Schema-bearing keywords. Annotation and
// extension payloads remain opaque even if they contain schema-shaped maps.
// Map identity terminates YAML aliases and any accidental raw cycles.
func (c *rawSchemaOverlayCollector) markRawSchemaTree(value any, seen map[uintptr]bool) bool {
	object, ok := value.(map[string]any)
	if !ok {
		return false // includes boolean schemas
	}
	identity := reflect.ValueOf(object).Pointer()
	if identity != 0 && seen[identity] {
		return false
	}
	if identity != 0 {
		seen[identity] = true
	}
	changed := c.markRawSchema(object)
	for key, child := range object {
		switch {
		// definitions/additionalItems are not decoded into typed child
		// SchemaRefs by this kin-openapi version. They already survive as
		// opaque extension data, so marking inside them would add a token no
		// typed-identity traversal could consume.
		case rawSchemaMapKeywords[key] && key != "definitions":
			for _, nested := range rawMapValues(child) {
				changed = c.markRawSchemaTree(nested, seen) || changed
			}
		case rawSchemaArrayKeywords[key]:
			for _, nested := range rawSliceValues(child) {
				changed = c.markRawSchemaTree(nested, seen) || changed
			}
		case rawSchemaSingleKeywords[key] && key != "additionalItems":
			changed = c.markRawSchemaTree(child, seen) || changed
		}
	}
	return changed
}

func (c *rawSchemaOverlayCollector) takeMarker(extensions map[string]any) map[string]any {
	if c == nil || extensions == nil {
		return nil
	}
	id, ok := extensions[schemaOverlayMarker].(string)
	if !ok {
		return nil
	}
	c.mu.Lock()
	overlay, ours := c.pending[id]
	c.mu.Unlock()
	if !ours {
		return nil
	}
	delete(extensions, schemaOverlayMarker)
	return overlay
}

// bindDocument consumes every private marker and pairs its raw overlay with
// typed identity before InternalizeRefs. The traversal follows every schema
// entrance supported by the typed OpenAPI model and is cycle-safe across
// recursive schemas, callbacks, and shared referenced objects.
func (c *rawSchemaOverlayCollector) bindDocument(doc *openapi3.T) {
	if c == nil || doc == nil {
		return
	}
	seenRefs := map[*openapi3.SchemaRef]bool{}
	seenSchemas := map[*openapi3.Schema]bool{}
	var visitRef func(*openapi3.SchemaRef)
	var visitSchema func(*openapi3.Schema)
	visitRef = func(ref *openapi3.SchemaRef) {
		if ref == nil || seenRefs[ref] {
			return
		}
		seenRefs[ref] = true
		if overlay := c.takeMarker(ref.Extensions); overlay != nil {
			c.byRef[ref] = overlay
			applyMissingOverlay(ref.Extensions, overlay)
		}
		visitSchema(ref.Value)
	}
	visitSchema = func(schema *openapi3.Schema) {
		if schema == nil || seenSchemas[schema] {
			return
		}
		seenSchemas[schema] = true
		if overlay := c.takeMarker(schema.Extensions); overlay != nil {
			c.bySchema[schema] = overlay
			// MarshalYAML begins with Extensions, then writes only non-zero
			// typed fields. Restoring the erased spellings here makes nested
			// schemas participate automatically in the ordinary typed marshal,
			// including schemas later moved by InternalizeRefs.
			applyMissingOverlay(schema.Extensions, overlay)
		}
		for _, refs := range []openapi3.SchemaRefs{schema.OneOf, schema.AnyOf, schema.AllOf, schema.PrefixItems} {
			for _, ref := range refs {
				visitRef(ref)
			}
		}
		for _, ref := range []*openapi3.SchemaRef{
			schema.Not, schema.Items, schema.AdditionalProperties.Schema,
			schema.Contains, schema.PropertyNames,
			schema.UnevaluatedItems.Schema, schema.UnevaluatedProperties.Schema,
			schema.If, schema.Then, schema.Else, schema.ContentSchema,
		} {
			visitRef(ref)
		}
		for _, schemas := range []openapi3.Schemas{
			schema.Properties, schema.PatternProperties, schema.DependentSchemas, schema.Defs,
		} {
			for _, ref := range schemas {
				visitRef(ref)
			}
		}
	}

	seenPathItems := map[*openapi3.PathItem]bool{}
	seenParameters := map[*openapi3.Parameter]bool{}
	seenRequestBodies := map[*openapi3.RequestBody]bool{}
	seenResponses := map[*openapi3.Response]bool{}
	seenCallbacks := map[*openapi3.Callback]bool{}
	var visitPathItem func(*openapi3.PathItem)
	var visitParameterRef func(*openapi3.ParameterRef)
	var visitHeaderRef func(*openapi3.HeaderRef)
	var visitRequestBodyRef func(*openapi3.RequestBodyRef)
	var visitResponseRef func(*openapi3.ResponseRef)
	var visitCallbackRef func(*openapi3.CallbackRef)
	visitContent := func(content openapi3.Content) {
		for _, media := range content {
			if media == nil {
				continue
			}
			visitRef(media.Schema)
			for _, encoding := range media.Encoding {
				if encoding == nil {
					continue
				}
				for _, header := range encoding.Headers {
					visitHeaderRef(header)
				}
			}
		}
	}
	visitParameterRef = func(ref *openapi3.ParameterRef) {
		if ref == nil || ref.Value == nil || seenParameters[ref.Value] {
			return
		}
		seenParameters[ref.Value] = true
		visitRef(ref.Value.Schema)
		visitContent(ref.Value.Content)
	}
	visitHeaderRef = func(ref *openapi3.HeaderRef) {
		if ref == nil || ref.Value == nil {
			return
		}
		visitParameterRef(&openapi3.ParameterRef{Value: &ref.Value.Parameter})
	}
	visitRequestBodyRef = func(ref *openapi3.RequestBodyRef) {
		if ref == nil || ref.Value == nil || seenRequestBodies[ref.Value] {
			return
		}
		seenRequestBodies[ref.Value] = true
		visitContent(ref.Value.Content)
	}
	visitResponseRef = func(ref *openapi3.ResponseRef) {
		if ref == nil || ref.Value == nil || seenResponses[ref.Value] {
			return
		}
		seenResponses[ref.Value] = true
		for _, header := range ref.Value.Headers {
			visitHeaderRef(header)
		}
		visitContent(ref.Value.Content)
	}
	visitCallbackRef = func(ref *openapi3.CallbackRef) {
		if ref == nil || ref.Value == nil || seenCallbacks[ref.Value] {
			return
		}
		seenCallbacks[ref.Value] = true
		for _, pathItem := range ref.Value.Map() {
			visitPathItem(pathItem)
		}
	}
	visitPathItem = func(pathItem *openapi3.PathItem) {
		if pathItem == nil || seenPathItems[pathItem] {
			return
		}
		seenPathItems[pathItem] = true
		for _, parameter := range pathItem.Parameters {
			visitParameterRef(parameter)
		}
		for _, operation := range pathItem.Operations() {
			for _, parameter := range operation.Parameters {
				visitParameterRef(parameter)
			}
			visitRequestBodyRef(operation.RequestBody)
			if operation.Responses != nil {
				for _, response := range operation.Responses.Map() {
					visitResponseRef(response)
				}
			}
			for _, callback := range operation.Callbacks {
				visitCallbackRef(callback)
			}
		}
	}

	if doc.Components != nil {
		for _, schema := range doc.Components.Schemas {
			visitRef(schema)
		}
		for _, parameter := range doc.Components.Parameters {
			visitParameterRef(parameter)
		}
		for _, header := range doc.Components.Headers {
			visitHeaderRef(header)
		}
		for _, body := range doc.Components.RequestBodies {
			visitRequestBodyRef(body)
		}
		for _, response := range doc.Components.Responses {
			visitResponseRef(response)
		}
		for _, callback := range doc.Components.Callbacks {
			visitCallbackRef(callback)
		}
	}
	if doc.Paths != nil {
		for _, pathItem := range doc.Paths.Map() {
			visitPathItem(pathItem)
		}
	}
	for _, pathItem := range doc.Webhooks {
		visitPathItem(pathItem)
	}
}

func (c *rawSchemaOverlayCollector) apply(ref *openapi3.SchemaRef, target map[string]any) {
	if c == nil || ref == nil || target == nil {
		return
	}
	if ref.Value != nil {
		applyMissingOverlay(target, c.bySchema[ref.Value])
	}
	applyMissingOverlay(target, c.byRef[ref])
}

func applyMissingOverlay(target, overlay map[string]any) {
	for key, value := range overlay {
		if _, present := target[key]; !present {
			target[key] = cloneOverlayValue(value, map[uintptr]any{})
		}
	}
}

// cloneOverlayValue gives every emitted operation schema ownership of the raw
// annotation data it receives. It copies aliased/cyclic maps and slices without
// recursively looping or relying on JSON round-tripping.
func cloneOverlayValue(value any, seen map[uintptr]any) any {
	switch typed := value.(type) {
	case map[string]any:
		identity := reflect.ValueOf(typed).Pointer()
		if clone, ok := seen[identity]; ok {
			return clone
		}
		clone := make(map[string]any, len(typed))
		seen[identity] = clone
		for key, child := range typed {
			clone[key] = cloneOverlayValue(child, seen)
		}
		return clone
	case []any:
		identity := reflect.ValueOf(typed).Pointer()
		if clone, ok := seen[identity]; ok {
			return clone
		}
		clone := make([]any, len(typed))
		seen[identity] = clone
		for index, child := range typed {
			clone[index] = cloneOverlayValue(child, seen)
		}
		return clone
	default:
		return value
	}
}
