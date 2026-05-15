package grpc

import (
	"fmt"
	"sort"

	openbindings "github.com/openbindings/openbindings-go"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func convertToInterface(disc *discovery, sourceLocation string, onWarning func(openbindings.CreatorWarning)) (openbindings.Interface, error) {
	if disc == nil {
		return openbindings.Interface{}, fmt.Errorf("nil discovery result")
	}

	sourceEntry := openbindings.Source{
		Format: FormatToken,
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
			if method.IsStreamingClient() {
				continue
			}

			fqn := string(svc.FullName()) + "/" + string(method.Name())
			opKey := openbindings.SanitizeKey(string(method.Name()))
			opKey = openbindings.ResolveKeyCollision(opKey, string(svc.Name()), usedKeys)
			usedKeys[opKey] = fqn

			op := openbindings.Operation{
				Description: commentToDescription(method),
			}

			if inputType := method.Input(); inputType != nil {
				op.Input = newSchemaWalker(onWarning, "operations."+opKey+".input").message(inputType)
			}
			if outputType := method.Output(); outputType != nil {
				op.Output = newSchemaWalker(onWarning, "operations."+opKey+".output").message(outputType)
			}

			iface.Operations[opKey] = op

			bindingKey := opKey + "." + DefaultSourceName
			iface.Bindings[bindingKey] = openbindings.BindingEntry{
				Operation: opKey,
				Source:    DefaultSourceName,
				Ref:       fqn,
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
type schemaWalker struct {
	visited   map[string]bool
	onWarning func(openbindings.CreatorWarning)
	path      string
}

func newSchemaWalker(onWarning func(openbindings.CreatorWarning), path string) *schemaWalker {
	return &schemaWalker{
		visited:   make(map[string]bool),
		onWarning: onWarning,
		path:      path,
	}
}

func (w *schemaWalker) warn(code, message string, details map[string]any) {
	if w.onWarning == nil {
		return
	}
	w.onWarning(openbindings.CreatorWarning{
		Code:    code,
		Message: message,
		Path:    w.path,
		Details: details,
	})
}

func (w *schemaWalker) message(msg protoreflect.MessageDescriptor) map[string]any {
	fqn := string(msg.FullName())
	if w.visited[fqn] {
		return map[string]any{"type": "object"}
	}

	// Well-known proto types have canonical JSON representations per the
	// proto3 JSON mapping spec. Emit those directly instead of descending
	// into the message's fields — traversing Timestamp's `seconds`/`nanos`
	// produces a contract the invoker's protojson layer cannot accept.
	if wk := wellKnownSchema(fqn); wk != nil {
		return wk
	}

	w.visited[fqn] = true

	schema := map[string]any{
		"type": "object",
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

	// Single oneof group: emit top-level `oneOf` preserving exactly-one-of
	// semantics. Multiple oneof groups: the v0.1 schema profile rejects
	// `oneOf` inside `allOf` (schemaprofile/allof.go), and a Cartesian
	// expansion would incorrectly force one member per group. Fall back to
	// putting multi-group oneof fields in `properties` as independent
	// optional fields and surface a warning so callers know the emitted
	// OBI cannot enforce exclusivity. Multi-group messages will be
	// properly expressible when a future profile revision allows `oneOf`
	// inside `allOf`.
	useOneOf := len(oneofGroups) == 1

	if len(oneofGroups) > 1 {
		groupNames := make([]string, 0, len(oneofOrder))
		groupNames = append(groupNames, oneofOrder...)
		w.warn(
			"grpc.multi_group_oneof",
			fmt.Sprintf("message %s contains %d oneof groups; the v0.1 schema profile cannot express multi-group exclusivity, so members are emitted as independent optional properties", string(msg.Name()), len(oneofGroups)),
			map[string]any{
				"message": string(msg.FullName()),
				"groups":  groupNames,
			},
		)
	}

	properties := map[string]any{}
	for _, field := range regularFields {
		properties[field.JSONName()] = w.field(field)
	}
	if !useOneOf {
		for _, name := range oneofOrder {
			for _, field := range oneofGroups[name] {
				properties[field.JSONName()] = w.field(field)
			}
		}
	}
	if len(properties) > 0 {
		schema["properties"] = properties
	}

	if useOneOf {
		group := oneofGroups[oneofOrder[0]]
		variants := make([]any, 0, len(group))
		for _, field := range group {
			jsonName := field.JSONName()
			variants = append(variants, map[string]any{
				"type": "object",
				"properties": map[string]any{
					jsonName: w.field(field),
				},
				"required": []any{jsonName},
			})
		}
		schema["oneOf"] = variants
	}

	return schema
}

// wellKnownSchema returns the canonical JSON Schema for a google.protobuf.*
// well-known message type, matching proto3's JSON mapping. Returns nil for
// any other fully qualified name.
//
// Schemas describe semantic types, not wire encoding. 64-bit integers emit
// as {"type":"integer","format":"int64"}; the wire's choice of carrying them
// as JSON numbers, JSON strings, or protobuf varints is an invoker concern.
// Downstream codegen can read format:int64 to pick precision-preserving
// language types (TypeScript string, Go int64, Rust i64).
func wellKnownSchema(fqn string) map[string]any {
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
		return map[string]any{"type": "object"}
	case "google.protobuf.BoolValue":
		return map[string]any{"type": "boolean"}
	case "google.protobuf.StringValue":
		return map[string]any{"type": "string"}
	case "google.protobuf.BytesValue":
		return map[string]any{"type": "string"}
	case "google.protobuf.Int32Value", "google.protobuf.UInt32Value":
		return map[string]any{"type": "integer"}
	case "google.protobuf.Int64Value", "google.protobuf.UInt64Value":
		return map[string]any{"type": "integer", "format": "int64"}
	case "google.protobuf.FloatValue", "google.protobuf.DoubleValue":
		return map[string]any{"type": "number"}
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
		return map[string]any{
			"type":                 "object",
			"additionalProperties": w.scalarOrMessage(valField),
		}
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
		protoreflect.Sfixed32Kind,
		protoreflect.Uint32Kind,
		protoreflect.Fixed32Kind:
		return map[string]any{"type": "integer"}

	case protoreflect.Int64Kind,
		protoreflect.Sint64Kind,
		protoreflect.Sfixed64Kind,
		protoreflect.Uint64Kind,
		protoreflect.Fixed64Kind:
		return map[string]any{"type": "integer", "format": "int64"}

	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return map[string]any{"type": "number"}

	case protoreflect.StringKind, protoreflect.BytesKind:
		return map[string]any{"type": "string"}

	case protoreflect.EnumKind:
		enumDesc := field.Enum()
		if enumDesc != nil {
			values := enumDesc.Values()
			out := make([]any, 0, values.Len())
			for i := 0; i < values.Len(); i++ {
				out = append(out, string(values.Get(i).Name()))
			}
			return map[string]any{"type": "string", "enum": out}
		}
		return map[string]any{"type": "string"}

	case protoreflect.MessageKind, protoreflect.GroupKind:
		msgDesc := field.Message()
		if msgDesc != nil {
			return w.message(msgDesc)
		}
		return map[string]any{"type": "object"}

	default:
		return map[string]any{"type": "string"}
	}
}
