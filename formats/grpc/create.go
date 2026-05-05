package grpc

import (
	"fmt"
	"sort"

	"github.com/jhump/protoreflect/desc" //nolint:staticcheck // no v2 equivalent yet
	openbindings "github.com/openbindings/openbindings-go"
	"google.golang.org/protobuf/types/descriptorpb"
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
		return disc.services[i].GetFullyQualifiedName() < disc.services[j].GetFullyQualifiedName()
	})

	for _, svc := range disc.services {
		methods := svc.GetMethods()
		sort.Slice(methods, func(i, j int) bool {
			return methods[i].GetName() < methods[j].GetName()
		})
		for _, method := range methods {
			if method.IsClientStreaming() {
				continue
			}

			fqn := svc.GetFullyQualifiedName() + "/" + method.GetName()
			opKey := openbindings.SanitizeKey(method.GetName())
			opKey = openbindings.ResolveKeyCollision(opKey, svc.GetName(), usedKeys)
			usedKeys[opKey] = fqn

			op := openbindings.Operation{
				Description: commentToDescription(method),
			}

			inputType := method.GetInputType()
			if inputType != nil {
				op.Input = newSchemaWalker(onWarning, "operations."+opKey+".input").message(inputType)
			}

			outputType := method.GetOutputType()
			if outputType != nil {
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
		iface.Name = svc.GetName()
		if len(disc.services) > 1 {
			iface.Name = packageName(svc)
		}
	}

	// gRPC/protobuf definitions do not expose security metadata, so we
	// leave the security section empty. If the server requires auth, the
	// driver's auth retry flow will handle it (Unauthenticated → resolve
	// credentials → retry).

	return iface, nil
}

func packageName(svc *desc.ServiceDescriptor) string {
	pkg := svc.GetFile().GetPackage()
	if pkg != "" {
		return pkg
	}
	return svc.GetName()
}

func commentToDescription(method *desc.MethodDescriptor) string {
	info := method.GetSourceInfo()
	if info != nil && info.GetLeadingComments() != "" {
		return trimComment(info.GetLeadingComments())
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

func (w *schemaWalker) message(msg *desc.MessageDescriptor) map[string]any {
	fqn := msg.GetFullyQualifiedName()
	if w.visited[fqn] {
		return map[string]any{"type": "object"}
	}

	// Well-known proto types have canonical JSON representations per the
	// proto3 JSON mapping spec. Emit those directly instead of descending
	// into the message's fields — traversing Timestamp's `seconds`/`nanos`
	// produces a contract the driver's jsonpb layer cannot accept.
	if wk := wellKnownSchema(fqn); wk != nil {
		return wk
	}

	w.visited[fqn] = true

	schema := map[string]any{
		"type": "object",
	}

	fields := msg.GetFields()
	if len(fields) == 0 {
		return schema
	}

	var regularFields []*desc.FieldDescriptor
	oneofGroups := map[string][]*desc.FieldDescriptor{}
	var oneofOrder []string
	for _, field := range fields {
		oo := field.GetOneOf()
		// Proto3 `optional` fields are wrapped in synthetic single-field
		// oneofs for explicit-presence tracking; they are not user-declared
		// unions and must not be emitted as oneOf variants.
		if oo == nil || field.IsProto3Optional() {
			regularFields = append(regularFields, field)
			continue
		}
		name := oo.GetName()
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
			fmt.Sprintf("message %s contains %d oneof groups; the v0.1 schema profile cannot express multi-group exclusivity, so members are emitted as independent optional properties", msg.GetName(), len(oneofGroups)),
			map[string]any{
				"message": msg.GetFullyQualifiedName(),
				"groups":  groupNames,
			},
		)
	}

	properties := map[string]any{}
	for _, field := range regularFields {
		properties[field.GetJSONName()] = w.field(field)
	}
	if !useOneOf {
		for _, name := range oneofOrder {
			for _, field := range oneofGroups[name] {
				properties[field.GetJSONName()] = w.field(field)
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
			jsonName := field.GetJSONName()
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
// as JSON numbers, JSON strings, or protobuf varints is a driver concern.
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

func (w *schemaWalker) field(field *desc.FieldDescriptor) map[string]any {
	if field.IsMap() {
		valField := field.GetMapValueType()
		return map[string]any{
			"type":                 "object",
			"additionalProperties": w.scalarOrMessage(valField),
		}
	}

	s := w.scalarOrMessage(field)

	if field.IsRepeated() && !field.IsMap() {
		return map[string]any{
			"type":  "array",
			"items": s,
		}
	}

	return s
}

func (w *schemaWalker) scalarOrMessage(field *desc.FieldDescriptor) map[string]any {
	t := field.GetType()
	switch t {
	case descriptorpb.FieldDescriptorProto_TYPE_BOOL:
		return map[string]any{"type": "boolean"}

	case descriptorpb.FieldDescriptorProto_TYPE_INT32,
		descriptorpb.FieldDescriptorProto_TYPE_SINT32,
		descriptorpb.FieldDescriptorProto_TYPE_SFIXED32,
		descriptorpb.FieldDescriptorProto_TYPE_UINT32,
		descriptorpb.FieldDescriptorProto_TYPE_FIXED32:
		return map[string]any{"type": "integer"}

	case descriptorpb.FieldDescriptorProto_TYPE_INT64,
		descriptorpb.FieldDescriptorProto_TYPE_SINT64,
		descriptorpb.FieldDescriptorProto_TYPE_SFIXED64,
		descriptorpb.FieldDescriptorProto_TYPE_UINT64,
		descriptorpb.FieldDescriptorProto_TYPE_FIXED64:
		return map[string]any{"type": "integer", "format": "int64"}

	case descriptorpb.FieldDescriptorProto_TYPE_FLOAT,
		descriptorpb.FieldDescriptorProto_TYPE_DOUBLE:
		return map[string]any{"type": "number"}

	case descriptorpb.FieldDescriptorProto_TYPE_STRING:
		return map[string]any{"type": "string"}

	case descriptorpb.FieldDescriptorProto_TYPE_BYTES:
		return map[string]any{"type": "string"}

	case descriptorpb.FieldDescriptorProto_TYPE_ENUM:
		enumDesc := field.GetEnumType()
		if enumDesc != nil {
			var values []any
			for _, v := range enumDesc.GetValues() {
				values = append(values, v.GetName())
			}
			return map[string]any{"type": "string", "enum": values}
		}
		return map[string]any{"type": "string"}

	case descriptorpb.FieldDescriptorProto_TYPE_MESSAGE,
		descriptorpb.FieldDescriptorProto_TYPE_GROUP:
		msgDesc := field.GetMessageType()
		if msgDesc != nil {
			return w.message(msgDesc)
		}
		return map[string]any{"type": "object"}

	default:
		return map[string]any{"type": "string"}
	}
}
