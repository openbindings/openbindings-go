package asyncapi

import (
	"testing"

	"github.com/openbindings/openbindings-go/invoke"
)

// config.value schema (2026-08-20 working-draft amendment): the configRequired
// signal carries an engine-asserted JSON Schema where the artifact declares a
// closed value set ({"enum": […]}), absent otherwise; `choices` is removed.

func TestEnumSchema(t *testing.T) {
	if enumSchema(nil) != nil {
		t.Error("no declared values assert nothing (nil = absent)")
	}
	schema := enumSchema([]string{"prod", "staging"})
	members, _ := schema["enum"].([]any)
	if len(members) != 2 || members[0] != "prod" || members[1] != "staging" {
		t.Errorf("enumSchema = %v, want {\"enum\": [prod staging]}", schema)
	}
}

func TestResolveTarget_SeveralServersChallengeCarriesEnumSchema(t *testing.T) {
	doc := &document{Servers: map[string]server{
		"eu":   {Host: "eu.example.com", Protocol: "wss"},
		"us":   {Host: "us.example.com", Protocol: "wss"},
		"mqtt": {Host: "q.example.com", Protocol: "mqtt"},
	}}
	_, err := resolveTarget(doc, nil, nil)
	cr, ok := err.(*configRequired)
	if !ok {
		t.Fatalf("expected *configRequired, got %v", err)
	}
	if cr.point != "server" || cr.path != "/key" {
		t.Fatalf("challenge = {point:%q path:%q}, want {server /key}", cr.point, cr.path)
	}
	members, _ := cr.schema["enum"].([]any)
	if len(members) != 2 {
		t.Fatalf("schema = %v, want an enum of the two bindable member keys", cr.schema)
	}
}

func TestResolveTarget_UndefaultedVariableChallengeCarriesEnumSchema(t *testing.T) {
	doc := &document{Servers: map[string]server{
		"prod": {Host: "{env}.example.com", Protocol: "wss", Variables: map[string]serverVariable{
			"env": {Enum: []string{"eu", "us"}},
		}},
	}}
	_, err := resolveTarget(doc, nil, nil)
	cr, ok := err.(*configRequired)
	if !ok {
		t.Fatalf("expected *configRequired, got %v", err)
	}
	if cr.point != "server" || cr.path != "/variables/env" {
		t.Fatalf("challenge = {point:%q path:%q}, want {server /variables/env}", cr.point, cr.path)
	}
	members, _ := cr.schema["enum"].([]any)
	if len(members) != 2 || members[0] != "eu" || members[1] != "us" {
		t.Errorf("schema = %v, want the declared enum", cr.schema)
	}
	if cr.hostHint != "{env}.example.com" {
		t.Errorf("hostHint = %q, want the artifact host template", cr.hostHint)
	}
}

// Stage 0 scope assertion (context-scope model, ratified 2026-08-19): the
// challenge target falls back resolved server URL → artifact host hint →
// canonicalized source location; a content-only source asserts nothing.
func TestConfigOrSourceError_TargetFallbackChain(t *testing.T) {
	base := configRequired{point: "server", path: "/key", description: "select a member"}

	withHint := base
	withHint.hostHint = "broker.example.com"
	if got := challengeTarget(t, configOrSourceError(&withHint, "wss://resolved.example.com", "https://example.com/spec.yaml")); got != "wss://resolved.example.com" {
		t.Errorf("target = %q, want the resolved server URL to win", got)
	}
	if got := challengeTarget(t, configOrSourceError(&withHint, "", "https://example.com/spec.yaml")); got != "broker.example.com" {
		t.Errorf("target = %q, want the artifact host hint next", got)
	}
	noHint := base
	if got := challengeTarget(t, configOrSourceError(&noHint, "", "HTTPS://Example.com:443/specs/./a.yaml")); got != "https://example.com/specs/a.yaml" {
		t.Errorf("target = %q, want the canonicalized source location", got)
	}
	if got := challengeTarget(t, configOrSourceError(&noHint, "", "")); got != "" {
		t.Errorf("target = %q, want empty for a content-only source (asserts nothing)", got)
	}
}

func TestConfigOrSourceError_RequirementCarriesSchema(t *testing.T) {
	durable := true
	cr := &configRequired{
		point: "server", path: "/key", description: "select a member",
		schema:  map[string]any{"enum": []any{"eu", "us"}},
		durable: &durable,
	}
	details := challengeDetails(t, configOrSourceError(cr, "", ""))
	req := details.Alternatives[0].Requirements[0]
	schema, _ := req.Extra["schema"].(map[string]any)
	if members, _ := schema["enum"].([]any); len(members) != 2 {
		t.Errorf("requirement schema = %v, want the signal's enum schema", req.Extra["schema"])
	}
	if _, present := req.Extra["choices"]; present {
		t.Error("choices is removed from the contract; nothing may emit it")
	}
}

func challengeDetails(t *testing.T, err *invoke.InvocationError) *invoke.ContextRequiredDetails {
	t.Helper()
	details := invoke.ContextRequiredFrom(err)
	if details == nil {
		t.Fatalf("expected a CONTEXT_REQUIRED challenge, got %v", err)
	}
	if len(details.Alternatives) != 1 || len(details.Alternatives[0].Requirements) != 1 {
		t.Fatalf("expected one alternative with one requirement, got %+v", details.Alternatives)
	}
	return details
}

func challengeTarget(t *testing.T, err *invoke.InvocationError) string {
	t.Helper()
	return challengeDetails(t, err).Target
}
