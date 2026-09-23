package openbindings

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"

	json "github.com/openbindings/openbindings-go/internal/thirdparty/jsoncodec"

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
// binding. It contains no runtime-supplied metadata. Selector is nil when the
// binding has no selector member, which its binding specification gives a
// meaning distinct from any present value (§5.3). Compare descriptors with
// Equal: Selector is a pointer, so == compares its identity.
type PreparedBindingDescriptor struct {
	Key           string
	OperationKey  string
	SourceKey     string
	BindingSpec   string
	Selector      *string
	HasTransforms bool
}

// Equal reports whether two descriptors describe the same binding, with
// selectors compared by presence and value.
func (d PreparedBindingDescriptor) Equal(other PreparedBindingDescriptor) bool {
	return d.Key == other.Key &&
		d.OperationKey == other.OperationKey &&
		d.SourceKey == other.SourceKey &&
		d.BindingSpec == other.BindingSpec &&
		(d.Selector == nil) == (other.Selector == nil) &&
		(d.Selector == nil || *d.Selector == *other.Selector) &&
		d.HasTransforms == other.HasTransforms
}

type preparedInterfaceState struct {
	snapshot     Interface
	encoded      []byte // the snapshot's encoding, from which copies are decoded
	view         any    // the snapshot's generic view, the root schemas compile against
	snapshotID   string
	operations   map[string]PreparedOperationDescriptor
	identifiers  map[string]string
	dependencies map[string]PreparedDependencyDescriptor
	bindings     map[string]PreparedBindingDescriptor

	mu         sync.Mutex
	schemas    *documentSchemas
	validators map[string]*CompiledSchema
}

// PreparedInterface is a validated, immutable, privately owned semantic
// snapshot of one OBI. Its document copy and mutable indexes are unexported;
// callers receive copied descriptors while schema compilation is shared.
type PreparedInterface struct {
	state *preparedInterfaceState
}

var nextSnapshotID atomic.Uint64

// PrepareInterface gates, snapshots, and indexes an OBI. It judges the
// document the interface encodes exactly as Interface.Validate does, refusing
// it with a *VersionRefusalError or a *ValidationError, and snapshots that
// same encoding, so it never freezes or retains the caller's maps. An
// interface that cannot be encoded, or whose encoding the document model
// cannot carry, is refused with that error.
func PrepareInterface(iface *Interface) (*PreparedInterface, error) {
	if iface == nil {
		return nil, fmt.Errorf("openbindings: interface is required")
	}
	if refusal := versionRefusalOf(iface.OpenBindings); refusal != nil {
		return nil, refusal
	}
	encoded, err := jsonvalue.Marshal(iface)
	if err != nil {
		return nil, fmt.Errorf("openbindings: encode interface: %w", err)
	}
	var view any
	if err := jsonvalue.Unmarshal(encoded, &view); err != nil {
		return nil, fmt.Errorf("openbindings: decode encoded interface: %w", err)
	}
	var c ruleChecks
	checkDocument(&c, view)
	if err := c.violationError(); err != nil {
		return nil, err
	}
	var snapshot Interface
	if err := json.Unmarshal(encoded, &snapshot); err != nil {
		return nil, fmt.Errorf("openbindings: the document model cannot carry this interface: %w", err)
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
			Selector:      clonePointer(binding.Selector),
			HasTransforms: binding.InputTransform != nil || binding.OutputTransform != nil,
		}
	}

	return &PreparedInterface{state: &preparedInterfaceState{
		snapshot:     snapshot,
		encoded:      encoded,
		view:         view,
		snapshotID:   fmt.Sprintf("snapshot:%d", nextSnapshotID.Add(1)),
		operations:   operations,
		identifiers:  identifiers,
		dependencies: dependencies,
		bindings:     bindings,
		validators:   make(map[string]*CompiledSchema),
	}}, nil
}

func clonePointer[T any](source *T) *T {
	if source == nil {
		return nil
	}
	value := *source
	return &value
}

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

// InterfaceSnapshot returns a private deep copy of the validated OBI snapshot,
// decoded afresh from the snapshot's encoding. Retained runtime artifacts call
// this once during deterministic closure and keep that copy; invocation hot
// paths never clone the document.
func (p *PreparedInterface) InterfaceSnapshot() *Interface {
	if p == nil || p.state == nil {
		return nil
	}
	var snapshot Interface
	if err := json.Unmarshal(p.state.encoded, &snapshot); err != nil {
		return nil // unreachable: preparation decoded these same bytes
	}
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
	descriptor.Selector = clonePointer(descriptor.Selector)
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
	if p.state.schemas == nil {
		schemas := collectDocumentSchemas(p.state.view)
		p.state.schemas = &schemas
	}
	validator, err = compileDocumentSchema(p.state.view, *p.state.schemas, "operations", key, position)
	if err != nil {
		return nil, false, &SchemaGraphUnavailableError{Cause: err}
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
