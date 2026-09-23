// Package schemacompiler is the SDK's use of the JSON Schema library
// santhosh-tekuri/jsonschema/v6: the compiler every schema compilation starts
// from, the projection of the library's results onto the SDK's outcomes (an
// established mismatch or no verdict), and the resource limits it is not
// handed work beyond.
package schemacompiler

import (
	"fmt"
	"regexp"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// New returns the schema compiler every SDK schema compilation uses: the
// library's own compiler, used as the library documents it.
//
// It never obtains a schema resource from outside what the caller registers.
// The library's default loader reads file: URLs from local disk, which would
// let a document-supplied $ref make validation read the validating machine's
// files and would make a verdict depend on that machine. Core lets a tool
// decline external resources (§7), and a graph that cannot be fully resolved
// validates nothing (OBI-T-16), so every external reference, file: and
// http(s) alike, is unavailable. The JSON Schema meta-schemas are built into
// the library and resolve without a loader. Patterns use Go's regexp, the
// library's own engine; see UncompiledPattern for one it cannot compile.
//
// format never rejects a value: §5.2 makes it an annotation at an operation
// boundary whatever dialect a subschema declares, and the library otherwise
// asserts it under the drafts before 2019-09 (which a reference to their
// meta-schemas reaches) with no option to stop. Every format the library
// checks is registered to accept every value; "regex" goes through
// compilePattern, which never refuses a pattern.
func New() *jsonschema.Compiler {
	c := jsonschema.NewCompiler()
	c.UseLoader(externalResourceRefusal{})
	c.UseRegexpEngine(compilePattern)
	for _, name := range libraryFormats {
		c.RegisterFormat(&jsonschema.Format{Name: name, Validate: func(any) error { return nil }})
	}
	return c
}

// libraryFormats are the formats santhosh-tekuri/jsonschema v6.0.3 checks.
// An unlisted format is never checked.
var libraryFormats = []string{
	"json-pointer", "relative-json-pointer", "uuid", "duration", "period",
	"ipv4", "ipv6", "hostname", "email", "date", "time", "date-time",
	"uri", "iri", "uri-reference", "iri-reference", "uri-template", "semver",
}

// compilePattern compiles a pattern with Go's regexp. A pattern Go's regexp
// does not support, such as an ECMAScript lookahead, does not stop the
// compilation: it compiles to an UncompiledPattern, so the rest of the schema
// graph is still resolved and the caller can tell from the compiled graph
// that a pattern in it cannot be evaluated.
func compilePattern(expression string) (jsonschema.Regexp, error) {
	re, err := regexp.Compile(expression)
	if err != nil {
		return UncompiledPattern{Source: expression, Cause: err}, nil
	}
	return re, nil
}

// UncompiledPattern is a pattern Go's regexp could not compile. A schema
// graph holding one cannot be evaluated; the caller refuses it rather than
// validating against it, so MatchString is never consulted for a verdict.
type UncompiledPattern struct {
	Source string
	Cause  error
}

func (p UncompiledPattern) String() string            { return p.Source }
func (p UncompiledPattern) MatchString(s string) bool { return false }

// externalResourceRefusal is the loader for every SDK compiler: it declines
// every URL, so a reference outside the registered resources is reported as
// an unavailable schema graph rather than fetched or read.
type externalResourceRefusal struct{}

func (externalResourceRefusal) Load(url string) (any, error) { return nil, RefuseExternal(url) }

// RefuseExternal is the error with which the SDK's compilers decline a
// schema resource outside what the caller registered.
func RefuseExternal(url string) error {
	return fmt.Errorf("external schema resource %s is not obtained; only resources the document embeds resolve", url)
}
