package asyncapi

import (
	"encoding/json"
	"sort"
)

// The routed operation envelope (§9.2, ruled 2026-08-14): a location-less
// channel parameter is per-invocation application data — the address is
// spelled from its values anew each invocation — and a declared Message
// Object headers contract is application data by AsyncAPI's own definition.
// Both ride the affected direction's operation-facing value as one JSON
// object in place of the bare payload: the payload under the
// protocol-neutral field "payload", declared headers under "headers", each
// location-less parameter under its own name (input direction only). A
// direction with neither keeps the bare wholesale payload — the envelope
// never appears undeclared. When several governing messages cover one
// direction, the union is over PER-MESSAGE envelopes: each alternative's
// payload pairs with its own declared headers, never a cross-product.

// envelopeParam is one location-less channel parameter entering the input
// envelope. A parameter whose location declares a runtime expression stays
// derived from that expression's source (the artifact's own bridge) and
// never becomes an envelope field.
type envelopeParam struct {
	name   string
	schema map[string]any
}

// channelEnvelopeParams returns the channel's location-less parameters in
// sorted name order (deterministic emission).
func channelEnvelopeParams(ch *channel) []envelopeParam {
	if ch == nil || len(ch.Parameters) == 0 {
		return nil
	}
	names := make([]string, 0, len(ch.Parameters))
	for name := range ch.Parameters {
		if ch.Parameters[name].Location == "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	out := make([]envelopeParam, 0, len(names))
	for _, name := range names {
		out = append(out, envelopeParam{name: name, schema: parameterEnvelopeSchema(ch.Parameters[name])})
	}
	return out
}

// parameterEnvelopeSchema derives one parameter's field schema. A 3.x
// Parameter Object is string-valued by definition (enum/default/description
// members only); a 2.x Parameter Object may declare a Schema Object, which
// enters under the ordinary dialect translation.
func parameterEnvelopeSchema(p parameter) map[string]any {
	if p.Schema != nil {
		return translateSchemaDialect(deepCopyMap(p.Schema))
	}
	schema := map[string]any{"type": "string"}
	if len(p.Enum) > 0 {
		values := make([]any, 0, len(p.Enum))
		for _, member := range p.Enum {
			values = append(values, member)
		}
		schema["enum"] = values
	}
	if p.Default != "" {
		schema["default"] = p.Default
	}
	if p.Description != "" {
		schema["description"] = p.Description
	}
	return schema
}

// directionNeedsEnvelope reports whether a direction's operation-facing
// value is the routed envelope: any governing message declares headers, or
// (input side) the channel declares location-less parameters.
func directionNeedsEnvelope(msgs []message, params []envelopeParam) bool {
	if len(params) > 0 {
		return true
	}
	for _, m := range msgs {
		if m.Headers != nil {
			return true
		}
	}
	return false
}

// envelopePayloadFieldName resolves the reserved field's name against the
// parameter set: a parameter literally named "payload" (or "headers") keeps
// its own name — parameter identity is caller-facing — and the reserved
// field takes the deterministic "_value"-suffixed spelling. Field naming is
// a synthesis concern the specification delegates (§9.2).
func envelopeFieldName(reserved string, params []envelopeParam) string {
	for _, p := range params {
		if p.name == reserved {
			return reserved + "_value"
		}
	}
	return reserved
}

// headersBoundarySchema derives one message's declared headers contract for
// its envelope field, under the same declaration pipeline as payloads:
// dialect translation for the default and Draft-07 forms, passthrough for
// declared 2020-12, the Avro derivation for the named correspondence, and
// the unconstrained schema for a foreign representation.
func headersBoundarySchema(m message) map[string]any {
	declared := m.Headers
	format := ""
	if wrapped, ok := declared["schemaFormat"].(string); ok {
		inner, _ := declared["schema"].(map[string]any)
		declared, format = inner, wrapped
	}
	if declared == nil {
		return map[string]any{}
	}
	switch classifySchemaFormat(format) {
	case schemaFormatTranslate:
		return translateSchemaDialect(declared)
	case schemaFormatPassthrough:
		return deepCopyMap(declared)
	case schemaFormatAvro:
		if derived, ok := deriveAvroSchema(declared); ok {
			return derived
		}
		return map[string]any{}
	default:
		return map[string]any{}
	}
}

// unionDirectionSchemas derives one direction's operation-boundary schema.
// Without envelope conditions it is exactly unionPayloadSchemas; with them,
// each governing message contributes its own envelope object (payload
// paired with its own headers, parameters on the input direction), deduped
// and unioned under the same deterministic ordering.
func unionDirectionSchemas(doc *document, msgs []message, params []envelopeParam, includePayload bool) map[string]any {
	if !directionNeedsEnvelope(msgs, params) {
		if !includePayload {
			return nil
		}
		return unionPayloadSchemas(doc, msgs)
	}
	if len(msgs) == 0 {
		// A parameter-only input envelope: addressing data is caller input
		// even when no payload travels (subscribe perspective).
		return envelopeObject(nil, nil, params, nil)
	}
	payloadField := envelopeFieldName("payload", params)
	headersField := envelopeFieldName("headers", params)
	unique := map[string]map[string]any{}
	for _, m := range msgs {
		payload := messagePayloadBoundarySchema(doc, m)
		var headers map[string]any
		required := []any{payloadField}
		if m.Headers != nil {
			headers = headersBoundarySchema(m)
			required = append(required, headersField)
		}
		envelope := envelopeObject(payload, headers, params, required)
		encoded, _ := json.Marshal(envelope)
		unique[string(encoded)] = envelope
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

// envelopeObject assembles one closed envelope. Parameter fields are never
// required: invocation context may pre-fill them (the amortized supply of
// an input), and a parameter with neither source is the ordinary
// pre-dispatch context negotiation.
func envelopeObject(payload, headers map[string]any, params []envelopeParam, required []any) map[string]any {
	properties := map[string]any{}
	if payload != nil {
		properties[envelopeFieldName("payload", params)] = payload
	}
	if headers != nil {
		properties[envelopeFieldName("headers", params)] = headers
	}
	for _, p := range params {
		properties[p.name] = p.schema
	}
	envelope := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		envelope["required"] = required
	}
	return envelope
}
