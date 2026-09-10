package openapi

import (
	"encoding/json"

	"github.com/openbindings/openapi-client/go/provider"
)

// SourceContent prepares an embedded OpenAPI artifact using the client's source
// grammar and exact numeric resolution. The returned object is not a synthesized
// interface and has not undergone OAS validation or reference resolution.
func SourceContent(data []byte) (json.RawMessage, error) {
	return provider.SourceJSONImage(data)
}
