package openbindings

import (
	"bytes"
	"errors"
	"fmt"
	json "github.com/openbindings/openbindings-go/internal/thirdparty/jsoncodec"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/openbindings/openbindings-go/jsonvalue"
)

// ParseDocument decodes a document for use: it checks the exact input bytes
// (OBI-D-01) and the embedded document schema (OBI-D-02), refuses an
// unsupported version (OBI-T-04), and unmarshals into an Interface. It is not
// a conformance check; ValidateDocument reports every document rule.
func ParseDocument(data []byte) (*Interface, error) {
	// OBI-D-01: the exact input is UTF-8 JSON with no duplicate object keys
	// and no byte-order mark.
	raw, err := decodeDocumentBytes(data)
	if err != nil {
		return nil, fmt.Errorf("parse document: invalid JSON: %w (OBI-D-01)", err)
	}
	if verr := compiledOBISchema.Validate(raw); verr != nil {
		lines := splitSchemaError(verr)
		return nil, &ValidationError{
			Problems: prefixLines("schema validation", lines),
		}
	}

	var iface Interface
	if err := json.Unmarshal(data, &iface); err != nil {
		return nil, fmt.Errorf("parse document: %w", err)
	}

	// OBI-T-04: a document declaring a well-formed version outside this SDK's
	// supported set is refused rather than interpreted, on every entry point.
	// The schema pattern above already rejects a malformed version string, so
	// the value here is well-formed SemVer. The refusal is the same
	// *VersionRefusalError Interface.Validate and ValidateDocument return, so
	// the diagnostic does not depend on the entry point.
	if refusal := versionRefusalOf(iface.OpenBindings); refusal != nil {
		return nil, refusal
	}

	return &iface, nil
}

func prefixLines(prefix string, lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = fmt.Sprintf("%s: %s", prefix, l)
	}
	return out
}

// FormatValidationErrors returns a human-readable multi-line string from a ValidationError.
func FormatValidationErrors(err error) string {
	var ve *ValidationError
	if !asValidationError(err, &ve) {
		return err.Error()
	}
	return strings.Join(ve.Problems, "\n")
}

func asValidationError(err error, target **ValidationError) bool {
	if err == nil {
		return false
	}
	ve, ok := err.(*ValidationError)
	if ok {
		*target = ve
		return true
	}
	return false
}

// decodeDocumentBytes applies OBI-D-01 to the exact input bytes: valid UTF-8,
// no duplicate object keys in any object, and JSON with no leading
// byte-order mark. It returns the generic JSON view of the document.
func decodeDocumentBytes(data []byte) (any, error) {
	// encoding/json tolerates invalid byte sequences (replacing them with
	// U+FFFD), so check encoding validity explicitly.
	if !utf8.Valid(data) {
		return nil, errors.New("input is not valid UTF-8")
	}
	if err := rejectDuplicateObjectKeys(data); err != nil {
		return nil, err
	}
	var raw any
	if err := jsonvalue.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func rejectDuplicateObjectKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := scanJSONValue(dec); err != nil {
		return err
	}
	tok, err := dec.Token()
	if err == io.EOF {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("unexpected trailing token %v", tok)
}

func scanJSONValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}

	switch delim {
	case '{':
		seen := map[string]struct{}{}
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyTok.(string)
			if !ok {
				return fmt.Errorf("object key is not a string")
			}
			if _, dup := seen[key]; dup {
				return fmt.Errorf("duplicate object key %q", key)
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(dec); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return fmt.Errorf("expected object close, got %v", end)
		}
	case '[':
		for dec.More() {
			if err := scanJSONValue(dec); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return fmt.Errorf("expected array close, got %v", end)
		}
	default:
		return fmt.Errorf("unexpected delimiter %q", delim)
	}
	return nil
}

// IsOBInterface returns true if the given map looks like a valid OpenBindings
// interface document (has "openbindings" string and "operations" map).
func IsOBInterface(v map[string]any) bool {
	if v == nil {
		return false
	}
	_, hasOB := v["openbindings"].(string)
	_, hasOps := v["operations"].(map[string]any)
	return hasOB && hasOps
}
