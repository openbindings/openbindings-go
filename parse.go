package openbindings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// ParseDocument validates raw JSON bytes against the OBI schema, then unmarshals into an Interface.
func ParseDocument(data []byte) (*Interface, error) {
	// OBI-D-01: an OBI document is UTF-8 encoded JSON. encoding/json
	// tolerates invalid byte sequences (replacing them with U+FFFD), so
	// check encoding validity explicitly.
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("parse document: invalid JSON: input is not valid UTF-8 (OBI-D-01)")
	}

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

	// OBI-T-04 (spec §11.1): refuse to PARSE a document declaring a higher
	// major version (pre-1.0: higher minor) than this SDK's MaxTested. This
	// must hold on every entry point (ParseDocument, ValidateDocument,
	// FetchInterface), not only Interface.Validate. The schema pattern above
	// already rejects a malformed-version string, so a bad version surfaces as
	// a schema error first; here the value is well-formed SemVer. The error is
	// emitted identically to Interface.Validate so the diagnostic is the same
	// regardless of entry point. Because ParseDocument fails first, callers
	// that go on to Interface.Validate never double-report.
	// The higher/lower/prerelease refusal (below MinSupported and pre-1.0
	// lower minor as well as the upward direction, plus unsupported
	// prereleases) is the same ordered decision Interface.Validate and
	// IsSupportedVersion make, via the shared versionRefusal predicate, with
	// identical messages.
	if msg, refused, err := versionRefusal(iface.OpenBindings); err != nil {
		return nil, &ValidationError{
			Problems: []string{fmt.Sprintf("openbindings: %v (OBI-T-04)", err)},
		}
	} else if refused {
		return nil, &ValidationError{
			Problems: []string{fmt.Sprintf("openbindings: %s (OBI-T-04)", msg)},
		}
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

// ValidateDocument is a convenience that calls ParseDocument followed by Validate.
func ValidateDocument(data []byte) (*Interface, error) {
	iface, err := ParseDocument(data)
	if err != nil {
		return nil, err
	}
	if err := iface.Validate(); err != nil {
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
