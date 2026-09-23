package synthesize

// ContractSelector projects a binding's optional selector onto the
// synthesis contracts this package implements. Revision 0.2 of the
// interface-synthesizer contract requires a coverage entry's bindingSelector,
// and revision 0.1 of the source-inspector contract requires a bindable
// target's selector, so neither can tell the absent-selector case the core
// model keeps distinct (§5.3) from a present empty selector: absence is sent
// as "". A contract revision that makes the member optional removes this
// projection.
func ContractSelector(selector *string) string {
	if selector == nil {
		return ""
	}
	return *selector
}
