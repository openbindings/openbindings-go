package asyncapi

import (
	"fmt"
	"sort"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/synthesize"
)

type observedMessage struct {
	sourceRef string
	message   *message
}

// synthesisCoverage inventories every operation and the independently
// selectable message and declared-server alternatives that govern it. An
// operation entry answers whether the target exists in the OBI; alternative
// entries answer whether every artifact choice survived that projection.
func synthesisCoverage(doc *document, iface *openbindings.Interface) []synthesize.SynthesisCoverageEntry {
	if doc == nil || iface == nil {
		return []synthesize.SynthesisCoverageEntry{}
	}
	bindingSpec := BindingSpec
	if source, ok := iface.Sources[DefaultSourceName]; ok && source.BindingSpec != "" {
		bindingSpec = source.BindingSpec
	}
	type identity struct {
		operation string
		selector  string
	}
	represented := make(map[string]identity, len(iface.Bindings))
	for _, binding := range iface.Bindings {
		represented[binding.Selector] = identity{operation: binding.Operation, selector: binding.Selector}
	}

	operationIDs := make([]string, 0, len(doc.Operations))
	for operationID := range doc.Operations {
		operationIDs = append(operationIDs, operationID)
	}
	sort.Strings(operationIDs)

	var entries []synthesize.SynthesisCoverageEntry
	for _, operationID := range operationIDs {
		op := doc.Operations[operationID]
		selector := operationSelector(operationID)
		id, emitted := represented[selector]
		exclusion := operationExclusion(doc, &op, bindingSpec)
		if exclusion != nil {
			entries = append(entries, coverageExclusion(selector, synthesize.SynthesisCoverageTarget, exclusion))
		} else if !emitted {
			entries = append(entries, synthesize.SynthesisCoverageEntry{
				SourceIndex: 0, SourceRef: selector, Scope: synthesize.SynthesisCoverageTarget,
				Status: synthesize.SynthesisImplementationUnsupported, ReasonCode: "asyncapi.missing_emitted_binding",
				Message: "the synthesizer returned without emitting this bindable operation",
			})
		} else {
			requirements := operationRequirements(doc, &op)
			entries = append(entries, synthesize.SynthesisCoverageEntry{
				SourceIndex: 0, SourceRef: selector, Scope: synthesize.SynthesisCoverageTarget,
				Status: synthesize.SynthesisRepresented, OperationKey: id.operation, BindingSelector: id.selector,
				Requirements: requirements,
			})
		}

		channelName := extractRefName(op.Channel.Ref)
		ch, hasChannel := doc.Channels[channelName]
		if !hasChannel {
			continue
		}
		for _, candidate := range governingMessageInventory(doc, &op, &ch, selector+"#message") {
			entries = append(entries, messageCoverage(doc, candidate, id, emitted, exclusion))
		}
		if op.Reply != nil {
			for _, candidate := range replyMessageInventory(doc, &op, selector+"#reply-message") {
				entries = append(entries, messageCoverage(doc, candidate, id, emitted, exclusion))
			}
		}
		for index, member := range effectiveServers(doc, &ch) {
			sourceRef := fmt.Sprintf("%s#server[%d]=%s", selector, index, member.Name)
			if emitted {
				entries = append(entries, synthesize.SynthesisCoverageEntry{
					SourceIndex: 0, SourceRef: sourceRef, Scope: synthesize.SynthesisCoverageAlternative,
					Status: synthesize.SynthesisRepresented, OperationKey: id.operation, BindingSelector: id.selector,
				})
			} else {
				entries = append(entries, synthesize.SynthesisCoverageEntry{
					SourceIndex: 0, SourceRef: sourceRef, Scope: synthesize.SynthesisCoverageAlternative,
					Status: synthesize.SynthesisExcluded, ReasonCode: "asyncapi.parent_target_excluded",
					Rule: "ASYNC-P-02", Message: "the governing operation has no faithfully representable target",
				})
			}
		}
	}
	return entries
}

func coverageExclusion(sourceRef string, scope synthesize.SynthesisCoverageScope, exclusion *authoringExclusion) synthesize.SynthesisCoverageEntry {
	status := synthesize.SynthesisExcluded
	if exclusion.status == "invalid" {
		status = synthesize.SynthesisInvalid
	}
	if exclusion.status == "implementation-unsupported" {
		status = synthesize.SynthesisImplementationUnsupported
	}
	return synthesize.SynthesisCoverageEntry{
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
	selector  string
}, emitted bool, parentExclusion *authoringExclusion) synthesize.SynthesisCoverageEntry {
	if candidate.message == nil {
		return synthesize.SynthesisCoverageEntry{
			SourceIndex: 0, SourceRef: candidate.sourceRef, Scope: synthesize.SynthesisCoverageAlternative,
			Status: synthesize.SynthesisInvalid, ReasonCode: "asyncapi.dangling_message_ref",
			Rule: "ASYNC-D-03", Message: "the message reference does not resolve",
		}
	}
	if candidate.message.Ref != "" || candidate.message.UnresolvedTrait != "" {
		return synthesize.SynthesisCoverageEntry{
			SourceIndex: 0, SourceRef: candidate.sourceRef, Scope: synthesize.SynthesisCoverageAlternative,
			Status: synthesize.SynthesisExcluded, ReasonCode: "asyncapi.message_unresolvable",
			Rule: "ASYNC-P-03", Message: "the message declaration does not resolve faithfully",
		}
	}
	if emitted {
		// The §9.2 floor: a declared non-JSON-Schema representation keeps the
		// operation invocable through an unconstrained direction, but the
		// emitted OBI has lost a contract bespoke artifact code still knows.
		// That is lossy, never fully represented — the coverage surface's
		// honest-partiality promise is the reason this ledger exists.
		switch messagePayloadLossReason(doc, *candidate.message) {
		case "asyncapi.schema_format_not_convertible":
			// Language loss: values still ride JSON, invocation works, the
			// contract is degraded — lossy.
			return synthesize.SynthesisCoverageEntry{
				SourceIndex: 0, SourceRef: candidate.sourceRef, Scope: synthesize.SynthesisCoverageAlternative,
				Status: synthesize.SynthesisLossy, OperationKey: id.operation, BindingSelector: id.selector,
				ReasonCode: "asyncapi.schema_format_not_convertible", Rule: "ASYNC-P-05",
				Message: "the declared schema format has no faithful JSON Schema conversion; the direction is represented by the unconstrained schema",
			}
		case "asyncapi.payload_byte_carriage":
			// Byte-boundary loss: the direction is invocable through the
			// canonical Base64 boundary, but the author-declared payload
			// contract is not expressible at that boundary — lossy, with
			// consumer codec hooks as the enrichment path to logical values.
			return synthesize.SynthesisCoverageEntry{
				SourceIndex: 0, SourceRef: candidate.sourceRef, Scope: synthesize.SynthesisCoverageAlternative,
				Status: synthesize.SynthesisLossy, OperationKey: id.operation, BindingSelector: id.selector,
				ReasonCode: "asyncapi.payload_byte_carriage", Rule: "ASYNC-P-05",
				Message: "the declared media carries bytes; the direction is represented by the canonical Base64 boundary schema, and the declared payload contract is not expressible at that boundary",
			}
		}
		return synthesize.SynthesisCoverageEntry{
			SourceIndex: 0, SourceRef: candidate.sourceRef, Scope: synthesize.SynthesisCoverageAlternative,
			Status: synthesize.SynthesisRepresented, OperationKey: id.operation, BindingSelector: id.selector,
		}
	}
	message := "the governing operation has no faithfully representable target"
	rule := "ASYNC-P-02"
	if parentExclusion != nil {
		message = parentExclusion.message
		rule = parentExclusion.rule
	}
	return synthesize.SynthesisCoverageEntry{
		SourceIndex: 0, SourceRef: candidate.sourceRef, Scope: synthesize.SynthesisCoverageAlternative,
		Status: synthesize.SynthesisExcluded, ReasonCode: "asyncapi.parent_target_excluded",
		Rule: rule, Message: message,
	}
}
