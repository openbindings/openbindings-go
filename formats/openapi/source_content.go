package openapi

import (
	"encoding/json"
	"fmt"

	"github.com/openbindings/openbindings-go/jsonvalue"
)

// sourceContentBytes translates OpenAPI's string source representation to YAML
// text and passes other JSON values to the OpenAPI loader. The loader owns
// document-shape validation and its load-phase error classification.
func sourceContentBytes(content json.RawMessage) ([]byte, error) {
	switch jsonvalue.ContentKind(content) {
	case "string":
		var text string
		if err := json.Unmarshal(content, &text); err != nil {
			return nil, fmt.Errorf("decode OpenAPI source text: %w", err)
		}
		return []byte(text), nil
	case "absent":
		return nil, fmt.Errorf("OpenAPI source content is absent")
	}
	return content, nil
}
