package asyncapi

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// This file resolves the governing content-type declarations of
// openbindings.asyncapi@1 §9.1 (ASYNC-P-03, input encoding) and §9.3
// (ASYNC-P-05, decode). Effective content type resolves PER MESSAGE first —
// the message's own `contentType`, else the document's
// `defaultContentType`, the AsyncAPI rule — and the governing set's
// distinct effective types decide the lane: exactly one selects it; none,
// or more than one (an ambiguous declaration), falls to the text lane
// rather than guessing. Everything here is decided by declarations, never
// payload bytes.

// governingMessages returns the operation's own governing message set — the
// declarations governing a subscription's outputs and a publish's input
// encoding: the operation's `messages` (resolved), else ALL of the
// operation channel's messages (the AsyncAPI rule for an operation that
// declares no `messages`), in sorted key order for determinism. An
// unresolvable message $ref contributes nothing (the core's
// partial-verification posture).
func governingMessages(doc *document, op *asyncOperation, ch *channel) []message {
	if op != nil && len(op.Messages) > 0 {
		out := make([]message, 0, len(op.Messages))
		for _, ref := range op.Messages {
			if m := resolveMessageRef(doc, ref); m != nil {
				out = append(out, *m)
			}
		}
		return out
	}
	if ch == nil {
		return nil
	}
	return channelMessages(ch)
}

// replyGoverningMessages returns the REPLY-side governing message set — the
// declarations governing a publish invocation's output (direction-correct
// decode, ASYNC-P-05): the reply's `messages` (resolved), else the reply
// channel's messages.
func replyGoverningMessages(doc *document, op *asyncOperation) []message {
	if op == nil || op.Reply == nil {
		return nil
	}
	if len(op.Reply.Messages) > 0 {
		out := make([]message, 0, len(op.Reply.Messages))
		for _, ref := range op.Reply.Messages {
			if m := resolveMessageRef(doc, ref); m != nil {
				out = append(out, *m)
			}
		}
		return out
	}
	if op.Reply.Channel != nil {
		if ch, ok := doc.Channels[extractRefName(op.Reply.Channel.Ref)]; ok {
			return channelMessages(&ch)
		}
	}
	return nil
}

// channelMessages returns a channel's messages in sorted key order (the
// map is unordered; sorting is a determinism choice, and the distinct-set
// computation below is order-insensitive anyway).
func channelMessages(ch *channel) []message {
	names := make([]string, 0, len(ch.Messages))
	for name := range ch.Messages {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]message, 0, len(names))
	for _, name := range names {
		out = append(out, ch.Messages[name])
	}
	return out
}

// distinctEffectiveTypes resolves each governing message's effective
// content type per the AsyncAPI rule (its `contentType`, else the
// document's `defaultContentType`) and returns the distinct set, in
// first-appearance order. Types are normalized for the distinctness test
// (lowercased, parameters stripped — a charset parameter never makes a type
// distinct). A message resolving to no declaration at all contributes the
// empty type as its own distinct member: a set mixing declared and
// undeclared messages is ambiguous, never silently collapsed onto the
// declared type.
func distinctEffectiveTypes(doc *document, msgs []message) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range msgs {
		ct := m.ContentType
		if ct == "" {
			ct = doc.DefaultContentType
		}
		norm := normalizeMediaType(ct)
		if !seen[norm] {
			seen[norm] = true
			out = append(out, norm)
		}
	}
	return out
}

// decodeContentType collapses a governing set to the declaration the decode
// point consults (§9.3, ASYNC-P-05): exactly one distinct effective type
// selects the lane (strict JSON for application/json and +json, text
// otherwise); none, or more than one distinct type, is the text lane ("" —
// builtinDecodeFor's non-JSON lane), never a guess.
func decodeContentType(doc *document, msgs []message) string {
	types := distinctEffectiveTypes(doc, msgs)
	if len(types) == 1 {
		return types[0]
	}
	return ""
}

// inputCodec is §9.1's resolved input encoding: the input value is the
// message payload, wholesale (ASYNC-P-03), rendered per the governing
// request-side declaration.
type inputCodec struct {
	// JSON serializes the value as JSON; otherwise the text lane applies
	// (a string value sent raw; a non-string value is refused).
	JSON bool
	// ContentType is the declared type the wire carries ("" when the
	// declaration is ambiguous and names no one type).
	ContentType string
}

// resolveInputCodec resolves the input encoding from the governing
// request-side declaration (§9.1, ASYNC-P-03): a JSON-family type — or no
// declaration at all, this specification's default — serializes the value
// as JSON; a text-family type sends a string value raw; any other declared
// family (binary, avro, protobuf, …) is EXCLUDED from revision 1 and
// refused before dispatch. A governing set with more than one distinct
// effective type is ambiguous and falls to the text lane, mirroring §9.3's
// decode rule. Forwarded frames on the duplex subscription cell use the
// same rule.
func resolveInputCodec(doc *document, msgs []message) (inputCodec, error) {
	types := distinctEffectiveTypes(doc, msgs)
	if len(types) > 1 {
		return inputCodec{JSON: false, ContentType: ""}, nil // ambiguous → the text lane
	}
	t := ""
	if len(types) == 1 {
		t = types[0]
	}
	switch {
	case t == "":
		// No declaration at all: JSON, the specification's default.
		return inputCodec{JSON: true, ContentType: "application/json"}, nil
	case isJSONContentType(t):
		return inputCodec{JSON: true, ContentType: t}, nil
	case isTextContentType(t):
		return inputCodec{JSON: false, ContentType: t}, nil
	default:
		return inputCodec{}, fmt.Errorf("declared content type %q is neither a JSON- nor a text-family type: excluded from openbindings.asyncapi@1 revision 1 and refused before dispatch", t)
	}
}

// encodeInput renders one caller value as message-payload bytes under the
// resolved codec: the JSON lane marshals; the text lane requires a string
// value and sends it raw — a non-string value there is refused (§9.1).
func encodeInput(codec inputCodec, v any) ([]byte, error) {
	if codec.JSON {
		return json.Marshal(v)
	}
	s, ok := v.(string)
	if !ok {
		return nil, fmt.Errorf("the governing declaration selects the text lane: the input value must be a string, got %T", v)
	}
	return []byte(s), nil
}

// normalizeMediaType lowercases a media type and strips its parameters:
// type/subtype matching ignores parameters (a charset never changes the
// lane). Mirrors openapi's normalizeMediaType (format packages do not share
// private helpers).
func normalizeMediaType(contentType string) string {
	mt := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.Index(mt, ";"); i >= 0 {
		mt = strings.TrimSpace(mt[:i])
	}
	return mt
}

// isTextContentType reports a text-family type: the `text/*` primary type.
// Application-tree types that happen to be textual (application/xml, …) are
// NOT the text family — on the input side they are excluded families
// (§9.1); on the decode side everything non-JSON is the text lane anyway.
func isTextContentType(contentType string) bool {
	return strings.HasPrefix(normalizeMediaType(contentType), "text/")
}
