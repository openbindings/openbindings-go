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

// bindableOperationIDs is the shared authoring eligibility gate. It admits
// exactly operations for which revision 1 has at least one faithful protocol
// cell. Required configuration (server URL, message selection, codec lane,
// protocol fields) remains satisfiable and therefore does not exclude a
// target; artifact contradictions and definition-level exclusions do.
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
	if op == nil {
		return &authoringExclusion{"invalid", "asyncapi.invalid_operation", "ASYNC-D-03", "the operations-map entry is not an operation object"}
	}
	if op.Ref != "" {
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
	if op.Bindings != nil && op.Bindings.HTTP != nil {
		v := op.Bindings.HTTP.BindingVersion
		if v != "" && v != "0.3.0" {
			return &authoringExclusion{"excluded", "asyncapi.unsupported_http_binding_version", "ASYNC-P-02", "the HTTP operation binding version is outside revision 1"}
		}
	}
	if ch.Bindings != nil && ch.Bindings.WS != nil {
		v := ch.Bindings.WS.BindingVersion
		if v != "" && v != "0.1.0" {
			return &authoringExclusion{"excluded", "asyncapi.unsupported_websocket_binding_version", "ASYNC-P-02", "the WebSocket channel binding version is outside revision 1"}
		}
	}
	hasHTTP, hasWS := false, false
	for _, member := range effectiveServers(doc, &ch) {
		switch strings.ToLower(member.Server.Protocol) {
		case "http", "https":
			hasHTTP = true
		case "ws", "wss":
			hasWS = true
		}
	}

	// A receive action can use HTTP when the artifact declares its method.
	// It can use WebSocket only without reply augmentation. Either protocol
	// remains selectable through the server configuration point.
	if op.Action == "receive" {
		messages := authoringInputMessages(doc, op, &ch)
		usable := false
		for _, m := range messages {
			if messageBindable(doc, m) {
				usable = true
				break
			}
		}
		if !usable {
			return &authoringExclusion{"excluded", "asyncapi.no_bindable_message", "ASYNC-P-03", "the publish interaction has no message alternative revision 1 can carry"}
		}
		httpOK := hasHTTP && op.Bindings != nil && op.Bindings.HTTP != nil && strings.TrimSpace(op.Bindings.HTTP.Method) != "" && requiredPropertiesMayBeStrings(op.Bindings.HTTP.Query) && replyMessagesBindable(doc, op)
		wsOK := hasWS && op.Reply == nil && wsFieldsMayBeStrings(&ch)
		if !httpOK && !wsOK {
			return &authoringExclusion{"excluded", "asyncapi.no_faithful_protocol_cell", "ASYNC-P-02", "neither the HTTP publish nor WebSocket publish cell is faithfully representable"}
		}
		return nil
	}

	// A send action is a subscription. Standalone HTTP send is excluded, so
	// only the WebSocket cell can make it bindable.
	if !hasWS {
		return &authoringExclusion{"excluded", "asyncapi.no_faithful_protocol_cell", "ASYNC-P-02", "the operation's effective server set provides no WebSocket subscription cell"}
	}
	if op.Reply != nil && preservesSendReplies(bindingSpec) {
		return &authoringExclusion{"excluded", "asyncapi.websocket_reply", "ASYNC-P-02", "reply-bearing WebSocket send operations require request/reply session semantics revision 2 does not define"}
	}
	if !wsFieldsMayBeStrings(&ch) {
		return &authoringExclusion{"excluded", "asyncapi.protocol_fields_unrepresentable", "ASYNC-P-04", "required WebSocket protocol fields do not admit string values"}
	}
	for _, parameter := range ch.Parameters {
		if parameter.Location != "" {
			return &authoringExclusion{"excluded", "asyncapi.subscription_parameter_location", "ASYNC-P-04", "a subscription channel parameter declares a location revision 1 cannot preserve"}
		}
	}
	messages := governingMessages(doc, op, &ch)
	if len(messages) == 0 {
		return &authoringExclusion{"excluded", "asyncapi.no_resolved_messages", "ASYNC-P-03", "the subscription interaction has no resolved message declaration"}
	}
	for _, m := range messages {
		if !messageBindable(doc, m) {
			return &authoringExclusion{"excluded", "asyncapi.unbindable_subscription_message", "ASYNC-P-03", "a subscription message alternative uses carriage outside revision 1"}
		}
	}
	ctx := map[string]any{"configuration": map[string]any{"decode": "json"}}
	_, err := resolveSubscriptionContentType(doc, messages, ctx)
	if err != nil {
		return &authoringExclusion{"excluded", "asyncapi.ambiguous_subscription_content_type", "ASYNC-P-05", err.Error()}
	}
	return nil
}

func authoringInputMessages(doc *document, op *asyncOperation, ch *channel) []message {
	return governingMessages(doc, op, ch)
}

func messageBindable(doc *document, m message) bool {
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
	var messages []message
	if input {
		channelName := extractRefName(op.Channel.Ref)
		ch := doc.Channels[channelName]
		messages = authoringInputMessages(doc, op, &ch)
		usable := messages[:0]
		for _, m := range messages {
			if messageBindable(doc, m) {
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
		if m := resolveMessageRef(doc, ref); m != nil && messageBindable(doc, *m) {
			messages = append(messages, *m)
		}
	}
	if len(messages) == 0 && reply.Channel != nil {
		if ch, ok := doc.Channels[extractRefName(reply.Channel.Ref)]; ok {
			for _, m := range channelMessages(&ch) {
				if messageBindable(doc, m) {
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
