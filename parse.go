package openbindings

import (
	"fmt"

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
		if refusal := declaredVersionRefusal(declaredVersionOf(data)); refusal != nil {
			return nil, refusal
		}
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
	if err := iface.decodeVerified(data); err != nil { // OBI-D-01 verified the bytes
		return nil, fmt.Errorf("parse document: the document model cannot carry it: %w", err)
	}
	return &iface, nil
}

// decodeDocumentBytes applies OBI-D-01 to the exact input bytes: valid UTF-8,
// no duplicate object keys in any object, and JSON with no leading
// byte-order mark. It returns the generic JSON view of the document.
func decodeDocumentBytes(data []byte) (any, error) {
	if err := verifyExactJSON(data); err != nil {
		return nil, err
	}
	var view any
	if err := jsonvalue.Unmarshal(data, &view); err != nil {
		return nil, err
	}
	return view, nil
}
