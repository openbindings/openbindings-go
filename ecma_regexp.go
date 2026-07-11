package openbindings

import (
	"time"

	"github.com/dlclark/regexp2"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ECMARegexpEngine is the schema-boundary regexp engine: JSON Schema
// 2020-12 specifies ECMA-262 regular expression semantics for `pattern`
// and `patternProperties`, which Go's RE2-based regexp cannot express
// (no lookahead/lookbehind). regexp2's ECMAScript mode provides the
// dialect; because it backtracks (RE2's linear-time guarantee is lost),
// every match carries a timeout so a document-supplied pattern cannot
// run away (the same bounded posture as the spec's schema processing
// limits).
func ECMARegexpEngine(expr string) (jsonschema.Regexp, error) {
	re, err := regexp2.Compile(expr, regexp2.ECMAScript)
	if err != nil {
		return nil, err
	}
	re.MatchTimeout = 100 * time.Millisecond
	return ecmaRegexp{re: re, source: expr}, nil
}

type ecmaRegexp struct {
	re     *regexp2.Regexp
	source string
}

func (r ecmaRegexp) String() string { return r.source }

func (r ecmaRegexp) MatchString(s string) bool {
	// A timeout (or internal error) reports no-match: the bounded
	// posture treats a runaway pattern as failing to match rather than
	// hanging the boundary.
	ok, err := r.re.MatchString(s)
	return err == nil && ok
}
