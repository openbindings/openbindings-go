package openbindings

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	json "github.com/openbindings/openbindings-go/internal/thirdparty/jsoncodec"

	"github.com/openbindings/openbindings-go/jsonvalue"
)

// ParseDocument decodes a document for use: it checks the exact input bytes
// (OBI-D-01), refuses an unsupported version (OBI-T-04), checks the embedded
// document schema (OBI-D-02), and unmarshals into an Interface. It is not a
// conformance check; ValidateDocument reports every document rule.
//
// The version decision comes first because the embedded schema is this
// version's: a document declaring an unsupported version is refused, not
// judged against rules it does not claim (§10.1). A refusal is a
// *VersionRefusalError and schema violations are a *ValidationError, as from
// Interface.Validate and ValidateDocument.
func ParseDocument(data []byte) (*Interface, error) {
	raw, err := decodeDocumentBytes(data)
	if err != nil {
		return nil, fmt.Errorf("parse document: invalid JSON: %w (OBI-D-01)", err)
	}
	if refusal := declaredVersionRefusal(raw); refusal != nil {
		return nil, refusal
	}
	var c ruleChecks
	validateAgainstOBISchema(&c, raw)
	if verr := c.violationError(); verr != nil {
		return nil, verr
	}
	var iface Interface
	if err := json.Unmarshal(data, &iface); err != nil {
		return nil, fmt.Errorf("parse document: the document model cannot carry it: %w", err)
	}
	return &iface, nil
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
