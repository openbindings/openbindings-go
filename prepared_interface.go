package openbindings

import (
	"fmt"
	json "github.com/openbindings/openbindings-go/internal/thirdparty/jsoncodec"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/openbindings/openbindings-go/jsonvalue"
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
	Selector      string
	HasTransforms bool
}

type preparedInterfaceState struct {
	snapshot     Interface
	snapshotID   string
	operations   map[string]PreparedOperationDescriptor
	identifiers  map[string]string
	dependencies map[string]PreparedDependencyDescriptor
	bindings     map[string]PreparedBindingDescriptor

	mu         sync.Mutex
	validators map[string]*CompiledSchema
}

// PreparedInterface is a validated, immutable, privately owned semantic
// snapshot of one OBI. Its document copy and mutable indexes are unexported;
// callers receive copied descriptors while schema compilation is shared.
type PreparedInterface struct {
	state *preparedInterfaceState
}

var nextSnapshotID atomic.Uint64

// PrepareInterface gates, snapshots, and indexes an OBI without requiring JCS.
// It refuses a document on any established violation of the document rules
// it checks, as Validate does, and never freezes or retains the caller's maps.
func PrepareInterface(iface *Interface) (*PreparedInterface, error) {
	if iface == nil {
		return nil, fmt.Errorf("openbindings: interface is required")
	}
	encoded, err := jsonvalue.Marshal(iface)
	if err != nil {
		return nil, fmt.Errorf("openbindings: encode interface: %w", err)
	}
	var snapshot Interface
	if err := jsonvalue.Unmarshal(encoded, &snapshot); err != nil {
		return nil, fmt.Errorf("openbindings: snapshot interface: %w", err)
	}
	if err := snapshot.validateWithDocument(nil, withoutDocumentSchemaValidation()); err != nil {
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
			Selector:      binding.Selector,
			HasTransforms: binding.InputTransform != nil || binding.OutputTransform != nil,
		}
	}

	return &PreparedInterface{state: &preparedInterfaceState{
		snapshot:     snapshot,
		snapshotID:   fmt.Sprintf("snapshot:%d", nextSnapshotID.Add(1)),
		operations:   operations,
		identifiers:  identifiers,
		dependencies: dependencies,
		bindings:     bindings,
		validators:   make(map[string]*CompiledSchema),
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
		return value // immutable exact token; not a request for Float64 conversion
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
	if err := jsonvalue.Unmarshal(encoded, &clone); err != nil {
		return value // likewise defensive; never authority for valid preparation
	}
	return clone
}

// Prepared returns the receiver. It is the Go idempotent preparation path: a
// prepared value never snapshots or validates itself again.
func (p *PreparedInterface) Prepared() *PreparedInterface { return p }

// SnapshotID is a process-local handle identity: it distinguishes one
// prepared snapshot from another within this process so runtime records
// (routes, provider descriptors) can say which handle they were built from.
// It is not document identity — two preparations of byte-identical
// documents get different IDs — and never equality, a content revision, or
// a persistent identifier.
func (p *PreparedInterface) SnapshotID() string {
	if p == nil || p.state == nil {
		return ""
	}
	return p.state.snapshotID
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
func (p *PreparedInterface) SchemaValidator(operationIdentifier, position string) (validator *CompiledSchema, found bool, err error) {
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

func copyPreparedOperation(value PreparedOperationDescriptor) PreparedOperationDescriptor {
	value.Identifiers = append([]string(nil), value.Identifiers...)
	value.BindingKeys = append([]string(nil), value.BindingKeys...)
	value.DependencyKeys = append([]string(nil), value.DependencyKeys...)
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
