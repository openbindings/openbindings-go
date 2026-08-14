package asyncapi

import (
	"encoding/json"
	"fmt"
	"sort"
)

// The routed operation envelope (openbindings.asyncapi@1 §9.2, ruled
// 2026-08-14): a channel that declares location-less parameters makes the
// publish input an envelope object — the payload under the protocol-neutral
// "payload" field, each parameter under its own name — because addressing
// data varies per invocation and is therefore operation input.
// configuration.address.parameters remains the amortized pre-fill; an
// explicitly supplied envelope field wins.

// locationlessParamNames returns the channel's location-less parameter
// names, sorted. A parameter whose location declares a runtime expression
// stays derived from that expression's source and never rides the envelope.
func locationlessParamNames(ch *channel) []string {
	if ch == nil {
		return nil
	}
	var names []string
	for name, declared := range ch.Parameters {
		if declared.Location == "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// invokerEnvelopeFieldName mirrors synthesis field naming: a parameter
// literally named "payload" (or "headers") keeps its own name and the
// reserved field takes the "_value"-suffixed spelling.
func invokerEnvelopeFieldName(reserved string, params []string) string {
	for _, name := range params {
		if name == reserved {
			return reserved + "_value"
		}
	}
	return reserved
}

// splitInputEnvelope separates a publish input riding the routed envelope
// into its payload value and address-parameter values. envelope=false means
// the channel declares no location-less parameters and the value is the
// bare wholesale payload, unchanged. (Headers-bearing input messages refuse
// earlier as an uncarried capability, so "headers" is not split here yet.)
func splitInputEnvelope(ch *channel, value any) (payload any, params map[string]string, envelope bool, err error) {
	names := locationlessParamNames(ch)
	if len(names) == 0 {
		return value, nil, false, nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, nil, true, fmt.Errorf("the parameterized channel's input is the routed envelope: supply one object carrying %q and the channel parameters, got %T", "payload", value)
	}
	payloadField := invokerEnvelopeFieldName("payload", names)
	declared := map[string]bool{payloadField: true}
	for _, name := range names {
		declared[name] = true
	}
	params = map[string]string{}
	for field, member := range object {
		if !declared[field] {
			return nil, nil, true, fmt.Errorf("the routed envelope is closed: field %q matches neither %q nor a location-less channel parameter", field, payloadField)
		}
		if field == payloadField {
			continue
		}
		text, terr := parameterText(member)
		if terr != nil {
			return nil, nil, true, fmt.Errorf("envelope parameter %q: %v", field, terr)
		}
		params[field] = text
	}
	supplied, present := object[payloadField]
	if !present {
		return nil, nil, true, fmt.Errorf("the routed envelope requires the %q field carrying the message payload", payloadField)
	}
	return supplied, params, true, nil
}

// parameterText renders one envelope parameter value for address expansion:
// strings ride as-is; other scalars ride their JSON text (a 2.x Parameter
// Object may declare a non-string schema); structured values have no
// address spelling and refuse.
func parameterText(value any) (string, error) {
	switch v := value.(type) {
	case string:
		return v, nil
	case bool, float64, json.Number:
		encoded, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	default:
		return "", fmt.Errorf("an address parameter value must be a scalar, got %T", value)
	}
}
