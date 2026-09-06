package openbindings

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/openbindings/openbindings-go/canonicaljson"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// PreparedOperationDescriptor is the immutable, index-only view of one
// canonical operation. Returned slices are private copies.
type PreparedOperationDescriptor struct {
	CanonicalKey   string
	Identifiers    []string
	BindingKeys    []string
	DependencyKeys []string
}

// PreparedDependencyDescriptor is one named consumption point resolved to its
// exact local operation. BindingSpecsPresent distinguishes an absent allow-list
// from an explicitly authored empty list.
type PreparedDependencyDescriptor struct {
	Key                 string
	OperationKey        string
	BindingSpecs        []string
	BindingSpecsPresent bool
}

// PreparedBindingDescriptor is the SDK-derived identity of one concrete OBI
// binding. It contains no runtime-supplied metadata.
type PreparedBindingDescriptor struct {
	Key           string
	OperationKey  string
	SourceKey     string
	BindingSpec   string
	Ref           string
	HasTransforms bool
}

// PreparedBoundaryContract is an exact authored boundary-graph identity.
// Complete is false when a reachable schema resource is not embedded in the
// OBI; preparation never fetches ambient resources.
type PreparedBoundaryContract struct {
	Revision              string
	Canonical             []byte
	Complete              bool
	UnavailableReferences []string
}

type preparedInterfaceState struct {
	snapshot     Interface
	canonical    []byte
	revision     string
	operations   map[string]PreparedOperationDescriptor
	identifiers  map[string]string
	dependencies map[string]PreparedDependencyDescriptor
	bindings     map[string]PreparedBindingDescriptor

	mu                sync.Mutex
	validators        map[string]*jsonschema.Schema
	boundaryContracts map[string]PreparedBoundaryContract
}

// PreparedInterface is a validated, immutable, content-addressed semantic
// snapshot of one OBI. Its document copy and mutable indexes are unexported;
// callers receive copied descriptors while schema compilation is shared.
type PreparedInterface struct {
	state *preparedInterfaceState
}

// PrepareInterface validates, canonicalizes, snapshots, and indexes an OBI.
// It never freezes or retains the caller's maps.
func PrepareInterface(iface *Interface, opts ...ValidateOption) (*PreparedInterface, error) {
	if iface == nil {
		return nil, fmt.Errorf("openbindings: interface is required")
	}
	canonical, err := canonicaljson.Marshal(iface)
	if err != nil {
		return nil, fmt.Errorf("openbindings: canonicalize interface: %w", err)
	}
	snapshot := clonePreparedInterface(*iface)
	validationOptions := append(append([]ValidateOption(nil), opts...), withoutDocumentSchemaValidation())
	if err := snapshot.validateWithDocument(nil, validationOptions...); err != nil {
		return nil, err
	}

	bindingKeysByOperation := make(map[string][]string)
	for _, key := range sortedBindingKeys(snapshot.Bindings) {
		binding := snapshot.Bindings[key]
		bindingKeysByOperation[binding.Operation] = append(bindingKeysByOperation[binding.Operation], key)
	}
	dependencyKeysByOperation := make(map[string][]string)
	for _, key := range sortedDependencyKeys(snapshot.Dependencies) {
		dependency := snapshot.Dependencies[key]
		dependencyKeysByOperation[dependency.Operation] = append(dependencyKeysByOperation[dependency.Operation], key)
	}

	operations := make(map[string]PreparedOperationDescriptor, len(snapshot.Operations))
	identifiers := make(map[string]string)
	for _, key := range sortedOperationKeys(snapshot.Operations) {
		op := snapshot.Operations[key]
		names := append([]string{key}, op.Aliases...)
		descriptor := PreparedOperationDescriptor{
			CanonicalKey:   key,
			Identifiers:    names,
			BindingKeys:    append([]string(nil), bindingKeysByOperation[key]...),
			DependencyKeys: append([]string(nil), dependencyKeysByOperation[key]...),
		}
		operations[key] = descriptor
		for _, name := range names {
			identifiers[name] = key
		}
	}

	dependencies := make(map[string]PreparedDependencyDescriptor, len(snapshot.Dependencies))
	for _, key := range sortedDependencyKeys(snapshot.Dependencies) {
		dependency := snapshot.Dependencies[key]
		dependencies[key] = PreparedDependencyDescriptor{
			Key:                 key,
			OperationKey:        dependency.Operation,
			BindingSpecs:        append([]string(nil), dependency.BindingSpecs...),
			BindingSpecsPresent: dependency.BindingSpecs != nil,
		}
	}

	bindings := make(map[string]PreparedBindingDescriptor, len(snapshot.Bindings))
	for _, key := range sortedBindingKeys(snapshot.Bindings) {
		binding := snapshot.Bindings[key]
		source := snapshot.Sources[binding.Source]
		bindings[key] = PreparedBindingDescriptor{
			Key:           key,
			OperationKey:  binding.Operation,
			SourceKey:     binding.Source,
			BindingSpec:   source.BindingSpec,
			Ref:           binding.Ref,
			HasTransforms: binding.InputTransform != nil || binding.OutputTransform != nil,
		}
	}

	digest := sha256.Sum256(canonical)
	return &PreparedInterface{state: &preparedInterfaceState{
		snapshot:          snapshot,
		canonical:         canonical,
		revision:          fmt.Sprintf("sha256:%x", digest),
		operations:        operations,
		identifiers:       identifiers,
		dependencies:      dependencies,
		bindings:          bindings,
		validators:        make(map[string]*jsonschema.Schema),
		boundaryContracts: make(map[string]PreparedBoundaryContract),
	}}, nil
}

func clonePreparedInterface(source Interface) Interface {
	clone := source
	clone.LosslessFields = clonePreparedLossless(source.LosslessFields)
	clone.Schemas = make(map[string]JSONSchema, len(source.Schemas))
	for key, schema := range source.Schemas {
		clone.Schemas[key] = clonePreparedJSON(schema)
	}
	clone.Operations = make(map[string]Operation, len(source.Operations))
	for key, operation := range source.Operations {
		copyOperation := operation
		copyOperation.Tags = clonePreparedStrings(operation.Tags)
		copyOperation.Aliases = clonePreparedStrings(operation.Aliases)
		copyOperation.Input = clonePreparedJSON(operation.Input)
		copyOperation.Output = clonePreparedJSON(operation.Output)
		copyOperation.LosslessFields = clonePreparedLossless(operation.LosslessFields)
		if operation.Idempotent != nil {
			value := *operation.Idempotent
			copyOperation.Idempotent = &value
		}
		copyOperation.Examples = make(map[string]OperationExample, len(operation.Examples))
		for exampleKey, example := range operation.Examples {
			copyExample := example
			copyExample.Input = clonePreparedJSON(example.Input)
			copyExample.Output = clonePreparedJSON(example.Output)
			copyExample.LosslessFields = clonePreparedLossless(example.LosslessFields)
			copyOperation.Examples[exampleKey] = copyExample
		}
		clone.Operations[key] = copyOperation
	}
	clone.Dependencies = make(map[string]DependencyEntry, len(source.Dependencies))
	for key, dependency := range source.Dependencies {
		copyDependency := dependency
		copyDependency.BindingSpecs = clonePreparedStrings(dependency.BindingSpecs)
		copyDependency.LosslessFields = clonePreparedLossless(dependency.LosslessFields)
		clone.Dependencies[key] = copyDependency
	}
	clone.Sources = make(map[string]Source, len(source.Sources))
	for key, value := range source.Sources {
		copySource := value
		copySource.Content = append(json.RawMessage(nil), value.Content...)
		copySource.LosslessFields = clonePreparedLossless(value.LosslessFields)
		clone.Sources[key] = copySource
	}
	clone.Bindings = make(map[string]BindingEntry, len(source.Bindings))
	for key, binding := range source.Bindings {
		copyBinding := binding
		if binding.Preference != nil {
			value := *binding.Preference
			copyBinding.Preference = &value
		}
		if binding.InputTransform != nil {
			value := *binding.InputTransform
			copyBinding.InputTransform = &value
		}
		if binding.OutputTransform != nil {
			value := *binding.OutputTransform
			copyBinding.OutputTransform = &value
		}
		copyBinding.LosslessFields = clonePreparedLossless(binding.LosslessFields)
		clone.Bindings[key] = copyBinding
	}
	clone.Transforms = make(map[string]Transform, len(source.Transforms))
	for key, transform := range source.Transforms {
		clone.Transforms[key] = transform
	}
	return clone
}

func clonePreparedLossless(source LosslessFields) LosslessFields {
	return LosslessFields{
		Extensions: clonePreparedRawMap(source.Extensions),
		Unknown:    clonePreparedRawMap(source.Unknown),
	}
}

func clonePreparedRawMap(source map[string]json.RawMessage) map[string]json.RawMessage {
	if source == nil {
		return nil
	}
	clone := make(map[string]json.RawMessage, len(source))
	for key, value := range source {
		clone[key] = append(json.RawMessage(nil), value...)
	}
	return clone
}

func clonePreparedStrings(source []string) []string {
	if source == nil {
		return nil
	}
	clone := make([]string, len(source))
	copy(clone, source)
	return clone
}

func clonePreparedJSON(value any) any {
	switch value := value.(type) {
	case map[string]any:
		clone := make(map[string]any, len(value))
		for key, member := range value {
			clone[key] = clonePreparedJSON(member)
		}
		return clone
	case []any:
		clone := make([]any, len(value))
		for index, member := range value {
			clone[index] = clonePreparedJSON(member)
		}
		return clone
	case []string:
		return clonePreparedStrings(value)
	case json.RawMessage:
		return append(json.RawMessage(nil), value...)
	}
	if _, ok := value.(json.Marshaler); ok {
		return clonePreparedEncodedJSON(value)
	}
	if _, ok := value.(json.Number); ok {
		return clonePreparedEncodedJSON(value)
	}

	// JSON-bearing OBI fields are intentionally `any`. Accept named map/slice
	// types and pointers without retaining caller-owned storage. This reflect
	// lane is cold for documents decoded by encoding/json (the fast cases
	// above) but closes the aliasing hole for manually constructed typed OBIs.
	raw := reflect.ValueOf(value)
	if !raw.IsValid() {
		return nil
	}
	switch raw.Kind() {
	case reflect.Interface, reflect.Pointer:
		if raw.IsNil() {
			return nil
		}
		return clonePreparedJSON(raw.Elem().Interface())
	case reflect.Map:
		if raw.IsNil() {
			return nil
		}
		if raw.Type().Key().Kind() == reflect.String {
			clone := make(map[string]any, raw.Len())
			iterator := raw.MapRange()
			for iterator.Next() {
				clone[iterator.Key().String()] = clonePreparedJSON(iterator.Value().Interface())
			}
			return clone
		}
	case reflect.Slice, reflect.Array:
		if raw.Kind() == reflect.Slice && raw.IsNil() {
			return nil
		}
		if raw.Type().Elem().Kind() == reflect.Uint8 {
			return clonePreparedEncodedJSON(value)
		}
		clone := make([]any, raw.Len())
		for index := range clone {
			clone[index] = clonePreparedJSON(raw.Index(index).Interface())
		}
		return clone
	case reflect.Bool, reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return value
	}

	// A custom JSON scalar/struct is uncommon, but canonicalization admitted
	// it by its encoded JSON meaning. Decode just this subtree so the snapshot
	// retains neither its pointers nor a custom mutable implementation object.
	return clonePreparedEncodedJSON(value)
}

func clonePreparedEncodedJSON(value any) any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return value // unreachable after the successful whole-document JCS gate
	}
	var clone any
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return value // likewise defensive; never authority for valid preparation
	}
	return clone
}

// Prepared returns the receiver. It is the Go idempotent preparation path: a
// prepared value never snapshots or validates itself again.
func (p *PreparedInterface) Prepared() *PreparedInterface { return p }

// Revision returns sha256:<hex> over RFC 8785 canonical OBI JSON.
func (p *PreparedInterface) Revision() string {
	if p == nil || p.state == nil {
		return ""
	}
	return p.state.revision
}

// CanonicalJSON returns a copy of the canonical OBI bytes.
func (p *PreparedInterface) CanonicalJSON() []byte {
	if p == nil || p.state == nil {
		return nil
	}
	return append([]byte(nil), p.state.canonical...)
}

// InterfaceSnapshot returns a private deep copy of the validated OBI snapshot.
// Retained runtime artifacts call this once during deterministic closure and
// keep that copy; invocation hot paths never clone the document.
func (p *PreparedInterface) InterfaceSnapshot() *Interface {
	if p == nil || p.state == nil {
		return nil
	}
	snapshot := clonePreparedInterface(p.state.snapshot)
	return &snapshot
}

// Operation resolves a canonical key or alias through the prepared index.
func (p *PreparedInterface) Operation(identifier string) (PreparedOperationDescriptor, bool) {
	if p == nil || p.state == nil {
		return PreparedOperationDescriptor{}, false
	}
	key, ok := p.state.identifiers[identifier]
	if !ok {
		return PreparedOperationDescriptor{}, false
	}
	descriptor := p.state.operations[key]
	return copyPreparedOperation(descriptor), true
}

// OperationKeys returns sorted canonical operation keys.
func (p *PreparedInterface) OperationKeys() []string {
	if p == nil || p.state == nil {
		return nil
	}
	keys := make([]string, 0, len(p.state.operations))
	for key := range p.state.operations {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Dependency looks up one exact dependency key.
func (p *PreparedInterface) Dependency(key string) (PreparedDependencyDescriptor, bool) {
	if p == nil || p.state == nil {
		return PreparedDependencyDescriptor{}, false
	}
	descriptor, ok := p.state.dependencies[key]
	if !ok {
		return PreparedDependencyDescriptor{}, false
	}
	descriptor.BindingSpecs = append([]string(nil), descriptor.BindingSpecs...)
	return descriptor, true
}

// DependencyKeys returns sorted exact dependency keys.
func (p *PreparedInterface) DependencyKeys() []string {
	if p == nil || p.state == nil {
		return nil
	}
	keys := make([]string, 0, len(p.state.dependencies))
	for key := range p.state.dependencies {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Binding returns one exact SDK-derived binding descriptor.
func (p *PreparedInterface) Binding(key string) (PreparedBindingDescriptor, bool) {
	if p == nil || p.state == nil {
		return PreparedBindingDescriptor{}, false
	}
	descriptor, ok := p.state.bindings[key]
	return descriptor, ok
}

// BindingKeys returns sorted exact binding keys.
func (p *PreparedInterface) BindingKeys() []string {
	if p == nil || p.state == nil {
		return nil
	}
	keys := make([]string, 0, len(p.state.bindings))
	for key := range p.state.bindings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// SchemaValidator compiles one operation boundary at most once. found is
// false for an unknown operation or absent/null schema.
func (p *PreparedInterface) SchemaValidator(operationIdentifier, position string) (validator *jsonschema.Schema, found bool, err error) {
	if p == nil || p.state == nil {
		return nil, false, fmt.Errorf("openbindings: prepared interface is required")
	}
	key, ok := p.state.identifiers[operationIdentifier]
	if !ok {
		return nil, false, nil
	}
	op := p.state.snapshot.Operations[key]
	var schema JSONSchema
	switch position {
	case "input":
		schema = op.Input
	case "output":
		schema = op.Output
	default:
		return nil, false, fmt.Errorf("openbindings: unknown schema position %q", position)
	}
	if schema == nil {
		return nil, false, nil
	}
	cacheKey := key + "\x00" + position
	p.state.mu.Lock()
	defer p.state.mu.Unlock()
	if validator := p.state.validators[cacheKey]; validator != nil {
		return validator, true, nil
	}
	validator, err = CompileOperationSchema(&p.state.snapshot, key, position)
	if err != nil {
		return nil, false, err
	}
	p.state.validators[cacheKey] = validator
	return validator, true, nil
}

// BoundaryContract computes and memoizes one exact authored boundary graph.
func (p *PreparedInterface) BoundaryContract(operationIdentifier string) (PreparedBoundaryContract, bool, error) {
	if p == nil || p.state == nil {
		return PreparedBoundaryContract{}, false, fmt.Errorf("openbindings: prepared interface is required")
	}
	key, ok := p.state.identifiers[operationIdentifier]
	if !ok {
		return PreparedBoundaryContract{}, false, nil
	}
	p.state.mu.Lock()
	defer p.state.mu.Unlock()
	if contract, ok := p.state.boundaryContracts[key]; ok {
		return copyBoundaryContract(contract), true, nil
	}
	contract, err := prepareBoundaryContract(&p.state.snapshot, key)
	if err != nil {
		return PreparedBoundaryContract{}, false, err
	}
	p.state.boundaryContracts[key] = contract
	return copyBoundaryContract(contract), true, nil
}

func copyPreparedOperation(value PreparedOperationDescriptor) PreparedOperationDescriptor {
	value.Identifiers = append([]string(nil), value.Identifiers...)
	value.BindingKeys = append([]string(nil), value.BindingKeys...)
	value.DependencyKeys = append([]string(nil), value.DependencyKeys...)
	return value
}

func copyBoundaryContract(value PreparedBoundaryContract) PreparedBoundaryContract {
	value.Canonical = append([]byte(nil), value.Canonical...)
	value.UnavailableReferences = append([]string(nil), value.UnavailableReferences...)
	return value
}

func sortedOperationKeys(values map[string]Operation) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedDependencyKeys(values map[string]DependencyEntry) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedBindingKeys(values map[string]BindingEntry) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func prepareBoundaryContract(iface *Interface, operationKey string) (PreparedBoundaryContract, error) {
	data, err := json.Marshal(iface)
	if err != nil {
		return PreparedBoundaryContract{}, err
	}
	var document any
	if err := json.Unmarshal(data, &document); err != nil {
		return PreparedBoundaryContract{}, err
	}
	op := iface.Operations[operationKey]
	anchors := collectSchemaAnchors(iface)
	resources := make(map[string]any)
	unavailableSet := make(map[string]struct{})
	visitedRefs := make(map[string]struct{})

	var visit func(any)
	visit = func(value any) {
		switch node := value.(type) {
		case []any:
			for _, child := range node {
				visit(child)
			}
		case map[string]any:
			for _, keyword := range []string{"$ref", "$dynamicRef"} {
				reference, ok := node[keyword].(string)
				if !ok {
					continue
				}
				if _, seen := visitedRefs[reference]; seen {
					continue
				}
				visitedRefs[reference] = struct{}{}
				target, ok := resolvePreparedReference(document, reference, anchors)
				if !ok {
					unavailableSet[reference] = struct{}{}
				} else {
					resources[reference] = target
					visit(target)
				}
			}
			for _, child := range node {
				visit(child)
			}
		}
	}
	inputPresent := op.InputPresent || op.Input != nil
	outputPresent := op.OutputPresent || op.Output != nil
	if inputPresent {
		visit(op.Input)
	}
	if outputPresent {
		visit(op.Output)
	}
	unavailable := make([]string, 0, len(unavailableSet))
	for reference := range unavailableSet {
		unavailable = append(unavailable, reference)
	}
	sort.Strings(unavailable)
	input := map[string]any{"present": inputPresent}
	if inputPresent {
		input["schema"] = op.Input
	}
	output := map[string]any{"present": outputPresent}
	if outputPresent {
		output["schema"] = op.Output
	}
	graph := map[string]any{
		"input":                 input,
		"output":                output,
		"resources":             resources,
		"unavailableReferences": unavailable,
	}
	canonical, err := canonicaljson.Marshal(graph)
	if err != nil {
		return PreparedBoundaryContract{}, err
	}
	digest := sha256.Sum256(canonical)
	return PreparedBoundaryContract{
		Revision:              fmt.Sprintf("sha256:%x", digest),
		Canonical:             canonical,
		Complete:              len(unavailable) == 0,
		UnavailableReferences: unavailable,
	}, nil
}

func collectSchemaAnchors(iface *Interface) map[string]any {
	anchors := make(map[string]any)
	var visit func(any)
	visit = func(value any) {
		switch node := value.(type) {
		case []any:
			for _, child := range node {
				visit(child)
			}
		case map[string]any:
			if id, ok := node["$id"].(string); ok {
				if _, exists := anchors[id]; !exists {
					anchors[id] = node
				}
			}
			for _, keyword := range []string{"$anchor", "$dynamicAnchor"} {
				if name, ok := node[keyword].(string); ok {
					key := "#" + name
					if _, exists := anchors[key]; !exists {
						anchors[key] = node
					}
				}
			}
			for _, child := range node {
				visit(child)
			}
		}
	}
	for _, schema := range iface.Schemas {
		visit(schema)
	}
	for _, operation := range iface.Operations {
		visit(operation.Input)
		visit(operation.Output)
	}
	return anchors
}

func resolvePreparedReference(document any, reference string, anchors map[string]any) (any, bool) {
	if reference == "#" {
		return document, true
	}
	if strings.HasPrefix(reference, "#/") {
		return resolvePreparedPointer(document, strings.TrimPrefix(reference, "#"))
	}
	if value, ok := anchors[reference]; ok {
		return value, true
	}
	if hash := strings.IndexByte(reference, '#'); hash >= 0 {
		resource, ok := anchors[reference[:hash]]
		if !ok {
			return nil, false
		}
		fragment := reference[hash+1:]
		if fragment == "" {
			return resource, true
		}
		if strings.HasPrefix(fragment, "/") {
			return resolvePreparedPointer(resource, fragment)
		}
		return findPreparedAnchor(resource, fragment)
	}
	return nil, false
}

func resolvePreparedPointer(root any, pointer string) (any, bool) {
	value := root
	for _, token := range strings.Split(pointer, "/")[1:] {
		key := strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		object, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		value, ok = object[key]
		if !ok {
			return nil, false
		}
	}
	return value, true
}

func findPreparedAnchor(root any, name string) (any, bool) {
	switch node := root.(type) {
	case []any:
		for _, child := range node {
			if value, ok := findPreparedAnchor(child, name); ok {
				return value, true
			}
		}
	case map[string]any:
		if node["$anchor"] == name || node["$dynamicAnchor"] == name {
			return node, true
		}
		for _, child := range node {
			if value, ok := findPreparedAnchor(child, name); ok {
				return value, true
			}
		}
	}
	return nil, false
}
