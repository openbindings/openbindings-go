package openbindings

import (
	"errors"
	"fmt"
)

// ParseDocument decodes a document for use: it checks the exact input bytes
// (OBI-01), refuses a version this SDK does not apply (CheckVersion),
// checks the embedded document schema (OBI-02), and decodes the model. It
// is not a conformance check; ValidateDocument reports every document rule.
//
// The version decision comes first because the embedded schema is this
// version's: a document declaring an unsupported version is refused, not
// judged against rules it does not claim (§10.1). OBI-01 and the declared
// version are read however deep the input nests. An error is one of three:
//   - a *VersionRefusalError, the refusal;
//   - a *ValidationError listing violations of OBI-01 or of the document
//     schema, as Document.Validate and ValidateDocument report them;
//   - an error matching ErrInconclusive, when this SDK cannot read the
//     document in full: input nested deeper than the decoder reads, a string
//     escaping a lone UTF-16 surrogate, a document the model does not carry,
//     or, should it ever happen, the schema library reaching no verdict
//     against the document schema. It concludes nothing about the document;
//     ValidateDocument reports the rules it can decide.
func ParseDocument(data []byte) (*Document, error) {
	raw, err := decodeDocumentBytes(data)
	if err != nil {
		if refusal := inputVersionRefusal(data); refusal != nil {
			return nil, refusal
		}
		if errors.Is(err, errNestingLimit) {
			return nil, fmt.Errorf("%w: the input is %w, so it is not decoded", ErrInconclusive, err)
		}
		if lone := (*loneSurrogateError)(nil); errors.As(err, &lone) {
			return nil, fmt.Errorf("%w: %w", ErrInconclusive, err)
		}
		return nil, &ValidationError{Findings: []Finding{obi01Violation(data, err)}}
	}
	if refusal := declaredVersionRefusal(raw); refusal != nil {
		return nil, refusal
	}
	var c ruleChecks
	validateAgainstOBISchema(&c, raw)
	positionFindings(data, c.findings)
	if verr := c.violationError(); verr != nil {
		return nil, verr
	}
	for _, finding := range c.findings {
		if finding.Status == EvidenceInconclusive {
			return nil, fmt.Errorf("%w: the document schema could not be applied at %q: %s (OBI-02)", ErrInconclusive, finding.Path, finding.Message)
		}
	}
	var doc Document
	if err := doc.decodeVerified(data); err != nil { // OBI-01 verified the bytes
		return nil, fmt.Errorf("%w: the document model cannot carry it: %w", ErrInconclusive, err)
	}
	return &doc, nil
}

// decodeDocumentBytes applies OBI-01 to the exact input bytes: valid UTF-8,
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
