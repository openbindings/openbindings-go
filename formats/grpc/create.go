package grpc

import (
	"fmt"
	"sort"

	"github.com/jhump/protoreflect/desc" //nolint:staticcheck // no v2 equivalent yet
	openbindings "github.com/openbindings/openbindings-go"
	"google.golang.org/protobuf/types/descriptorpb"
)

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
				op.Input = messageToJSONSchema(inputType)
			}

			outputType := method.GetOutputType()
			if outputType != nil {
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
		iface.Name = svc.GetName()
		if len(disc.services) > 1 {
			iface.Name = packageName(svc)
		}
	}

	// gRPC/protobuf definitions do not expose security metadata, so we
	// leave the security section empty. If the server requires auth, the
	// executor's auth retry flow will handle it (Unauthenticated → resolve
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

func messageToJSONSchema(msg *desc.MessageDescriptor) map[string]any {
	return messageToJSONSchemaVisited(msg, make(map[string]bool))
}

func messageToJSONSchemaVisited(msg *desc.MessageDescriptor, visited map[string]bool) map[string]any {
	fqn := msg.GetFullyQualifiedName()
	if visited[fqn] {
		return map[string]any{"type": "object"}
	}
	visited[fqn] = true

	schema := map[string]any{
		"type": "object",
	}

	fields := msg.GetFields()
	if len(fields) == 0 {
		return schema
	}

	properties := map[string]any{}
	for _, field := range fields {
		properties[field.GetJSONName()] = fieldToSchema(field, visited)
	}
	schema["properties"] = properties

	return schema
}

func fieldToSchema(field *desc.FieldDescriptor, visited map[string]bool) map[string]any {
	if field.IsMap() {
		valField := field.GetMapValueType()
		return map[string]any{
			"type":                 "object",
			"additionalProperties": scalarOrMessageSchema(valField, visited),
		}
	}

	s := scalarOrMessageSchema(field, visited)

	if field.IsRepeated() && !field.IsMap() {
		return map[string]any{
			"type":  "array",
			"items": s,
		}
	}

	return s
}

func scalarOrMessageSchema(field *desc.FieldDescriptor, visited map[string]bool) map[string]any {
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
		return map[string]any{"type": "string"}

	case descriptorpb.FieldDescriptorProto_TYPE_FLOAT,
		descriptorpb.FieldDescriptorProto_TYPE_DOUBLE:
		return map[string]any{"type": "number"}

	case descriptorpb.FieldDescriptorProto_TYPE_STRING:
		return map[string]any{"type": "string"}

	case descriptorpb.FieldDescriptorProto_TYPE_BYTES:
		return map[string]any{"type": "string", "contentEncoding": "base64"}

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
			return messageToJSONSchemaVisited(msgDesc, visited)
		}
		return map[string]any{"type": "object"}

	default:
		return map[string]any{"type": "string"}
	}
}
