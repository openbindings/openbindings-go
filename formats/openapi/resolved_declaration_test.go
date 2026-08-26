package openapi

import (
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func declarationSchema(types ...string) *openapi3.Schema {
	schema := &openapi3.Schema{}
	if types != nil {
		declared := openapi3.Types(types)
		schema.Type = &declared
	}
	return schema
}

func TestResolvedDeclarationProcedure(t *testing.T) {
	object := declarationSchema("object")
	null := declarationSchema("null")
	stringOrNull := declarationSchema("string", "null")
	conditional := declarationSchema()
	conditional.Not = &openapi3.SchemaRef{Value: object}
	conditional.If = &openapi3.SchemaRef{Value: object}
	conditional.Then = &openapi3.SchemaRef{Value: object}

	tests := []struct {
		name       string
		schema     *openapi3.Schema
		oas30      bool
		onlyObject bool
		stringOnly bool
		typeless   bool
	}{
		{name: "typeless", schema: declarationSchema(), typeless: true},
		{name: "type array retained", schema: stringOrNull, stringOnly: true},
		{name: "single non-null anyOf branch", schema: &openapi3.Schema{AnyOf: openapi3.SchemaRefs{{Value: object}, {Value: null}}}, onlyObject: true},
		{name: "ambiguous choice proves no type", schema: &openapi3.Schema{OneOf: openapi3.SchemaRefs{{Value: object}, {Value: declarationSchema("string")}}}, typeless: true},
		{name: "allOf conjoins", schema: &openapi3.Schema{AllOf: openapi3.SchemaRefs{{Value: declarationSchema()}, {Value: object}}}, onlyObject: true},
		{name: "contradictory allOf proves no inhabited type", schema: &openapi3.Schema{AllOf: openapi3.SchemaRefs{{Value: object}, {Value: declarationSchema("string")}}}, typeless: true},
		{name: "not and conditionals do not participate", schema: conditional, typeless: true},
		{name: "3.0 nullable retains null", schema: &openapi3.Schema{Type: &openapi3.Types{"string"}, Nullable: true}, oas30: true, stringOnly: true},
		{name: "3.0 type array is not a declaration", schema: stringOrNull, oas30: true, typeless: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolved := resolveDeclaration(test.schema, test.oas30)
			if got := resolved.declaresOnly("object"); got != test.onlyObject {
				t.Fatalf("declares only object = %v, want %v (types=%v ambiguous=%v)", got, test.onlyObject, resolved.types, resolved.ambiguous)
			}
			if got := resolved.admitsStringAsSoleNonNullType(); got != test.stringOnly {
				t.Fatalf("admits string as sole non-null type = %v, want %v", got, test.stringOnly)
			}
			if test.typeless && len(resolved.types) != 0 {
				t.Fatalf("resolved types = %v, want no proved type", resolved.types)
			}
		})
	}
}

func TestResolvedDeclarationMembersConjoinAllOfAndSelectedChoice(t *testing.T) {
	schema := &openapi3.Schema{AllOf: openapi3.SchemaRefs{
		{Value: &openapi3.Schema{Type: &openapi3.Types{"object"}, Properties: openapi3.Schemas{
			"item": {Value: &openapi3.Schema{AnyOf: openapi3.SchemaRefs{{Value: declarationSchema("array")}, {Value: declarationSchema("null")}}}},
		}}},
		{Value: &openapi3.Schema{Properties: openapi3.Schemas{"other": {Value: declarationSchema("string")}}}},
	}}
	resolved := resolveDeclaration(schema, false)
	if !resolved.declaresOnly("object") {
		t.Fatalf("top-level types = %v, want object", resolved.types)
	}
	if got := resolved.property("item"); !got.declaresOnly("array") {
		t.Fatalf("selected property types = %v, want array", got.types)
	}
	if len(resolved.propertyNames()) != 2 {
		t.Fatalf("property names = %v", resolved.propertyNames())
	}
}
