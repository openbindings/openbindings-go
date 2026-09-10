package jsonschema

// OpenBindings Project correction: bound numeric work at its actual predicate
// entry, not at JSON admission. The existing evaluator still owns all traversal,
// branch selection, references and annotation state. A budget refusal aborts the
// whole evaluation; it must not become a failed speculative branch.
import "github.com/openbindings/openbindings-go/jsonvalue"

func checkNumericWork(value any) {
	if err := jsonvalue.CheckNumericWork(value); err != nil {
		panic(numericWorkRefusal{err})
	}
}

type numericWorkRefusal struct{ cause error }

func recoverNumericCapability(err *error) {
	if cause := recover(); cause != nil {
		if refusal, ok := cause.(numericWorkRefusal); ok {
			*err = refusal.cause
			return
		}
		panic(cause) // never mask an unrelated backend bug or host panic
	}
}

// Existing regex engines retain their bool-only contract. The SDK's bounded
// engine provides this optional seam so timeout/refusal is not a false match.
func matchRegexp(re Regexp, value string) bool {
	if bounded, ok := re.(interface{ MatchStringError(string) (bool, error) }); ok {
		matched, err := bounded.MatchStringError(value)
		if err != nil {
			panic(numericWorkRefusal{err})
		}
		return matched
	}
	return re.MatchString(value)
}
