package openapi

import (
	openapiclient "github.com/openbindings/openapi-client/go"

	"github.com/getkin/kin-openapi/openapi3"
)

// resolvedDeclaration adapts the standalone client's OpenAPI-native schema
// analysis to the binding implementation's existing internal call sites.
// All resolution mechanics live in openapi-client; the copied fields are
// compatibility observations used by existing synthesis and coverage code.
type resolvedDeclaration struct {
	inner     openapiclient.SchemaDeclaration
	conjuncts []*openapi3.Schema
	types     map[string]bool
	ambiguous bool
	oas30     bool
}

func resolveDeclaration(schema *openapi3.Schema, oas30 bool) resolvedDeclaration {
	edition := "3.1"
	if oas30 {
		edition = "3.0"
	}
	return wrapResolvedDeclaration(openapiclient.ResolveSchemaDeclaration(schema, edition), oas30)
}

func wrapResolvedDeclaration(declaration openapiclient.SchemaDeclaration, oas30 bool) resolvedDeclaration {
	types := map[string]bool{}
	for _, member := range declaration.Types() {
		types[member] = true
	}
	return resolvedDeclaration{
		inner: declaration, conjuncts: declaration.Conjuncts(), types: types,
		ambiguous: declaration.Ambiguous(), oas30: oas30,
	}
}

func (d resolvedDeclaration) declaresOnly(allowed ...string) bool {
	return d.inner.DeclaresOnly(allowed...)
}

func (d resolvedDeclaration) admitsStringAsSoleNonNullType() bool {
	return d.inner.AdmitsStringAsSoleNonNullType()
}

func (d resolvedDeclaration) typeless() bool { return d.inner.Typeless() }

// admitsNoInstance: a boolean false conjunct or §5.2's empty intersection (delegated to the client engine).
func (d resolvedDeclaration) admitsNoInstance() bool { return d.inner.AdmitsNoInstance() }
func (d resolvedDeclaration) admitsNull() bool       { return d.inner.AdmitsNull() }
func (d resolvedDeclaration) soleNonNullType() (string, bool) {
	return d.inner.SoleNonNullType()
}
func (d resolvedDeclaration) format() (string, bool) { return d.inner.Format() }
func (d resolvedDeclaration) keywordString(key string) (string, bool) {
	return d.inner.KeywordString(key)
}
func (d resolvedDeclaration) propertyNames() []string { return d.inner.PropertyNames() }
func (d resolvedDeclaration) property(name string) resolvedDeclaration {
	return wrapResolvedDeclaration(d.inner.Property(name), d.oas30)
}
func (d resolvedDeclaration) requiresProperty(name string) bool {
	return d.inner.RequiresProperty(name)
}
func (d resolvedDeclaration) items() resolvedDeclaration {
	return wrapResolvedDeclaration(d.inner.Items(), d.oas30)
}
