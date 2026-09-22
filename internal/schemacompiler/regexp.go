package schemacompiler

import (
	"time"

	"github.com/dlclark/regexp2/v2"
	"github.com/openbindings/openbindings-go/internal/thirdparty/jsonschema"
	"github.com/openbindings/openbindings-go/jsonvalue"
)

// RegexpEngine uses the existing ECMAScript Unicode implementation for
// the Core and Operation Graph JSON Schema boundaries.
func RegexpEngine(expression string) (jsonschema.Regexp, error) {
	re, err := regexp2.Compile(expression, regexp2.ECMAScript|regexp2.Unicode)
	if err != nil {
		return nil, err
	}
	re.MatchTimeout = 100 * time.Millisecond
	return schemaRegexp{re: re, source: expression}, nil
}

type schemaRegexp struct {
	re     *regexp2.Regexp
	source string
}

func (r schemaRegexp) String() string            { return r.source }
func (r schemaRegexp) MatchString(s string) bool { ok, _ := r.MatchStringError(s); return ok }
func (r schemaRegexp) MatchStringError(s string) (bool, error) {
	ok, err := r.re.MatchString(s)
	if err != nil {
		return false, &jsonvalue.CapabilityError{Operation: "regular-expression matching"}
	}
	return ok, nil
}
