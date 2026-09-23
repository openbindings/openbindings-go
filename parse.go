package openbindings

import (
	"errors"
	"fmt"
)

// ParseDocument decodes a document for use: it checks the exact input bytes
// (OBI-D-01), refuses an unsupported version (OBI-T-04), checks the embedded
// document schema (OBI-D-02), and unmarshals into an Interface. It is not a
// conformance check; ValidateDocument reports every document rule.
//
// The version decision comes first because the embedded schema is this
// version's: a document declaring an unsupported version is refused, not
// judged against rules it does not claim (§10.1). A refusal is a
// *VersionRefusalError, and violations of OBI-D-01 or the document schema are
// a *ValidationError, as from Interface.Validate and ValidateDocument. A
// document either check could not be applied to (input nested deeper than
// the decoder reads, or a binding preference beyond the numeric limits of
// schema evaluation) is not parsed, and returns another error, as is one
// holding an escape of a lone UTF-16 surrogate, which the document model
// does not carry.
func ParseDocument(data []byte) (*Interface, error) {
	raw, err := decodeDocumentBytes(data)
	if err != nil {
		if refusal := declaredVersionRefusal(declaredVersionOf(data)); refusal != nil {
			return nil, refusal
		}
		if errors.Is(err, errNestingLimit) {
			return nil, fmt.Errorf("parse document: the input is %w, so OBI-D-01 was not checked", err)
		}
		if lone := (*loneSurrogateError)(nil); errors.As(err, &lone) {
			return nil, fmt.Errorf("parse document: %w", err)
		}
		return nil, &ValidationError{Findings: []Finding{{Rule: "OBI-D-01", Status: EvidenceViolated, Message: fmt.Sprintf("not a JSON document this specification accepts: %v", err)}}}
	}
	if refusal := declaredVersionRefusal(raw); refusal != nil {
		return nil, refusal
	}
	var c ruleChecks
	validateAgainstOBISchema(&c, raw)
	if verr := c.violationError(); verr != nil {
		return nil, verr
	}
	for _, finding := range c.findings {
		if finding.Status == EvidenceInconclusive {
			return nil, fmt.Errorf("parse document: the document schema could not be applied at %q: %s (OBI-D-02)", finding.Path, finding.Message)
		}
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
	if err := unmarshalJSON(data, &view); err != nil {
		return nil, err
	}
	return view, nil
}
