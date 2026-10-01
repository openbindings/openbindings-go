package openbindings

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

var (
	rawMessagePointerType = reflect.TypeFor[*json.RawMessage]()
	textMarshalerType     = reflect.TypeFor[interface{ MarshalText() ([]byte, error) }]()
	jsonSchemaType        = reflect.TypeFor[JSONSchema]()
)

// rawLeaves calls visit for each json.RawMessage a value holds, reached as
// encoding/json reaches it (through pointers, interfaces, maps, slices,
// arrays, and the exported fields of structs), with the reference tokens from
// the value to it, and returns the first error visit returns. It does not
// descend into another value that encodes itself, nor, unless intoOwn, into a
// type of this package, which checks its own JSON when it encodes. A nil
// json.RawMessage, which encoding/json writes as null, is not visited. open
// holds the containers being walked, so a value holding itself ends the walk
// there; encoding refuses it all the same.
func rawLeaves(v reflect.Value, intoOwn bool, at []string, open map[heldValue]bool, visit func(at []string, raw json.RawMessage) error) error {
	if !v.IsValid() {
		return nil
	}
	switch v.Type() {
	case rawMessageType:
		if raw := v.Interface().(json.RawMessage); raw != nil {
			return visit(at, raw)
		}
		return nil
	case rawMessagePointerType:
		if !v.IsNil() && *v.Interface().(*json.RawMessage) != nil {
			return visit(at, *v.Interface().(*json.RawMessage))
		}
		return nil
	}
	if v.Kind() != reflect.Interface && (v.Type().Implements(marshalerType) || v.Type().Implements(textMarshalerType)) && !(intoOwn && ownType(v.Type())) {
		return nil
	}
	if held, isContainer := heldBy(v); isContainer {
		if open[held] {
			return nil
		}
		open[held] = true
		defer delete(open, held)
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return nil
		}
		return rawLeaves(v.Elem(), intoOwn, at, open, visit)
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return nil // bytes, which hold no raw JSON
		}
		for i := range v.Len() {
			if err := rawLeaves(v.Index(i), intoOwn, append(at, fmt.Sprint(i)), open, visit); err != nil {
				return err
			}
		}
	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			if err := rawLeaves(iter.Value(), intoOwn, append(at, fmt.Sprint(iter.Key().Interface())), open, visit); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for i := range v.NumField() {
			field := v.Type().Field(i)
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if !field.IsExported() || name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			tokens := at
			if !field.Anonymous {
				tokens = append(at, name)
			}
			if err := rawLeaves(v.Field(i), intoOwn, tokens, open, visit); err != nil {
				return err
			}
		}
	}
	return nil
}

// verifyRawSchemas checks, before encoding, the raw JSON an OBI-defined
// object's schemas hold, as a json.RawMessage at a schema position or within
// a schema, as verifyRawMembers checks raw members: it must be JSON decoding
// would accept. So a limit of this SDK the text meets (nesting deeper than the
// decoder reads, a string escaping a lone UTF-16 surrogate) is found by core
// itself, before encoding/json compacts the text and refuses it as its own
// syntax error. Raw JSON a caller's own marshaler writes is the marshaler's,
// and is not checked here.
func verifyRawSchemas(typed any) error {
	value := reflect.ValueOf(typed)
	for _, field := range membersOf(value.Type()).fields {
		fieldType := value.Type().Field(field.index).Type
		if fieldType != jsonSchemaType && !(fieldType.Kind() == reflect.Map && fieldType.Elem() == jsonSchemaType) {
			continue
		}
		err := rawLeaves(value.Field(field.index), false, nil, map[heldValue]bool{}, func(at []string, raw json.RawMessage) error {
			if err := verifyExactJSON(raw); err != nil {
				return fmt.Errorf("%s: %w", strings.Join(append([]string{field.name}, at...), "/"), err)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}
