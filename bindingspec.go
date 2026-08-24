package openbindings

// BindingSpecInfo describes a binding specification supported by an
// invoker, by exact identifier.
type BindingSpecInfo struct {
	BindingSpec string `json:"bindingSpec"`
	Description string `json:"description,omitempty"`
}

// BindingSpecVerdict is the authoritative support answer for one exact
// binding-specification identifier. Future fields may annotate a verdict but
// must never qualify Supported.
type BindingSpecVerdict struct {
	BindingSpec string `json:"bindingSpec"`
	Supported   bool   `json:"supported"`
}

// CheckBindingSpecs returns one strict support verdict for each unique input
// token, preserving first-occurrence order. Identifiers are compared by exact
// string equality: they are opaque, with no prefix, range, or version-algebra
// interpretation.
func CheckBindingSpecs(bindingSpecs []string, supported []BindingSpecInfo) []BindingSpecVerdict {
	warranted := make(map[string]struct{}, len(supported))
	for _, info := range supported {
		warranted[info.BindingSpec] = struct{}{}
	}

	seen := make(map[string]struct{}, len(bindingSpecs))
	verdicts := make([]BindingSpecVerdict, 0, len(bindingSpecs))
	for _, bindingSpec := range bindingSpecs {
		if _, duplicate := seen[bindingSpec]; duplicate {
			continue
		}
		seen[bindingSpec] = struct{}{}
		_, ok := warranted[bindingSpec]
		verdicts = append(verdicts, BindingSpecVerdict{
			BindingSpec: bindingSpec,
			Supported:   ok,
		})
	}
	return verdicts
}
