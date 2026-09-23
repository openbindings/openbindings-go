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
// validates nothing (OBI-T-16), so every external reference, file: and
// http(s) alike, is unavailable. The JSON Schema meta-schemas are built into
// the library and resolve without a loader. Patterns use the library's
// regular expression engine, Go's regexp: a pattern it cannot compile leaves
// the schema uncompiled.
func New() *jsonschema.Compiler {
	c := jsonschema.NewCompiler()
	c.UseLoader(externalResourceRefusal{})
	return c
}

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
