package asyncapi

import (
	"encoding/json"
	"fmt"

	openbindings "github.com/openbindings/openbindings-go"
)

// sourceContentBytes translates AsyncAPI's string source representation to
// YAML text and passes other JSON values to the AsyncAPI loader. The loader
// owns document-shape validation and its error classification.
func sourceContentBytes(content json.RawMessage) ([]byte, error) {
	switch openbindings.ContentKind(content) {
	case "string":
		var text string
		if err := json.Unmarshal(content, &text); err != nil {
			return nil, fmt.Errorf("decode AsyncAPI source text: %w", err)
		}
		return []byte(text), nil
	case "absent":
		return nil, fmt.Errorf("AsyncAPI source content is absent")
	}
	return content, nil
}
