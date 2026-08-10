package asyncapi

import (
	"encoding/json"
	"fmt"
	"mime"
	"sort"
	"strings"
	"unicode/utf8"

	openbindings "github.com/openbindings/openbindings-go"
)

// This file resolves the governing content-type declarations of
// openbindings.asyncapi@2 §9.1 (ASYNC-P-03, input encoding) and §9.3
// (ASYNC-P-05, decode). Effective content type resolves PER MESSAGE first —
// the message's own `contentType`, else the document's
// `defaultContentType`, the AsyncAPI rule — and the governing set's
// distinct effective types decide the lane. Input requires exactly one
// selected message; output requires one coherent effective type. An absent
// type requires the corresponding configuration point. Everything here is
// decided by declarations, never payload bytes.

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

// selectedInputMessages resolves the publish-side message point to exactly
// one artifact declaration. Selection is declarative; payload bytes never
// participate.
func selectedInputMessages(doc *document, op *asyncOperation, ch *channel, bindCtx map[string]any) ([]message, error) {
	type candidate struct {
		name    string
		message message
	}
	var candidates []candidate
	if op != nil && len(op.Messages) > 0 {
		for _, ref := range op.Messages {
			if m := resolveMessageRef(doc, ref); m != nil {
				name := m.Name
				if name == "" {
					name = extractRefName(ref.Ref)
				}
				candidates = append(candidates, candidate{name, *m})
			}
		}
	} else if ch != nil {
		names := make([]string, 0, len(ch.Messages))
		for name := range ch.Messages {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			candidates = append(candidates, candidate{name, ch.Messages[name]})
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("publish operation has no resolved message declaration")
	}
	selected := candidates[0]
	if len(candidates) > 1 {
		choice, _ := openbindings.ContextConfiguration(bindCtx)["message"].(string)
		if choice == "" {
			return nil, fmt.Errorf("publish operation declares several messages; configuration.message must select one artifact-declared member")
		}
		matches := 0
		for _, candidate := range candidates {
			if candidate.name == choice {
				selected = candidate
				matches++
			}
		}
		if matches != 1 {
			return nil, fmt.Errorf("configuration.message %q does not select exactly one operation message", choice)
		}
	}
	if selected.message.Headers != nil {
		return nil, fmt.Errorf("selected message declares headers, which this AsyncAPI binding revision cannot carry")
	}
	return []message{selected.message}, nil
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
// point consults (§9.3, ASYNC-P-05). Runtime callers additionally validate
// that the result belongs to the supported JSON or UTF-8 text carriage.
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
// request-side declaration (§9.1, ASYNC-P-03): a JSON-family type serializes
// the value as JSON; a supported text-family type carries a UTF-8 string.
// Binary/codec-specific media and explicit non-UTF-8 charsets are refused:
// revision 1 has no bytes value or common transcode surface. With no
// declaration, configuration.encode must choose the JSON or text lane. More
// or fewer than one selected message is refused; payload bytes never select
// among artifact alternatives.
func resolveInputCodec(doc *document, msgs []message, contexts ...map[string]any) (inputCodec, error) {
	if len(msgs) != 1 {
		return inputCodec{}, fmt.Errorf("publish input must be governed by exactly one selected message")
	}
	types := distinctEffectiveTypes(doc, msgs)
	if len(types) > 1 {
		return inputCodec{}, fmt.Errorf("selected message has ambiguous effective content type")
	}
	t := ""
	if len(types) == 1 {
		t = types[0]
	}
	switch {
	case t == "":
		var ctx map[string]any
		if len(contexts) > 0 {
			ctx = contexts[0]
		}
		lane, err := requiredCodecLane(ctx, "encode")
		if err != nil {
			return inputCodec{}, err
		}
		return lane, nil
	case isJSONContentType(t):
		effective := messageEffectiveContentType(doc, msgs[0])
		if err := supportedMessageContentType(effective); err != nil {
			return inputCodec{}, err
		}
		return inputCodec{JSON: true, ContentType: effective}, nil
	case isTextContentType(t):
		effective := messageEffectiveContentType(doc, msgs[0])
		if err := supportedMessageContentType(effective); err != nil {
			return inputCodec{}, err
		}
		return inputCodec{JSON: false, ContentType: effective}, nil
	default:
		return inputCodec{}, fmt.Errorf("effective content type %q has no revision-1 value carriage", messageEffectiveContentType(doc, msgs[0]))
	}
}

func messageEffectiveContentType(doc *document, m message) string {
	if m.ContentType != "" {
		return m.ContentType
	}
	return doc.DefaultContentType
}

// supportedMessageContentType enforces the revision-1 value boundary:
// JSON-family and UTF-8 text-family media are representable; absence is
// completed by encode/decode configuration; binary codecs and a non-UTF-8
// charset are not assigned an invented bytes convention.
func supportedMessageContentType(contentType string) error {
	if strings.TrimSpace(contentType) == "" {
		return nil
	}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return fmt.Errorf("invalid media type %q: %v", contentType, err)
	}
	mediaType = strings.ToLower(mediaType)
	if mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json") && !strings.HasPrefix(mediaType, "text/") {
		return fmt.Errorf("effective content type %q has no revision-1 value carriage", contentType)
	}
	if charset := strings.ToLower(strings.TrimSpace(params["charset"])); charset != "" && charset != "utf-8" && charset != "utf8" {
		return fmt.Errorf("effective content type %q declares unsupported non-UTF-8 charset %q", contentType, params["charset"])
	}
	return nil
}

func requiredCodecLane(bindCtx map[string]any, point string) (inputCodec, error) {
	value, _ := openbindings.ContextConfiguration(bindCtx)[point].(string)
	switch value {
	case "json":
		return inputCodec{JSON: true, ContentType: "application/json"}, nil
	case "text":
		return inputCodec{JSON: false, ContentType: "text/plain; charset=utf-8"}, nil
	default:
		return inputCodec{}, fmt.Errorf("configuration.%s must select json or text because the artifact declares no effective content type", point)
	}
}

func resolveSubscriptionContentType(doc *document, msgs []message, bindCtx map[string]any) (string, error) {
	if len(msgs) == 0 {
		return "", fmt.Errorf("operation has no resolved output message declaration")
	}
	identities := map[string]string{}
	for _, m := range msgs {
		if err := validateMessageBindingVersion(m); err != nil {
			return "", err
		}
		if m.Headers != nil {
			return "", fmt.Errorf("output message declares headers, which revision 1 cannot carry")
		}
		ct := m.ContentType
		if ct == "" {
			ct = doc.DefaultContentType
		}
		if err := supportedMessageContentType(ct); err != nil {
			return "", err
		}
		identity := ""
		if ct != "" {
			var err error
			identity, _, err = canonicalMedia(ct)
			if err != nil {
				return "", err
			}
		}
		identities[identity] = ct
	}
	if len(identities) > 1 {
		return "", fmt.Errorf("output messages declare conflicting effective content types")
	}
	for identity, ct := range identities {
		if identity != "" {
			return ct, nil
		}
		lane, err := requiredCodecLane(bindCtx, "decode")
		if err != nil {
			return "", err
		}
		return lane.ContentType, nil
	}
	return "", fmt.Errorf("operation has no output message declaration")
}

func resolveReplyContentType(doc *document, op *asyncOperation, status int, actual string, bindCtx map[string]any) (string, error) {
	all := replyGoverningMessages(doc, op)
	if len(all) == 0 {
		return "", fmt.Errorf("non-empty HTTP response has no artifact-declared reply message")
	}
	var exact, fallback []message
	for _, m := range all {
		if m.Bindings != nil && m.Bindings.HTTP != nil && m.Bindings.HTTP.StatusCode != 0 {
			if m.Bindings.HTTP.StatusCode == status {
				exact = append(exact, m)
			}
		} else {
			fallback = append(fallback, m)
		}
	}
	candidates := fallback
	if len(exact) > 0 {
		candidates = exact
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("non-empty HTTP %d response has no governing reply message", status)
	}
	for _, m := range candidates {
		if err := validateMessageBindingVersion(m); err != nil {
			return "", err
		}
		if m.Headers != nil {
			return "", fmt.Errorf("selected reply message declares headers, which revision 1 cannot carry")
		}
		if err := supportedMessageContentType(messageEffectiveContentType(doc, m)); err != nil {
			return "", err
		}
	}
	type match struct {
		value, identity string
		specificity     int
	}
	var declared []match
	for _, m := range candidates {
		ct := m.ContentType
		if ct == "" {
			ct = doc.DefaultContentType
		}
		if ct == "" {
			continue
		}
		identity, params, err := canonicalMedia(ct)
		if err != nil {
			return "", err
		}
		declared = append(declared, match{ct, identity, len(params)})
	}
	if len(declared) == 0 {
		lane, err := requiredCodecLane(bindCtx, "decode")
		return lane.ContentType, err
	}
	if strings.TrimSpace(actual) == "" {
		return "", fmt.Errorf("reply candidates declare content types but the HTTP response has no Content-Type")
	}
	_, actualParams, err := canonicalMedia(actual)
	if err != nil {
		return "", err
	}
	actualType, _, _ := mime.ParseMediaType(actual)
	var matches []match
	for _, d := range declared {
		dType, dParams, _ := mime.ParseMediaType(d.value)
		if !strings.EqualFold(dType, actualType) {
			continue
		}
		ok := true
		for k, v := range dParams {
			if actualParams[k] != v {
				ok = false
				break
			}
		}
		if ok {
			matches = append(matches, d)
		}
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("HTTP response Content-Type %q matches no reply declaration", actual)
	}
	best := -1
	for _, m := range matches {
		if m.specificity > best {
			best = m.specificity
		}
	}
	var winners []match
	for _, m := range matches {
		if m.specificity == best {
			winners = append(winners, m)
		}
	}
	if len(winners) != 1 {
		return "", fmt.Errorf("HTTP response matches several distinct reply media declarations at equal specificity")
	}
	return winners[0].value, nil
}

func validateMessageBindingVersion(m message) error {
	if m.Bindings != nil && m.Bindings.HTTP != nil {
		v := m.Bindings.HTTP.BindingVersion
		if v != "" && v != "0.3.0" {
			return fmt.Errorf("HTTP message binding version %q is outside revision 1's incorporated 0.3.0 envelope", v)
		}
	}
	return nil
}

func canonicalMedia(value string) (string, map[string]string, error) {
	t, params, err := mime.ParseMediaType(value)
	if err != nil {
		return "", nil, fmt.Errorf("invalid media type %q: %v", value, err)
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(strings.ToLower(t))
	for _, k := range keys {
		b.WriteString(";")
		b.WriteString(strings.ToLower(k))
		b.WriteString("=")
		b.WriteString(params[k])
	}
	return b.String(), params, nil
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
	if !utf8.ValidString(s) {
		return nil, fmt.Errorf("the text-lane input is not valid UTF-8")
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
// The input and output correspondence uses the broader non-JSON string lane;
// this predicate is retained only for callers that specifically need MIME's
// text family.
func isTextContentType(contentType string) bool {
	return strings.HasPrefix(normalizeMediaType(contentType), "text/")
}
