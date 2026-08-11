package asyncapi

import (
	"fmt"
	"sort"

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
	bindingSpec := BindingSpec
	if source, ok := iface.Sources[DefaultSourceName]; ok && source.BindingSpec != "" {
		bindingSpec = source.BindingSpec
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
		exclusion := operationExclusion(doc, &op, bindingSpec)
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
		if op.Reply != nil {
			for _, candidate := range replyMessageInventory(doc, &op, ref+"#reply-message") {
				entries = append(entries, messageCoverage(doc, candidate, id, emitted, exclusion))
			}
		}
		for index, member := range effectiveServers(doc, &ch) {
			sourceRef := fmt.Sprintf("%s#server[%d]=%s", ref, index, member.Name)
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
				sourceRef: fmt.Sprintf("%s[%d]=%s", prefix, index, messageRefIdentity(doc, ref)),
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
			sourceRef: fmt.Sprintf("%s[%d]=%s/messages/%s", prefix, index, channelSourceRef(op, ch), name),
			message:   &msg,
		})
	}
	return out
}

func channelSourceRef(op *asyncOperation, ch *channel) string {
	if ch != nil && ch.SourceRef != "" {
		return ch.SourceRef
	}
	return "#/channels/" + extractRefName(op.Channel.Ref)
}

func replyMessageInventory(doc *document, op *asyncOperation, prefix string) []observedMessage {
	if op.Reply == nil {
		return nil
	}
	if len(op.Reply.Messages) > 0 {
		out := make([]observedMessage, 0, len(op.Reply.Messages))
		for index, ref := range op.Reply.Messages {
			out = append(out, observedMessage{
				sourceRef: fmt.Sprintf("%s[%d]=%s", prefix, index, messageRefIdentity(doc, ref)),
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
			Rule: "ASYNC-P-03", Message: "the first candidate cannot carry AsyncAPI message headers at the ordinary value boundary",
		}
	}
	if candidate.message.Ref != "" || candidate.message.UnresolvedTrait != "" {
		return openbindings.SynthesisCoverageEntry{
			SourceIndex: 0, SourceRef: candidate.sourceRef, Scope: openbindings.SynthesisCoverageAlternative,
			Status: openbindings.SynthesisExcluded, ReasonCode: "asyncapi.message_unresolvable",
			Rule: "ASYNC-P-03", Message: "the message declaration does not resolve faithfully",
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
