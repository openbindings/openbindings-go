package graphql

import (
	"fmt"
	"regexp"
	"strings"
)

var graphqlName = regexp.MustCompile(`^[_A-Za-z][_0-9A-Za-z]*$`)

// parseSelector parses the selector forms shared by the supported GraphQL revisions:
// query/<field>, mutation/<field>, or subscription/<field>.
func parseSelector(selector string) (rootType string, fieldName string, err error) {
	if selector == "" {
		return "", "", fmt.Errorf("empty GraphQL selector")
	}
	idx := strings.Index(selector, "/")
	if idx < 0 || idx == 0 || idx == len(selector)-1 || strings.Contains(selector[idx+1:], "/") {
		return "", "", fmt.Errorf("GraphQL selector %q must be in the form query/fieldName, mutation/fieldName, or subscription/fieldName", selector)
	}
	rootType = selector[:idx]
	fieldName = selector[idx+1:]
	switch rootType {
	case "query", "mutation", "subscription":
		if !graphqlName.MatchString(fieldName) {
			return "", "", fmt.Errorf("GraphQL selector %q has invalid field name %q", selector, fieldName)
		}
		return rootType, fieldName, nil
	default:
		return "", "", fmt.Errorf("GraphQL selector %q has invalid operation kind %q (must be query, mutation, or subscription)", selector, rootType)
	}
}

// resolveField resolves a root-type field from the introspected schema.
// Errors when the schema has no such root type or the field is missing.
func resolveField(schema *introspectionSchema, rootType, fieldName string) (*field, error) {
	typeName := schema.rootTypeName(rootType)
	if typeName == "" {
		return nil, fmt.Errorf("schema has no %s root type", rootType)
	}
	tm := schema.typeMap()
	rootTypeObj, ok := tm[typeName]
	if !ok {
		return nil, fmt.Errorf("type %q not found in schema", typeName)
	}
	for i := range rootTypeObj.Fields {
		if rootTypeObj.Fields[i].Name == fieldName {
			return &rootTypeObj.Fields[i], nil
		}
	}
	return nil, fmt.Errorf("field %q not found on %s root type %q", fieldName, rootType, typeName)
}
