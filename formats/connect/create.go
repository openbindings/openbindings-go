package connect

import (
	"context"
	"fmt"
	"sort"

	"github.com/bufbuild/protocompile"
	openbindings "github.com/openbindings/openbindings-go"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type discovery struct {
	services []protoreflect.ServiceDescriptor
}

// discoverFromProto parses a .proto file (or inline content) and extracts
// service descriptors. Uses protocompile (the v2-native successor to jhump's
// protoparse, maintained by Buf).
func discoverFromProto(ctx context.Context, location string, content any) (*discovery, error) {
	var compiler protocompile.Compiler
	var fileName string

	if content != nil {
		raw, convErr := openbindings.ContentToBytes(content)
		if convErr != nil {
			return nil, fmt.Errorf("convert proto content: %w", convErr)
		}
		fileName = "inline.proto"
		compiler = protocompile.Compiler{
			Resolver: &protocompile.SourceResolver{
				Accessor: protocompile.SourceAccessorFromMap(map[string]string{
					fileName: string(raw),
				}),
			},
		}
	} else if location != "" {
		fileName = location
		compiler = protocompile.Compiler{
			Resolver: &protocompile.SourceResolver{},
		}
	} else {
		return nil, fmt.Errorf("proto source requires a location or content")
	}

	files, err := compiler.Compile(ctx, fileName)
	if err != nil {
		return nil, fmt.Errorf("parse proto: %w", err)
	}

	disc := &discovery{}
	for _, fd := range files {
		services := fd.Services()
		for i := 0; i < services.Len(); i++ {
			disc.services = append(disc.services, services.Get(i))
		}
	}
	return disc, nil
}

// convertToInterface converts protobuf service descriptors to an OpenBindings
// interface with Connect format bindings.
func convertToInterface(disc *discovery, sourceLocation string) (openbindings.Interface, error) {
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
				op.Input = messageToJSONSchema(inputType)
			}
			if outputType := method.Output(); outputType != nil {
				op.Output = messageToJSONSchema(outputType)
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

	return iface, nil
}

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

// messageToJSONSchema and helpers are similar to grpc-go's implementation.
// Both formats use the same protobuf type system.

func messageToJSONSchema(msg protoreflect.MessageDescriptor) map[string]any {
	return messageToJSONSchemaVisited(msg, make(map[string]bool))
}

func messageToJSONSchemaVisited(msg protoreflect.MessageDescriptor, visited map[string]bool) map[string]any {
	fqn := string(msg.FullName())
	if visited[fqn] {
		return map[string]any{"type": "object"}
	}
	visited[fqn] = true

	schema := map[string]any{"type": "object"}

	fields := msg.Fields()
	if fields.Len() == 0 {
		return schema
	}

	properties := map[string]any{}
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		properties[field.JSONName()] = fieldToSchema(field, visited)
	}
	schema["properties"] = properties
	return schema
}

func fieldToSchema(field protoreflect.FieldDescriptor, visited map[string]bool) map[string]any {
	if field.IsMap() {
		valField := field.MapValue()
		return map[string]any{
			"type":                 "object",
			"additionalProperties": scalarOrMessageSchema(valField, visited),
		}
	}

	s := scalarOrMessageSchema(field, visited)

	if field.Cardinality() == protoreflect.Repeated && !field.IsMap() {
		return map[string]any{
			"type":  "array",
			"items": s,
		}
	}

	return s
}

func scalarOrMessageSchema(field protoreflect.FieldDescriptor, visited map[string]bool) map[string]any {
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
		return map[string]any{"type": "string"}

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
			return messageToJSONSchemaVisited(msgDesc, visited)
		}
		return map[string]any{"type": "object"}

	default:
		return map[string]any{"type": "string"}
	}
}
