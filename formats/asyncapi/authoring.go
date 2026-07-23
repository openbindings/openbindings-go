package asyncapi

import (
	"encoding/json"
	"sort"
	"strings"
)

// bindableOperationIDs is the shared authoring eligibility gate. It admits
// exactly operations for which revision 1 has at least one faithful protocol
// cell. Required configuration (server URL, message selection, codec lane,
// protocol fields) remains satisfiable and therefore does not exclude a
// target; artifact contradictions and definition-level exclusions do.
func bindableOperationIDs(doc *document) []string {
	ids := make([]string, 0, len(doc.Operations))
	for id, op := range doc.Operations {
		if operationBindable(doc, &op) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func operationBindable(doc *document, op *asyncOperation) bool {
	if op == nil || op.Ref != "" || (op.Action != "send" && op.Action != "receive") {
		return false
	}
	channelName := extractRefName(op.Channel.Ref)
	ch, ok := doc.Channels[channelName]
	if !ok {
		return false
	}
	if op.Bindings != nil && op.Bindings.HTTP != nil {
		v := op.Bindings.HTTP.BindingVersion
		if v != "" && v != "0.3.0" {
			return false
		}
	}
	if ch.Bindings != nil && ch.Bindings.WS != nil {
		v := ch.Bindings.WS.BindingVersion
		if v != "" && v != "0.1.0" {
			return false
		}
	}

	// A receive action can use HTTP when the artifact declares its method.
	// It can use WebSocket only without reply augmentation. Either protocol
	// remains selectable through the server configuration point.
	if op.Action == "receive" {
		messages := authoringInputMessages(doc, op, &ch)
		usable := false
		for _, m := range messages {
			if messageBindable(m) {
				usable = true
				break
			}
		}
		if !usable {
			return false
		}
		httpOK := op.Bindings != nil && op.Bindings.HTTP != nil && strings.TrimSpace(op.Bindings.HTTP.Method) != "" && requiredPropertiesMayBeStrings(op.Bindings.HTTP.Query)
		wsOK := op.Reply == nil && wsFieldsMayBeStrings(&ch)
		return httpOK || wsOK
	}

	// A send action is a subscription. Standalone HTTP send is excluded, so
	// only the WebSocket cell can make it bindable.
	if !wsFieldsMayBeStrings(&ch) {
		return false
	}
	for _, parameter := range ch.Parameters {
		if parameter.Location != "" {
			return false
		}
	}
	messages := governingMessages(doc, op, &ch)
	if len(messages) == 0 {
		return false
	}
	for _, m := range messages {
		if !messageBindable(m) {
			return false
		}
	}
	ctx := map[string]any{"configuration": map[string]any{"decode": "json"}}
	_, err := resolveSubscriptionContentType(doc, messages, ctx)
	return err == nil
}

func authoringInputMessages(doc *document, op *asyncOperation, ch *channel) []message {
	return governingMessages(doc, op, ch)
}

func messageBindable(m message) bool {
	if m.Headers != nil {
		return false
	}
	if m.Bindings != nil && m.Bindings.HTTP != nil {
		v := m.Bindings.HTTP.BindingVersion
		if v != "" && v != "0.3.0" {
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
	var messages []message
	if input {
		channelName := extractRefName(op.Channel.Ref)
		ch := doc.Channels[channelName]
		messages = authoringInputMessages(doc, op, &ch)
		usable := messages[:0]
		for _, m := range messages {
			if messageBindable(m) {
				usable = append(usable, m)
			}
		}
		messages = usable
	} else {
		channelName := extractRefName(op.Channel.Ref)
		ch := doc.Channels[channelName]
		messages = governingMessages(doc, op, &ch)
	}
	return unionPayloadSchemas(messages)
}

func replyPayloadSchema(doc *document, reply *operationReply) map[string]any {
	if reply == nil {
		return nil
	}
	var messages []message
	for _, ref := range reply.Messages {
		if m := resolveMessageRef(doc, ref); m != nil && messageBindable(*m) {
			messages = append(messages, *m)
		}
	}
	if len(messages) == 0 && reply.Channel != nil {
		if ch, ok := doc.Channels[extractRefName(reply.Channel.Ref)]; ok {
			for _, m := range channelMessages(&ch) {
				if messageBindable(m) {
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
		if message.Payload == nil {
			return nil // one artifact alternative is unconstrained
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
