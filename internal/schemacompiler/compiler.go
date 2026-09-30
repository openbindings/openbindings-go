// Package schemacompiler is core's private use of the JSON Schema library
// santhosh-tekuri/jsonschema/v6, for the document rules that evaluate fixed
// schemas (OBI-D-02 against the derived schema, OBI-D-10 against the 2020-12
// meta-schemas): the compiler they start from, the projection of the
// library's results onto a rule's evidence, and the resource limits it is
// not handed work beyond. It also holds the ECMA-262 pattern grammar core
// checks a value contract's patterns against (CheckPattern). Values are
// validated against value contracts by the evaluator an application
// supplies, never here.
package schemacompiler

import (
	"fmt"

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
// Patterns are read by the library's Go regexp engine. The fixed schemas the
// document rules evaluate hold only anchored patterns over ASCII classes,
// which mean the same under Go's syntax as under ECMA-262 with the u flag,
// the dialect OBI-D-02 and OBI-D-10 read them in (JSON Schema Core §6.4);
// TestFixedSchemaPatterns pins them.
//
// format never rejects a value: OBI-T-08 makes it an annotation where the
// dialect leaves its assertion optional, and the library otherwise asserts it
// under the drafts before 2019-09 (which a reference to their meta-schemas
// reaches) with no option to stop. Every format the library checks is
// registered to accept every value.
func New() *jsonschema.Compiler {
	c := jsonschema.NewCompiler()
	c.UseLoader(externalResourceRefusal{})
	for _, name := range append(libraryFormats, "regex") {
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

// externalResourceRefusal is the loader for every compiler here: it declines
// every URL, so nothing outside the registered resources is fetched or read.
type externalResourceRefusal struct{}

func (externalResourceRefusal) Load(url string) (any, error) { return nil, RefuseExternal(url) }

// RefuseExternal is the error with which the SDK's compilers decline a
// schema resource outside what the caller registered.
func RefuseExternal(url string) error {
	return fmt.Errorf("external schema resource %s is not obtained; only resources the document embeds resolve", url)
}
