package openbindings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ParseDocument validates raw JSON bytes against the OBI schema, then unmarshals into an Interface.
func ParseDocument(data []byte) (*Interface, error) {
	if err := rejectDuplicateObjectKeys(data); err != nil {
		return nil, fmt.Errorf("parse document: invalid JSON: %w", err)
	}

	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse document: invalid JSON: %w", err)
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

	return &iface, nil
}

func prefixLines(prefix string, lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = fmt.Sprintf("%s: %s", prefix, l)
	}
	return out
}

// ValidateDocument is a convenience that calls ParseDocument followed by ValidateInterface.
func ValidateDocument(data []byte) (*Interface, error) {
	iface, err := ParseDocument(data)
	if err != nil {
		return nil, err
	}
	if err := iface.ValidateInterface(); err != nil {
		return iface, err
	}
	return iface, nil
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
