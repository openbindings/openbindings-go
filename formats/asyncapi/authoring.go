package asyncapi

import (
	"encoding/json"
	"errors"
	"mime"
	"sort"
	"strings"

	openbindings "github.com/openbindings/openbindings-go"
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
		// A caller-input direction whose every alternative declares media the
		// JSON value boundary cannot carry is statically guaranteed to refuse
		// today: input encoding has no codec seam (the decode hook exists but
		// is unreachable behind content-type validation). The synthesis
		// contract forbids emitting an operation guaranteed to refuse as
		// usable, so the target is implementation-unsupported until the codec
		// extension path exists — visible MC2 debt, never laundered.
		allCarriageUnsupported := true
		for _, message := range inputMessages {
			if messagePayloadLossReason(doc, message) != "asyncapi.payload_carriage_unsupported" {
				allCarriageUnsupported = false
				break
			}
		}
		if allCarriageUnsupported {
			return &authoringExclusion{"implementation-unsupported", "asyncapi.payload_carriage_unsupported", "ASYNC-P-05", "every caller-input message declares media without JSON application-value carriage; invocation would refuse before dispatch until a codec extension path exists"}
		}
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
	if defect := operationSchemaDefect(doc, op); defect != nil {
		return defect
	}
	return nil
}

func authoringInputMessages(doc *document, op *asyncOperation, ch *channel) []message {
	return governingMessages(doc, op, ch)
}

// operationBoundarySchemas derives both directions' operation-boundary
// schemas under the complementary caller perspective (ASYNC-P-02): a send
// operation's messages are the invoker's output, a receive operation's the
// invoker's input, and a declared reply is the opposite direction.
func operationBoundarySchemas(doc *document, op *asyncOperation) (input, output map[string]any) {
	switch op.Action {
	case "send":
		output = operationPayloadSchema(doc, op, false)
		if op.Reply != nil {
			input = replyPayloadSchema(doc, op.Reply)
		}
	case "receive":
		input = operationPayloadSchema(doc, op, true)
		if op.Reply != nil {
			output = replyPayloadSchema(doc, op.Reply)
		}
	}
	return input, output
}

// operationSchemaDefect is the §9.2 confinement gate: a payload declaration
// that projects to an ill-formed OBI schema (invalid under its own declared
// dialect, or a reference the artifact leaves dangling) excludes exactly this
// operation — never its siblings and never the artifact. The check validates
// a single-operation probe interface with the core validator so the judgment
// is the same one the emitted document would face (OBI-D-16/D-17).
func operationSchemaDefect(doc *document, op *asyncOperation) *authoringExclusion {
	input, output := operationBoundarySchemas(doc, op)
	if input == nil && output == nil {
		return nil
	}
	probeOp := openbindings.Operation{}
	if input != nil {
		probeOp.Input = decycleOperationSchema(input, doc.raw, "#/operations/probe/input")
	}
	if output != nil {
		probeOp.Output = decycleOperationSchema(output, doc.raw, "#/operations/probe/output")
	}
	probe := openbindings.Interface{
		OpenBindings: openbindings.MaxTestedVersion,
		Name:         "probe",
		Version:      "0.0.0",
		Operations:   map[string]openbindings.Operation{"probe": probeOp},
	}
	if err := probe.Validate(); err != nil {
		return &authoringExclusion{"invalid", "asyncapi.payload_schema_invalid", "ASYNC-P-05", "the payload declaration does not project to a well-formed OBI schema: " + err.Error()}
	}
	return nil
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
	return unionPayloadSchemas(doc, messages)
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
	return unionPayloadSchemas(doc, messages)
}

// unionPayloadSchemas derives the operation-boundary schema for a direction.
// Each governed payload enters an OBI position only under dialect translation
// (translate.go); a message whose declared schemaFormat or effective content
// type identifies a non-JSON-Schema representation cannot contribute a
// faithful schema, so the direction is represented by the unconstrained
// schema per the binding specification's §9.2 floor. Coverage accounts the
// degraded direction; the operation remains invocable.
func unionPayloadSchemas(doc *document, messages []message) map[string]any {
	if len(messages) == 0 {
		return nil
	}
	unique := map[string]map[string]any{}
	for _, message := range messages {
		if messagePayloadNotConvertible(doc, message) {
			return map[string]any{}
		}
		schema, format := effectivePayload(message)
		var translated map[string]any
		if classifySchemaFormat(format) == schemaFormatTranslate {
			translated = translateSchemaDialect(schema)
		} else {
			// Passthrough must not alias document memory: downstream passes
			// (cyclic hoisting) rewrite the boundary schema in place.
			translated = deepCopyMap(schema)
		}
		encoded, _ := json.Marshal(translated)
		unique[string(encoded)] = translated
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

// schemaFormatDisposition classifies a declared schemaFormat by EXACT parsed
// identity, replacing the earlier substring heuristic (which both accepted
// bogus formats and applied Draft-07 rules to other dialects). The pinned
// set:
//
//   - absent → the edition's default AsyncAPI Schema Object (Draft-07
//     superset in every accepted edition) → translate;
//   - application/vnd.aai.asyncapi(+json|+yaml);version=2.x/3.x → the same
//     AsyncAPI Schema Object → translate;
//   - application/schema+json|+yaml;version=draft-07 → JSON Schema Draft 07
//     → translate;
//   - application/schema+json|+yaml;version=draft/2020-12 → already the OBI
//     dialect → passthrough;
//   - anything else (Avro, Protobuf, OpenAPI, RAML, unknown or malformed
//     versions, unparseable media types) → foreign.
type schemaFormatDisposition int

const (
	schemaFormatTranslate schemaFormatDisposition = iota
	schemaFormatPassthrough
	schemaFormatForeign
)

func classifySchemaFormat(format string) schemaFormatDisposition {
	trimmed := strings.TrimSpace(format)
	if trimmed == "" {
		return schemaFormatTranslate
	}
	mediaType, params, err := mime.ParseMediaType(trimmed)
	if err != nil {
		// The JSON Schema version parameter is conventionally spelled
		// unquoted (version=draft/2020-12) even though the slash is not a
		// legal RFC 2045 token character. Fall back to a lenient split, as
		// the TS twin parses.
		mediaType, params, err = lenientParseMediaType(trimmed)
		if err != nil {
			return schemaFormatForeign
		}
	}
	version := strings.ToLower(strings.TrimSpace(params["version"]))
	switch strings.ToLower(mediaType) {
	case "application/vnd.aai.asyncapi", "application/vnd.aai.asyncapi+json", "application/vnd.aai.asyncapi+yaml":
		return schemaFormatTranslate
	case "application/schema+json", "application/schema+yaml":
		switch version {
		case "draft-07":
			return schemaFormatTranslate
		case "draft/2020-12":
			return schemaFormatPassthrough
		default:
			return schemaFormatForeign
		}
	default:
		return schemaFormatForeign
	}
}

// lenientParseMediaType splits "type/subtype;name=value;..." without
// enforcing RFC 2045 token rules on parameter values, stripping optional
// surrounding quotes. Mirrors the TS twin's parser exactly.
func lenientParseMediaType(input string) (string, map[string]string, error) {
	segments := strings.Split(input, ";")
	mediaType := strings.ToLower(strings.TrimSpace(segments[0]))
	if _, _, err := mime.ParseMediaType(mediaType); err != nil {
		return "", nil, err
	}
	params := make(map[string]string, len(segments)-1)
	for _, segment := range segments[1:] {
		name, value, found := strings.Cut(segment, "=")
		if !found {
			return "", nil, errInvalidMediaTypeParameter
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
			value = value[1 : len(value)-1]
		}
		params[strings.ToLower(strings.TrimSpace(name))] = value
	}
	return mediaType, params, nil
}

var errInvalidMediaTypeParameter = errors.New("asyncapi: media type parameter is not name=value")

func foreignSchemaFormat(format string) bool {
	return classifySchemaFormat(format) == schemaFormatForeign
}

// effectivePayload resolves the schema a message actually declares, across
// the editions' two spellings. AsyncAPI 2.x declares `schemaFormat` at the
// MESSAGE level governing `payload` directly; AsyncAPI 3.x moved it into a
// Multi Format Schema Object — `payload: {schemaFormat, schema}` — with a
// bare payload meaning the edition's default Schema Object. The two shapes
// are disjoint after edition normalization, so discrimination follows the
// 3.x spec's own rule: a payload object carrying a string `schemaFormat`
// member IS the wrapper, and the declared schema is its `schema` member.
// Treating the wrapper itself as the schema silently buried the real
// contract under an unknown keyword while reporting full representation.
func effectivePayload(m message) (map[string]any, string) {
	if format, ok := m.Payload["schemaFormat"].(string); ok {
		schema, _ := m.Payload["schema"].(map[string]any)
		return schema, format
	}
	return m.Payload, m.SchemaFormat
}

// messagePayloadNotConvertible reports whether the emitted direction must be
// the unconstrained schema: no payload at all, a declared foreign
// schemaFormat, or an effective content type with no JSON application-value
// carriage. Declaration-driven only — never payload sniffing. This is the
// EMISSION predicate; whether anything was LOST is messagePayloadLossReason's
// separate question — an absent payload declares no contract, so the
// unconstrained schema loses nothing.
func messagePayloadNotConvertible(doc *document, m message) bool {
	schema, _ := effectivePayload(m)
	if schema == nil {
		return true
	}
	return messagePayloadLossReason(doc, m) != ""
}

// messagePayloadLossReason names the lossy-coverage reason when the floored
// direction discards an author-declared contract, or empty when nothing is
// lost. The two causes carry different authority and remediation, so they
// account under distinct codes: a foreign schema format (an Avro or Protobuf
// schema is not a JSON Schema) versus a content type whose bytes the JSON
// value boundary cannot carry even when the schema itself might translate.
func messagePayloadLossReason(doc *document, m message) string {
	schema, format := effectivePayload(m)
	if schema == nil && m.Payload == nil {
		return ""
	}
	if foreignSchemaFormat(format) {
		return "asyncapi.schema_format_not_convertible"
	}
	// A wrapper with a foreign-format schema handled above; a wrapper whose
	// schema member is absent declares no contract in a recognized language.
	if schema == nil {
		return ""
	}
	if supportedMessageContentType(messageEffectiveContentType(doc, m)) != nil {
		return "asyncapi.payload_carriage_unsupported"
	}
	return ""
}
