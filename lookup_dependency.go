package openbindings

// ResolvedDependency is a named dependency and the exact local operation
// contract it references.
type ResolvedDependency struct {
	// Key is the dependency's local map key. Dependency keys have no alias form.
	Key string
	// Dependency is the declaration stored under Key.
	Dependency DependencyEntry
	// OperationKey is the canonical operation key stored by Dependency.
	OperationKey string
	Operation    Operation
}

// LookupDependency resolves a named dependency and its referenced operation.
//
// Lookup is exact: dependency keys do not participate in operation alias
// resolution, and the operation reference must be an exact key per OBI-D-19.
// A validated OBI therefore either resolves completely or has no result;
// malformed programmatically constructed values fail closed.
func LookupDependency(iface *Interface, key string) (ResolvedDependency, bool) {
	if iface == nil || iface.Dependencies == nil {
		return ResolvedDependency{}, false
	}
	dependency, ok := iface.Dependencies[key]
	if !ok || dependency.Operation == "" {
		return ResolvedDependency{}, false
	}
	operation, ok := iface.Operations[dependency.Operation]
	if !ok {
		return ResolvedDependency{}, false
	}
	return ResolvedDependency{
		Key:          key,
		Dependency:   dependency,
		OperationKey: dependency.Operation,
		Operation:    operation,
	}, true
}
