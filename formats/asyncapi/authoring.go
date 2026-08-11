package asyncapi

import (
	"encoding/json"
	"sort"
	"strings"
)

type authoringExclusion struct {
	status  string
	code    string
	rule    string
	message string
}

// bindableOperationIDs is the protocol-independent authoring gate. Driver
// installation and built-in driver coverage never determine synthesis.
func bindableOperationIDs(doc *document, bindingSpec string) []string {
	ids := make([]string, 0, len(doc.Operations))
	for id, op := range doc.Operations {
		if operationBindable(doc, &op, bindingSpec) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func operationBindable(doc *document, op *asyncOperation, bindingSpec string) bool {
	return operationExclusion(doc, op, bindingSpec) == nil
}

func operationExclusion(doc *document, op *asyncOperation, bindingSpec string) *authoringExclusion {
	_ = bindingSpec
	if op == nil {
		return &authoringExclusion{"invalid", "asyncapi.invalid_operation", "ASYNC-D-03", "the operations-map entry is not an operation object"}
	}
	if op.Ref != "" || op.UnresolvedTrait != "" {
		return &authoringExclusion{"invalid", "asyncapi.dangling_operation_ref", "ASYNC-D-03", "the operations-map reference does not resolve to an operation object"}
	}
	if op.Action != "send" && op.Action != "receive" {
		return &authoringExclusion{"invalid", "asyncapi.invalid_action", "ASYNC-D-03", "the operation action is neither send nor receive"}
	}
	channelName := extractRefName(op.Channel.Ref)
	ch, ok := doc.Channels[channelName]
	if !ok {
		return &authoringExclusion{"invalid", "asyncapi.dangling_channel_ref", "ASYNC-D-03", "the operation channel reference does not resolve"}
	}
	if len(effectiveServers(doc, &ch)) == 0 {
		return &authoringExclusion{"excluded", "asyncapi.no_effective_server", "ASYNC-P-04", "the operation has no effective artifact-declared server or protocol"}
	}
	operationMessages := governingMessages(doc, op, &ch)
	if len(operationMessages) == 0 {
		return &authoringExclusion{"excluded", "asyncapi.no_resolved_messages", "ASYNC-P-03", "the operation has no resolved message declaration"}
	}
	replyMessages := replyGoverningMessages(doc, op)
	if op.Reply != nil && len(replyMessages) == 0 {
		return &authoringExclusion{"excluded", "asyncapi.no_resolved_reply_messages", "ASYNC-P-03", "the operation declares a reply but no reply message resolves"}
	}
	inputMessages, outputMessages := operationMessages, replyMessages
	if op.Action == "send" {
		inputMessages, outputMessages = replyMessages, operationMessages
	}
	if len(inputMessages) > 0 {
		allHaveHeaders := true
		for _, message := range inputMessages {
			if message.Headers == nil {
				allHaveHeaders = false
				break
			}
		}
		if allHaveHeaders {
			return &authoringExclusion{"excluded", "asyncapi.message_headers", "ASYNC-P-03", "every caller-input message declares application headers the first candidate cannot carry at the ordinary value boundary"}
		}
	}
	for _, message := range outputMessages {
		if message.Headers != nil {
			return &authoringExclusion{"excluded", "asyncapi.message_headers", "ASYNC-P-03", "a possible caller-output message declares application headers the first candidate cannot carry at the ordinary value boundary"}
		}
	}
	return nil
}

func authoringInputMessages(doc *document, op *asyncOperation, ch *channel) []message {
	return governingMessages(doc, op, ch)
}

func messageBindable(doc *document, m message) bool {
	if m.UnresolvedTrait != "" {
		return false
	}
	if m.Headers != nil {
		return false
	}
	if m.Bindings != nil && m.Bindings.HTTP != nil {
		v := m.Bindings.HTTP.BindingVersion
		if v != "" && v != "0.3.0" {
			return false
		}
	}
	return supportedMessageContentType(messageEffectiveContentType(doc, m)) == nil
}

func replyMessagesBindable(doc *document, op *asyncOperation) bool {
	if op == nil || op.Reply == nil {
		return true
	}
	messages := replyGoverningMessages(doc, op)
	if len(messages) == 0 {
		return false
	}
	for _, m := range messages {
		if !messageBindable(doc, m) {
			return false
		}
	}
	return true
}

func wsFieldsMayBeStrings(ch *channel) bool {
	if ch == nil || ch.Bindings == nil || ch.Bindings.WS == nil {
		return true
	}
	return requiredPropertiesMayBeStrings(ch.Bindings.WS.Query) && requiredPropertiesMayBeStrings(ch.Bindings.WS.Headers)
}

// Required protocol fields need at least one string value. Optional fields
// can be omitted, so a non-string-only optional schema does not make the
// entire target unbindable.
func requiredPropertiesMayBeStrings(schema map[string]any) bool {
	if schema == nil {
		return true
	}
	required := authoringStringSlice(schema["required"])
	properties, _ := schema["properties"].(map[string]any)
	for _, name := range required {
		raw, declared := properties[name]
		if !declared {
			return false
		}
		property, _ := raw.(map[string]any)
		if typ, present := property["type"]; present && typ != "string" {
			return false
		}
	}
	return true
}

func authoringStringSlice(value any) []string {
	switch values := value.(type) {
	case []string:
		return values
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func operationPayloadSchema(doc *document, op *asyncOperation, input bool) map[string]any {
	channelName := extractRefName(op.Channel.Ref)
	ch := doc.Channels[channelName]
	messages := governingMessages(doc, op, &ch)
	if input {
		usable := make([]message, 0, len(messages))
		for _, m := range messages {
			if m.Headers == nil {
				usable = append(usable, m)
			}
		}
		messages = usable
	}
	return unionPayloadSchemas(messages)
}

func replyPayloadSchema(doc *document, reply *operationReply) map[string]any {
	if reply == nil {
		return nil
	}
	var messages []message
	for _, ref := range reply.Messages {
		if m := resolveMessageRef(doc, ref); m != nil && m.Headers == nil {
			messages = append(messages, *m)
		}
	}
	if len(messages) == 0 && reply.Channel != nil {
		if ch, ok := doc.Channels[extractRefName(reply.Channel.Ref)]; ok {
			for _, m := range channelMessages(&ch) {
				if m.Headers == nil {
					messages = append(messages, m)
				}
			}
		}
	}
	return unionPayloadSchemas(messages)
}

func unionPayloadSchemas(messages []message) map[string]any {
	if len(messages) == 0 {
		return nil
	}
	unique := map[string]map[string]any{}
	for _, message := range messages {
		if message.Payload == nil || foreignSchemaFormat(message.SchemaFormat) {
			return map[string]any{}
		}
		encoded, _ := json.Marshal(message.Payload)
		unique[string(encoded)] = message.Payload
	}
	if len(unique) == 1 {
		for _, schema := range unique {
			return schema
		}
	}
	keys := make([]string, 0, len(unique))
	for key := range unique {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	choices := make([]any, 0, len(keys))
	for _, key := range keys {
		choices = append(choices, unique[key])
	}
	return map[string]any{"anyOf": choices}
}

func foreignSchemaFormat(format string) bool {
	format = strings.ToLower(format)
	return format != "" && !strings.Contains(format, "asyncapi") && !strings.Contains(format, "json-schema")
}
