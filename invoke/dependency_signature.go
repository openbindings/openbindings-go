package invoke

// DependencySignature is the typed identity of one named dependency in a
// consumer OBI. I and O are phantom compile-time types derived by codegen from
// the operation referenced by the dependency entry. At runtime the value
// carries only the dependency key; the OBI remains authoritative for the
// operation relationship and binding-specification constraints.
type DependencySignature[I, O any] struct {
	key string
}

// Key returns the dependency key this immutable signature names.
func (s DependencySignature[I, O]) Key() string { return s.key }

// NewDependencySignatureForOperation derives the dependency's phantom I/O
// types from an operation signature. Generated catalogs use this constructor,
// so they cannot independently assert a mismatched dependency contract.
func NewDependencySignatureForOperation[I, O any](key string, _ OperationSignature[I, O]) DependencySignature[I, O] {
	return DependencySignature[I, O]{key: key}
}

// NewDynamicDependencySignature constructs an untyped dependency identity.
func NewDynamicDependencySignature(key string) DependencySignature[any, any] {
	return DependencySignature[any, any]{key: key}
}

// NewUnsafeDependencySignature is the explicit escape hatch for callers that
// assert dependency I/O without a generated operation signature.
func NewUnsafeDependencySignature[I, O any](key string) DependencySignature[I, O] {
	return DependencySignature[I, O]{key: key}
}
