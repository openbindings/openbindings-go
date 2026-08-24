package grpc

import (
	"fmt"
	"sort"
	"strings"

	"github.com/openbindings/openbindings-go/synthesize"

	"google.golang.org/protobuf/reflect/protoreflect"

	openbindings "github.com/openbindings/openbindings-go"
)

func convertToInterface(disc *discovery, sourceLocation string, onWarning func(synthesize.SynthesizerWarning)) (openbindings.Interface, error) {
	if disc == nil {
		return openbindings.Interface{}, fmt.Errorf("nil discovery result")
	}

	sourceEntry := openbindings.Source{
		BindingSpec: BindingSpec,
	}
	if sourceLocation != "" {
		sourceEntry.Location = sourceLocation
	}

	iface := openbindings.Interface{
		OpenBindings: openbindings.MaxTestedVersion,
		Operations:   map[string]openbindings.Operation{},
		Bindings:     map[string]openbindings.BindingEntry{},
		Sources: map[string]openbindings.Source{
			DefaultSourceName: sourceEntry,
		},
	}

	usedKeys := map[string]string{}

	sort.Slice(disc.services, func(i, j int) bool {
		return string(disc.services[i].FullName()) < string(disc.services[j].FullName())
	})

	for _, svc := range disc.services {
		methods := serviceMethodsSorted(svc)
		for _, method := range methods {
			// The binding specification defines its accepted schema range per
			// bound method. A method outside that range is not bindable under
			// openbindings.grpc@1 and is accounted for by the coverage surface;
			// it must never become an OBI binding that invocation will
			// deterministically refuse.
			if err := validateBoundClosure(method); err != nil {
				continue
			}
			fqn := string(svc.FullName()) + "/" + string(method.Name())
			opKey := synthesize.SanitizeKey(string(method.Name()))
			opKey = synthesize.ResolveKeyCollision(opKey, string(svc.Name()), usedKeys)
			usedKeys[opKey] = fqn

			op := openbindings.Operation{
				Description: commentToDescription(method),
			}

			if inputType := method.Input(); inputType != nil {
				op.Input = newSchemaWalker(schemaInput, onWarning, "operations."+opKey+".input").root(inputType)
			}
			if outputType := method.Output(); outputType != nil {
				op.Output = newSchemaWalker(schemaOutput, onWarning, "operations."+opKey+".output").root(outputType)
			}

			iface.Operations[opKey] = op

			bindingKey := opKey + "." + DefaultSourceName
			iface.Bindings[bindingKey] = openbindings.BindingEntry{
				Operation: opKey,
				Source:    DefaultSourceName,
				Selector:  fqn,
			}
		}
	}

	if len(disc.services) > 0 {
		svc := disc.services[0]
		iface.Name = string(svc.Name())
		if len(disc.services) > 1 {
			iface.Name = packageName(svc)
		}
	}

	// gRPC/protobuf definitions do not expose security metadata, so we
	// leave the security section empty. If the server requires auth, the
	// invoker's auth retry flow will handle it (Unauthenticated → resolve
	// credentials → retry).

	return iface, nil
}

// serviceMethodsSorted returns the methods of a service in stable name order
// for deterministic output.
func serviceMethodsSorted(svc protoreflect.ServiceDescriptor) []protoreflect.MethodDescriptor {
	methods := svc.Methods()
	out := make([]protoreflect.MethodDescriptor, 0, methods.Len())
	for i := 0; i < methods.Len(); i++ {
		out = append(out, methods.Get(i))
	}
	sort.Slice(out, func(i, j int) bool {
		return string(out[i].Name()) < string(out[j].Name())
	})
	return out
}

func packageName(svc protoreflect.ServiceDescriptor) string {
	if file := svc.ParentFile(); file != nil {
		if pkg := string(file.Package()); pkg != "" {
			return pkg
		}
	}
	return string(svc.Name())
}

func commentToDescription(method protoreflect.MethodDescriptor) string {
	file := method.ParentFile()
	if file == nil {
		return ""
	}
	loc := file.SourceLocations().ByDescriptor(method)
	if loc.LeadingComments != "" {
		return trimComment(loc.LeadingComments)
	}
	return ""
}

func trimComment(s string) string {
	if len(s) > 0 && s[0] == ' ' {
		s = s[1:]
	}
	if len(s) > 0 && s[len(s)-1] == '\n' {
		s = s[:len(s)-1]
	}
	return s
}

// schemaWalker walks a proto message tree and produces JSON Schema. It holds
// traversal state (cycle detection, warning callback, OBI path) so individual
// walk methods stay focused on structural translation.
type schemaDirection uint8

const (
	schemaInput schemaDirection = iota
	schemaOutput
)

type schemaWalker struct {
	direction schemaDirection
	defs      map[string]any
	building  map[string]bool
	baseID    string
	onWarning func(synthesize.SynthesizerWarning)
	path      string
}

func newSchemaWalker(direction schemaDirection, onWarning func(synthesize.SynthesizerWarning), path string) *schemaWalker {
	return &schemaWalker{
		direction: direction,
		defs:      make(map[string]any),
		building:  make(map[string]bool),
		baseID:    "urn:openbindings:generated:grpc:" + path,
		onWarning: onWarning,
		path:      path,
	}
}

func (w *schemaWalker) warn(code, message string, details map[string]any) {
	if w.onWarning == nil {
		return
	}
	w.onWarning(synthesize.SynthesizerWarning{
		Code:    code,
		Message: message,
		Path:    w.path,
		Details: details,
	})
}

func (w *schemaWalker) root(msg protoreflect.MessageDescriptor) map[string]any {
	if wk := wellKnownSchema(string(msg.FullName()), w.direction); wk != nil {
		return wk
	}
	_ = w.messageReference(msg)
	definition, _ := w.defs[string(msg.FullName())].(map[string]any)
	root := make(map[string]any, len(definition)+2)
	for key, value := range definition {
		root[key] = value
	}
	root["$id"] = w.baseID
	root["$defs"] = w.defs
	return root
}

func (w *schemaWalker) messageReference(msg protoreflect.MessageDescriptor) map[string]any {
	fqn := string(msg.FullName())
	if wk := wellKnownSchema(fqn, w.direction); wk != nil {
		return wk
	}
	if _, exists := w.defs[fqn]; !exists && !w.building[fqn] {
		w.building[fqn] = true
		// Install the completed definition after the walk. Recursive edges
		// already point at this stable $defs address.
		w.defs[fqn] = w.messageDefinition(msg)
		delete(w.building, fqn)
	}
	return map[string]any{"$ref": w.baseID + "#/$defs/" + escapeJSONPointerToken(fqn)}
}

func (w *schemaWalker) messageDefinition(msg protoreflect.MessageDescriptor) map[string]any {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
	}

	fieldsDesc := msg.Fields()
	if fieldsDesc.Len() == 0 {
		return schema
	}

	var regularFields []protoreflect.FieldDescriptor
	oneofGroups := map[string][]protoreflect.FieldDescriptor{}
	var oneofOrder []string
	for i := 0; i < fieldsDesc.Len(); i++ {
		field := fieldsDesc.Get(i)
		oo := field.ContainingOneof()
		// Proto3 `optional` fields are wrapped in synthetic single-field
		// oneofs for explicit-presence tracking; they are not user-declared
		// unions and must not be emitted as oneOf variants.
		if oo == nil || oo.IsSynthetic() {
			regularFields = append(regularFields, field)
			continue
		}
		name := string(oo.Name())
		if _, seen := oneofGroups[name]; !seen {
			oneofOrder = append(oneofOrder, name)
		}
		oneofGroups[name] = append(oneofGroups[name], field)
	}

	properties := map[string]any{}
	var constraints []any
	for _, field := range regularFields {
		constraints = append(constraints, w.addProtoFieldProperties(properties, field, w.field(field))...)
	}
	for _, name := range oneofOrder {
		for _, field := range oneofGroups[name] {
			constraints = append(constraints, w.addProtoFieldProperties(properties, field, w.field(field))...)
		}
		constraints = append(constraints, oneofConstraints(oneofGroups[name], w.direction)...)
	}
	if len(properties) > 0 {
		schema["properties"] = properties
	}
	if len(constraints) > 0 {
		schema["allOf"] = constraints
	}

	return schema
}

func (w *schemaWalker) addProtoFieldProperties(properties map[string]any, field protoreflect.FieldDescriptor, schema map[string]any) []any {
	projected := schema
	if w.direction == schemaInput {
		projected = map[string]any{"anyOf": []any{schema, map[string]any{"type": "null"}}}
	}
	properties[field.JSONName()] = projected
	if protoName := string(field.Name()); protoName != field.JSONName() {
		if w.direction == schemaInput {
			properties[protoName] = projected
			// ProtoJSON accepts either spelling but rejects a duplicate field,
			// including the two aliases of one descriptor.
			return []any{map[string]any{"not": map[string]any{"required": []any{field.JSONName(), protoName}}}}
		}
	}
	return nil
}

func oneofConstraints(fields []protoreflect.FieldDescriptor, direction schemaDirection) []any {
	var constraints []any
	for left := 0; left < len(fields); left++ {
		for right := left + 1; right < len(fields); right++ {
			for _, leftName := range protoFieldSpellings(fields[left], direction) {
				for _, rightName := range protoFieldSpellings(fields[right], direction) {
					constraints = append(constraints, map[string]any{
						"not": map[string]any{
							"required": []any{leftName, rightName},
							"properties": map[string]any{
								leftName:  map[string]any{"not": map[string]any{"type": "null"}},
								rightName: map[string]any{"not": map[string]any{"type": "null"}},
							},
						},
					})
				}
			}
		}
	}
	return constraints
}

func protoFieldSpellings(field protoreflect.FieldDescriptor, direction schemaDirection) []string {
	names := []string{field.JSONName()}
	if protoName := string(field.Name()); direction == schemaInput && protoName != field.JSONName() {
		names = append(names, protoName)
	}
	return names
}

func escapeJSONPointerToken(value string) string {
	value = strings.ReplaceAll(value, "~", "~0")
	return strings.ReplaceAll(value, "/", "~1")
}

// wellKnownSchema returns the canonical JSON Schema for a google.protobuf.*
// well-known message type, matching proto3's JSON mapping. Returns nil for
// any other fully qualified name.
//
// Schemas describe the directional ProtoJSON boundary. Inputs admit an exact
// interoperable JSON-integer range plus the mapping's string carriage; outputs
// use the canonical string printer form. Downstream codegen can read
// format:int64 to pick a precision-preserving language type.
func wellKnownSchema(fqn string, directions ...schemaDirection) map[string]any {
	direction := schemaInput
	if len(directions) > 0 {
		direction = directions[0]
	}
	switch fqn {
	case "google.protobuf.Timestamp":
		return map[string]any{"type": "string", "format": "date-time"}
	case "google.protobuf.Duration":
		return map[string]any{
			"type":        "string",
			"description": "Duration in seconds with up to nine fractional digits, suffixed with 's'",
		}
	case "google.protobuf.FieldMask":
		return map[string]any{
			"type":        "string",
			"description": "Comma-separated list of fully-qualified field paths",
		}
	case "google.protobuf.Struct":
		return map[string]any{"type": "object"}
	case "google.protobuf.Value":
		return map[string]any{}
	case "google.protobuf.ListValue":
		return map[string]any{"type": "array"}
	case "google.protobuf.Empty":
		return map[string]any{"type": "object", "additionalProperties": false}
	case "google.protobuf.BoolValue":
		return map[string]any{"type": "boolean"}
	case "google.protobuf.StringValue":
		return map[string]any{"type": "string"}
	case "google.protobuf.BytesValue":
		return protoBytesSchema()
	case "google.protobuf.Int32Value", "google.protobuf.UInt32Value":
		if fqn == "google.protobuf.UInt32Value" {
			return map[string]any{"type": "integer", "minimum": 0, "maximum": uint64(4294967295)}
		}
		return map[string]any{"type": "integer", "minimum": int64(-2147483648), "maximum": int64(2147483647)}
	case "google.protobuf.Int64Value", "google.protobuf.UInt64Value":
		return integer64Schema(fqn == "google.protobuf.UInt64Value", direction)
	case "google.protobuf.FloatValue", "google.protobuf.DoubleValue":
		return protoFloatSchema()
	case "google.protobuf.Any":
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"@type": map[string]any{"type": "string"},
				"value": map[string]any{},
			},
			"required": []any{"@type"},
		}
	}
	return nil
}

func (w *schemaWalker) field(field protoreflect.FieldDescriptor) map[string]any {
	if field.IsMap() {
		valField := field.MapValue()
		schema := map[string]any{
			"type":                 "object",
			"additionalProperties": w.scalarOrMessage(valField),
		}
		if propertyNames := protoMapKeySchema(field.MapKey()); propertyNames != nil {
			schema["propertyNames"] = propertyNames
		}
		return schema
	}

	s := w.scalarOrMessage(field)

	if field.Cardinality() == protoreflect.Repeated && !field.IsMap() {
		return map[string]any{
			"type":  "array",
			"items": s,
		}
	}

	return s
}

func (w *schemaWalker) scalarOrMessage(field protoreflect.FieldDescriptor) map[string]any {
	switch field.Kind() {
	case protoreflect.BoolKind:
		return map[string]any{"type": "boolean"}

	case protoreflect.Int32Kind,
		protoreflect.Sint32Kind,
		protoreflect.Sfixed32Kind:
		return map[string]any{"type": "integer", "minimum": int64(-2147483648), "maximum": int64(2147483647)}

	case protoreflect.Uint32Kind,
		protoreflect.Fixed32Kind:
		return map[string]any{"type": "integer", "minimum": 0, "maximum": uint64(4294967295)}

	case protoreflect.Int64Kind,
		protoreflect.Sint64Kind,
		protoreflect.Sfixed64Kind:
		return integer64Schema(false, w.direction)

	case protoreflect.Uint64Kind,
		protoreflect.Fixed64Kind:
		return integer64Schema(true, w.direction)

	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return protoFloatSchema()

	case protoreflect.StringKind:
		return map[string]any{"type": "string"}
	case protoreflect.BytesKind:
		return protoBytesSchema()

	case protoreflect.EnumKind:
		enumDesc := field.Enum()
		if enumDesc != nil {
			if enumDesc.FullName() == "google.protobuf.NullValue" {
				return map[string]any{"type": "null"}
			}
			values := enumDesc.Values()
			out := make([]any, 0, values.Len())
			for i := 0; i < values.Len(); i++ {
				out = append(out, string(values.Get(i).Name()))
			}
			if w.direction == schemaOutput {
				return map[string]any{"type": "string", "enum": out}
			}
			return map[string]any{"anyOf": []any{
				map[string]any{"type": "string", "enum": out},
				map[string]any{"type": "integer", "minimum": int64(-2147483648), "maximum": int64(2147483647)},
			}}
		}
		return map[string]any{"type": "string"}

	case protoreflect.MessageKind, protoreflect.GroupKind:
		msgDesc := field.Message()
		if msgDesc != nil {
			return w.messageReference(msgDesc)
		}
		return map[string]any{"type": "object"}

	default:
		return map[string]any{"type": "string"}
	}
}

func integer64Schema(unsigned bool, direction schemaDirection) map[string]any {
	if direction == schemaOutput {
		pattern := `^-?(?:0|[1-9][0-9]*)$`
		if unsigned {
			pattern = `^(?:0|[1-9][0-9]*)$`
		}
		return map[string]any{"type": "string", "format": "int64", "pattern": pattern}
	}
	integer := map[string]any{
		"type":   "integer",
		"format": "int64",
		// Restrict the numeric spelling to the exact interoperable JSON
		// range. Every 64-bit value remains available through the string
		// branch below.
		"maximum": int64(9007199254740991),
	}
	if unsigned {
		integer["minimum"] = 0
	} else {
		integer["minimum"] = int64(-9007199254740991)
	}
	return map[string]any{"anyOf": []any{
		integer,
		// ProtoJSON accepts quoted integer forms (including exponent
		// notation) and performs the exact range/integrality check. JSON
		// Schema cannot express that arithmetic without rejecting valid
		// spellings, so the string branch stays deliberately open.
		map[string]any{
			"type":    "string",
			"format":  "int64",
			"pattern": `^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`,
		},
	}}
}

func protoMapKeySchema(field protoreflect.FieldDescriptor) map[string]any {
	if field == nil {
		return nil
	}
	switch field.Kind() {
	case protoreflect.BoolKind:
		return map[string]any{"enum": []any{"true", "false"}}
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return map[string]any{"pattern": `^-?(?:0|[1-9][0-9]*)$`}
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return map[string]any{"pattern": `^(?:0|[1-9][0-9]*)$`}
	default:
		return nil
	}
}

func protoBytesSchema() map[string]any {
	return map[string]any{
		"type": "string",
		// ProtoJSON accepts padded or unpadded standard and URL-safe base64.
		"pattern": `^(?:[A-Za-z0-9+/_-]{4})*(?:[A-Za-z0-9+/_-]{2}(?:==)?|[A-Za-z0-9+/_-]{3}=?)?$`,
	}
}

func protoFloatSchema() map[string]any {
	return map[string]any{"anyOf": []any{
		map[string]any{"type": "number"},
		map[string]any{"type": "string", "enum": []any{"NaN", "Infinity", "-Infinity"}},
	}}
}
