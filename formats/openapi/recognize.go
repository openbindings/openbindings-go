package openapi

import (
	"strings"

	"github.com/openbindings/openapi-client/go/provider"
)

// RecognizeRepresentation maps native edition recognition to this adapter's
// exact supported binding identifier without treating invalid operations as an
// unrecognized format. It is an optional authoring helper, not invoker behavior.
func (*Adapter) RecognizeRepresentation(data []byte) (string, bool, error) {
	edition, claimed, err := provider.RecognizeRepresentation(data)
	if !claimed || err != nil {
		return "", claimed, err
	}
	switch {
	case edition == provider.EditionSwagger20:
		return BindingSpecOpenAPI20, true, nil
	case strings.HasPrefix(string(edition), "3.0."):
		return BindingSpecOpenAPI30, true, nil
	case strings.HasPrefix(string(edition), "3.1."):
		return BindingSpecOpenAPI31, true, nil
	default:
		return BindingSpecOpenAPI32, true, nil
	}
}
