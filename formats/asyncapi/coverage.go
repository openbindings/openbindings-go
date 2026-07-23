package asyncapi

import (
	"fmt"
	"sort"
	"strings"

	openbindings "github.com/openbindings/openbindings-go"
)

type observedMessage struct {
	sourceRef string
	message   *message
}

// synthesisCoverage inventories every operation and the independently
// selectable message and declared-server alternatives that govern it. An
// operation entry answers whether the target exists in the OBI; alternative
// entries answer whether every artifact choice survived that projection.
func synthesisCoverage(doc *document, iface *openbindings.Interface) []openbindings.SynthesisCoverageEntry {
	if doc == nil || iface == nil {
		return []openbindings.SynthesisCoverageEntry{}
	}
	type identity struct {
		operation string
		ref       string
	}
	represented := make(map[string]identity, len(iface.Bindings))
	for _, binding := range iface.Bindings {
		represented[binding.Ref] = identity{operation: binding.Operation, ref: binding.Ref}
	}

	operationIDs := make([]string, 0, len(doc.Operations))
	for operationID := range doc.Operations {
		operationIDs = append(operationIDs, operationID)
	}
	sort.Strings(operationIDs)

	var entries []openbindings.SynthesisCoverageEntry
	for _, operationID := range operationIDs {
		op := doc.Operations[operationID]
		ref := operationRef(operationID)
		id, emitted := represented[ref]
		exclusion := operationExclusion(doc, &op)
		if exclusion != nil {
			entries = append(entries, coverageExclusion(ref, openbindings.SynthesisCoverageTarget, exclusion))
		} else if !emitted {
			entries = append(entries, openbindings.SynthesisCoverageEntry{
				SourceIndex: 0, SourceRef: ref, Scope: openbindings.SynthesisCoverageTarget,
				Status: openbindings.SynthesisImplementationUnsupported, ReasonCode: "asyncapi.missing_emitted_binding",
				Message: "the synthesizer returned without emitting this bindable operation",
			})
		} else {
			requirements := operationRequirements(doc, &op)
			entries = append(entries, openbindings.SynthesisCoverageEntry{
				SourceIndex: 0, SourceRef: ref, Scope: openbindings.SynthesisCoverageTarget,
				Status: openbindings.SynthesisRepresented, OperationKey: id.operation, BindingRef: id.ref,
				Requirements: requirements,
			})
		}

		channelName := extractRefName(op.Channel.Ref)
		ch, hasChannel := doc.Channels[channelName]
		if !hasChannel {
			continue
		}
		for _, candidate := range governingMessageInventory(doc, &op, &ch, ref+"#message") {
			entries = append(entries, messageCoverage(doc, candidate, id, emitted, exclusion))
		}
		if op.Action == "receive" && op.Reply != nil {
			for _, candidate := range replyMessageInventory(doc, &op, ref+"#reply-message") {
				entries = append(entries, messageCoverage(doc, candidate, id, emitted, exclusion))
			}
		}
		for index, member := range effectiveServers(doc, &ch) {
			sourceRef := fmt.Sprintf("%s#server[%d]=%s", ref, index, member.Name)
			protocol := strings.ToLower(member.Server.Protocol)
			if !isBoundProtocol(protocol) {
				entries = append(entries, openbindings.SynthesisCoverageEntry{
					SourceIndex: 0, SourceRef: sourceRef, Scope: openbindings.SynthesisCoverageAlternative,
					Status: openbindings.SynthesisExcluded, ReasonCode: "asyncapi.protocol_outside_revision",
					Rule: "ASYNC-P-02", Message: fmt.Sprintf("server protocol %q is outside revision 1", member.Server.Protocol),
				})
				continue
			}
			if cell := protocolCellExclusion(doc, &op, &ch, protocol); cell != nil {
				entries = append(entries, coverageExclusion(sourceRef, openbindings.SynthesisCoverageAlternative, cell))
				continue
			}
			if emitted {
				entries = append(entries, openbindings.SynthesisCoverageEntry{
					SourceIndex: 0, SourceRef: sourceRef, Scope: openbindings.SynthesisCoverageAlternative,
					Status: openbindings.SynthesisRepresented, OperationKey: id.operation, BindingRef: id.ref,
				})
			} else {
				entries = append(entries, openbindings.SynthesisCoverageEntry{
					SourceIndex: 0, SourceRef: sourceRef, Scope: openbindings.SynthesisCoverageAlternative,
					Status: openbindings.SynthesisExcluded, ReasonCode: "asyncapi.parent_target_excluded",
					Rule: "ASYNC-P-02", Message: "the governing operation has no faithfully representable target",
				})
			}
		}
	}
	return entries
}

func coverageExclusion(sourceRef string, scope openbindings.SynthesisCoverageScope, exclusion *authoringExclusion) openbindings.SynthesisCoverageEntry {
	status := openbindings.SynthesisExcluded
	if exclusion.status == "invalid" {
		status = openbindings.SynthesisInvalid
	}
	return openbindings.SynthesisCoverageEntry{
		SourceIndex: 0, SourceRef: sourceRef, Scope: scope, Status: status,
		ReasonCode: exclusion.code, Rule: exclusion.rule, Message: exclusion.message,
	}
}

func operationRequirements(doc *document, op *asyncOperation) []string {
	channelName := extractRefName(op.Channel.Ref)
	ch := doc.Channels[channelName]
	var requirements []string
	if defaultServer(effectiveServers(doc, &ch)) == nil {
		requirements = append(requirements, "configuration.server")
	}
	if op.Action == "receive" && len(governingMessages(doc, op, &ch)) > 1 {
		requirements = append(requirements, "configuration.message")
	}
	return requirements
}

func governingMessageInventory(doc *document, op *asyncOperation, ch *channel, prefix string) []observedMessage {
	if len(op.Messages) > 0 {
		out := make([]observedMessage, 0, len(op.Messages))
		for index, ref := range op.Messages {
			out = append(out, observedMessage{
				sourceRef: fmt.Sprintf("%s[%d]=%s", prefix, index, ref.Ref),
				message:   resolveMessageRef(doc, ref),
			})
		}
		return out
	}
	names := make([]string, 0, len(ch.Messages))
	for name := range ch.Messages {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]observedMessage, 0, len(names))
	for index, name := range names {
		msg := ch.Messages[name]
		out = append(out, observedMessage{
			sourceRef: fmt.Sprintf("%s[%d]=#/channels/%s/messages/%s", prefix, index, extractRefName(op.Channel.Ref), name),
			message:   &msg,
		})
	}
	return out
}

func replyMessageInventory(doc *document, op *asyncOperation, prefix string) []observedMessage {
	if op.Reply == nil {
		return nil
	}
	if len(op.Reply.Messages) > 0 {
		out := make([]observedMessage, 0, len(op.Reply.Messages))
		for index, ref := range op.Reply.Messages {
			out = append(out, observedMessage{
				sourceRef: fmt.Sprintf("%s[%d]=%s", prefix, index, ref.Ref),
				message:   resolveMessageRef(doc, ref),
			})
		}
		return out
	}
	if op.Reply.Channel == nil {
		return nil
	}
	ch, ok := doc.Channels[extractRefName(op.Reply.Channel.Ref)]
	if !ok {
		return []observedMessage{{
			sourceRef: prefix + "=<dangling-reply-channel>",
		}}
	}
	return governingMessageInventory(doc, &asyncOperation{Channel: *op.Reply.Channel}, &ch, prefix)
}

func messageCoverage(doc *document, candidate observedMessage, id struct {
	operation string
	ref       string
}, emitted bool, parentExclusion *authoringExclusion) openbindings.SynthesisCoverageEntry {
	if candidate.message == nil {
		return openbindings.SynthesisCoverageEntry{
			SourceIndex: 0, SourceRef: candidate.sourceRef, Scope: openbindings.SynthesisCoverageAlternative,
			Status: openbindings.SynthesisInvalid, ReasonCode: "asyncapi.dangling_message_ref",
			Rule: "ASYNC-D-03", Message: "the message reference does not resolve",
		}
	}
	if candidate.message.Headers != nil {
		return openbindings.SynthesisCoverageEntry{
			SourceIndex: 0, SourceRef: candidate.sourceRef, Scope: openbindings.SynthesisCoverageAlternative,
			Status: openbindings.SynthesisExcluded, ReasonCode: "asyncapi.message_headers",
			Rule: "ASYNC-P-03", Message: "revision 1 cannot carry AsyncAPI message headers",
		}
	}
	if candidate.message.Bindings != nil && candidate.message.Bindings.HTTP != nil {
		version := candidate.message.Bindings.HTTP.BindingVersion
		if version != "" && version != "0.3.0" {
			return openbindings.SynthesisCoverageEntry{
				SourceIndex: 0, SourceRef: candidate.sourceRef, Scope: openbindings.SynthesisCoverageAlternative,
				Status: openbindings.SynthesisExcluded, ReasonCode: "asyncapi.unsupported_message_binding_version",
				Rule: "ASYNC-P-02", Message: "the HTTP message binding version is outside revision 1",
			}
		}
	}
	if err := supportedMessageContentType(messageEffectiveContentType(doc, *candidate.message)); err != nil {
		return openbindings.SynthesisCoverageEntry{
			SourceIndex: 0, SourceRef: candidate.sourceRef, Scope: openbindings.SynthesisCoverageAlternative,
			Status: openbindings.SynthesisExcluded, ReasonCode: "asyncapi.message_content_type_unrepresentable",
			Rule: "ASYNC-P-03", Message: err.Error(),
		}
	}
	if emitted {
		return openbindings.SynthesisCoverageEntry{
			SourceIndex: 0, SourceRef: candidate.sourceRef, Scope: openbindings.SynthesisCoverageAlternative,
			Status: openbindings.SynthesisRepresented, OperationKey: id.operation, BindingRef: id.ref,
		}
	}
	message := "the governing operation has no faithfully representable target"
	rule := "ASYNC-P-02"
	if parentExclusion != nil {
		message = parentExclusion.message
		rule = parentExclusion.rule
	}
	return openbindings.SynthesisCoverageEntry{
		SourceIndex: 0, SourceRef: candidate.sourceRef, Scope: openbindings.SynthesisCoverageAlternative,
		Status: openbindings.SynthesisExcluded, ReasonCode: "asyncapi.parent_target_excluded",
		Rule: rule, Message: message,
	}
}

func protocolCellExclusion(doc *document, op *asyncOperation, ch *channel, protocol string) *authoringExclusion {
	switch protocol {
	case "http", "https":
		if op.Action == "send" {
			return &authoringExclusion{"excluded", "asyncapi.standalone_http_send", "ASYNC-P-02", "standalone HTTP send is outside revision 1"}
		}
		if op.Bindings == nil || op.Bindings.HTTP == nil || strings.TrimSpace(op.Bindings.HTTP.Method) == "" {
			return &authoringExclusion{"excluded", "asyncapi.http_method_unresolved", "ASYNC-P-02", "the HTTP publish cell has no artifact-declared method"}
		}
		if !requiredPropertiesMayBeStrings(op.Bindings.HTTP.Query) {
			return &authoringExclusion{"excluded", "asyncapi.protocol_fields_unrepresentable", "ASYNC-P-04", "required HTTP protocol fields do not admit string values"}
		}
		if !replyMessagesBindable(doc, op) {
			return &authoringExclusion{"excluded", "asyncapi.reply_carriage_unrepresentable", "ASYNC-P-05", "an HTTP reply message uses carriage outside revision 1"}
		}
	case "ws", "wss":
		if op.Action == "receive" && op.Reply != nil {
			return &authoringExclusion{"excluded", "asyncapi.websocket_reply", "ASYNC-P-02", "reply-bearing publish is not representable over the WebSocket cell"}
		}
		if !wsFieldsMayBeStrings(ch) {
			return &authoringExclusion{"excluded", "asyncapi.protocol_fields_unrepresentable", "ASYNC-P-04", "required WebSocket protocol fields do not admit string values"}
		}
		if op.Action == "send" {
			for _, parameter := range ch.Parameters {
				if parameter.Location != "" {
					return &authoringExclusion{"excluded", "asyncapi.subscription_parameter_location", "ASYNC-P-04", "a subscription channel parameter declares a location revision 1 cannot preserve"}
				}
			}
			messages := governingMessages(doc, op, ch)
			if len(messages) == 0 {
				return &authoringExclusion{"excluded", "asyncapi.no_resolved_messages", "ASYNC-P-03", "the subscription interaction has no resolved message declaration"}
			}
			for _, msg := range messages {
				if !messageBindable(doc, msg) {
					return &authoringExclusion{"excluded", "asyncapi.unbindable_subscription_message", "ASYNC-P-03", "a subscription message alternative uses carriage outside revision 1"}
				}
			}
			ctx := map[string]any{"configuration": map[string]any{"decode": "json"}}
			if _, err := resolveSubscriptionContentType(doc, messages, ctx); err != nil {
				return &authoringExclusion{"excluded", "asyncapi.ambiguous_subscription_content_type", "ASYNC-P-05", err.Error()}
			}
		}
	}
	return nil
}
