package schemaeval

import (
	"fmt"
	"time"

	"github.com/dlclark/regexp2/v2"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// defaultPatternTimeout bounds one match of a pattern against one string when
// Options.PatternMatchTimeout is zero.
const defaultPatternTimeout = 100 * time.Millisecond

// patternEngine is the library's regexp engine: patterns matched as
// ECMA-262 regular expressions with the u flag, by regexp2 in ECMAScript
// mode behind the rewrites forEngine makes where it departs from ECMA-262.
// A pattern this engine cannot match as ECMA-262 does, and a match that meets
// its time limit, give no verdict where evaluation reaches them: the
// compiled pattern panics with a sentinel, which Validate recovers.
type patternEngine struct{ timeout time.Duration }

func (e patternEngine) compile(pattern string) (jsonschema.Regexp, error) {
	if err := checkUnicodePattern(pattern); err != nil {
		return undecidablePattern{pattern, fmt.Sprintf("the pattern %q is one this evaluator does not match as ECMA-262 does: %v", pattern, err)}, nil
	}
	re, err := regexp2.Compile(forEngine(pattern), regexp2.ECMAScript|regexp2.Unicode)
	if err != nil {
		return undecidablePattern{pattern, fmt.Sprintf("the pattern %q does not compile in this evaluator's engine: %v", pattern, err)}, nil
	}
	re.MatchTimeout = e.timeout
	return ecmaPattern{re, pattern}, nil
}

type ecmaPattern struct {
	re     *regexp2.Regexp
	source string
}

func (p ecmaPattern) String() string { return p.source }

func (p ecmaPattern) MatchString(s string) bool {
	matched, err := p.re.MatchString(s)
	if err != nil {
		panic(sentinel{fmt.Sprintf("matching the pattern %q reached no answer within this evaluator's limits: %v", p.source, err)})
	}
	return matched
}

// undecidablePattern is a pattern this evaluator does not match: matching it
// gives no verdict.
type undecidablePattern struct {
	source, reason string
}

func (p undecidablePattern) String() string          { return p.source }
func (p undecidablePattern) MatchString(string) bool { panic(sentinel{p.reason}) }

// sentinel is the panic by which evaluation reaching what this evaluator
// cannot decide stops, and Validate gives no verdict.
type sentinel struct{ reason string }
