// Package schemacompiler is the SDK's use of the JSON Schema library
// santhosh-tekuri/jsonschema/v6: the compiler every schema compilation starts
// from, the projection of the library's results onto the SDK's outcomes (an
// established mismatch or no verdict), and the resource limits it is not
// handed work beyond.
package schemacompiler

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/dlclark/regexp2/v2"
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
// validates nothing (OBI-T-08), so every external reference, file: and
// http(s) alike, is unavailable. The JSON Schema meta-schemas are built into
// the library and resolve without a loader.
//
// Patterns are ECMA-262 regular expressions with Unicode semantics, as
// OBI-T-08 reads a value's schemas and OBI-D-02 and OBI-D-10 read the
// schemas they apply (JSON Schema Core §6.4), through compilePattern.
//
// format never rejects a value: OBI-T-08 makes it an annotation where the
// dialect leaves its assertion optional, and the library otherwise asserts it
// under the drafts before 2019-09 (which a reference to their meta-schemas
// reaches) with no option to stop. Every format the library checks is
// registered to accept every value; "regex" goes through compilePattern,
// which never refuses a pattern.
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

// PatternMatchTimeout bounds one match of a pattern against one string. A
// match that meets it, or the engine's backtracking limit, is a resource limit
// met (§10.4): it establishes no match and no mismatch.
const PatternMatchTimeout = 100 * time.Millisecond

// matchFailures counts the matches that reached no answer. A validation that
// sees it change reaches no verdict (MatchFailures).
var matchFailures atomic.Uint64

// MatchFailures returns how many pattern matches have reached no answer, for
// a caller to compare before and after a validation: the library asks a
// pattern only whether it matches, so a match that reached no answer would
// otherwise read as a mismatch. The count is process-wide, so a validation
// running beside another whose match fails also reaches no verdict, which is
// conservative, never wrong.
func MatchFailures() uint64 { return matchFailures.Load() }

// CompilePattern compiles a pattern as an ECMA-262 regular expression with
// Unicode semantics (the u flag). It returns an error for a pattern that is
// not one, which has no meaning to evaluate, and for one this SDK does not
// evaluate: a Unicode property escape, or a pattern the engine does not
// compile.
func CompilePattern(expression string) (*regexp2.Regexp, error) {
	if err := checkUnicodePattern(expression); err != nil {
		return nil, err
	}
	re, err := regexp2.Compile(forEngine(expression), regexp2.ECMAScript|regexp2.Unicode)
	if err != nil {
		return nil, err
	}
	re.MatchTimeout = PatternMatchTimeout
	return re, nil
}

// compilePattern is the library's pattern engine. A pattern that is not an
// ECMA-262 regular expression with Unicode semantics compiles to an
// UncompiledPattern rather than failing: the library checks format "regex"
// with this engine, and a format never rejects a value. A caller refuses a
// schema holding such a pattern before the library evaluates it.
func compilePattern(expression string) (jsonschema.Regexp, error) {
	re, err := CompilePattern(expression)
	if err != nil {
		return UncompiledPattern{Source: expression, Cause: err}, nil
	}
	return ecmaPattern{re: re, source: expression}, nil
}

// ecmaPattern is a compiled pattern as the library consults it.
type ecmaPattern struct {
	re     *regexp2.Regexp
	source string
}

func (p ecmaPattern) String() string { return p.source }

func (p ecmaPattern) MatchString(s string) bool {
	matched, err := p.re.MatchString(s)
	if err != nil {
		matchFailures.Add(1)
		return false
	}
	return matched
}

// UncompiledPattern is a pattern that is not an ECMA-262 regular expression
// with Unicode semantics. A schema holding one cannot be evaluated, and its
// caller refuses it before validating, so MatchString is never consulted for
// a verdict.
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
