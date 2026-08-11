package asyncapi

import (
	"strings"
	"testing"
)

// Tests for the exported endpoint-resolution seam (ParseDocument /
// Document.ResolveEndpoint): the §9.2 server and address configuration
// points (ASYNC-P-04) exercised through the export directly, the way its
// consumer (ob's delegate frame lane) calls it.

func mustParse(t *testing.T, src string) *Document {
	t.Helper()
	d, err := ParseDocument([]byte(src))
	if err != nil {
		t.Fatalf("ParseDocument: %v", err)
	}
	return d
}

// configuration wraps per-point values the way the binding context carries
// them (the `configuration` field the format consults).
func configuration(points map[string]any) map[string]any {
	return map[string]any{"configuration": points}
}

func TestParseDocument_AcceptedLineOnly(t *testing.T) {
	if _, err := ParseDocument([]byte("asyncapi: \"3.1.2\"\ninfo:\n  title: t\n  version: \"1\"\n")); err == nil {
		t.Fatal("expected ASYNC-P-01 refusal for an unadopted 3.1 patch")
	} else if !strings.Contains(err.Error(), "ASYNC-P-01") {
		t.Errorf("refusal should cite ASYNC-P-01, got: %v", err)
	}
	if _, err := ParseDocument([]byte("{ not json")); err == nil {
		t.Fatal("expected a parse error for malformed JSON")
	}
	// JSON and YAML representations of the same artifact both parse.
	mustParse(t, `{"asyncapi":"3.0.0","info":{"title":"t","version":"1"}}`)
	mustParse(t, `{"asyncapi":"3.1.0","info":{"title":"t","version":"1"}}`)
	mustParse(t, "asyncapi: \"2.6.0\"\ninfo:\n  title: t\n  version: \"1\"\n")
	mustParse(t, "asyncapi: \"3.0.0\"\ninfo:\n  title: t\n  version: \"1\"\n")
}

const frameDoc = `asyncapi: "3.0.0"
info:
  title: delegate
  version: 0.1.0
servers:
  local:
    host: example.com:20290
    protocol: ws
channels:
  bindingsInvoke:
    address: /bindings/invoke
operations:
  invokeBinding:
    action: send
    channel:
      $ref: "#/channels/bindingsInvoke"
`

// TestResolveEndpoint_Default is the consumer's shape: one ws server, a
// concrete address, nil binding context.
func TestResolveEndpoint_Default(t *testing.T) {
	d := mustParse(t, frameDoc)
	ep, err := d.ResolveEndpoint("#/operations/invokeBinding", nil)
	if err != nil {
		t.Fatal(err)
	}
	if ep.URL != "ws://example.com:20290/bindings/invoke" {
		t.Errorf("URL = %q", ep.URL)
	}
	if ep.Protocol != "ws" {
		t.Errorf("Protocol = %q", ep.Protocol)
	}
}

func TestResolveEndpoint_RefGrammar(t *testing.T) {
	d := mustParse(t, frameDoc)

	// ASYNC-D-03: the pointer is the only conformant spelling.
	if _, err := d.ResolveEndpoint("invokeBinding", nil); err == nil {
		t.Error("a bare operation key must be refused (ASYNC-D-03)")
	}
	if _, err := d.ResolveEndpoint("#/operations/nope", nil); err == nil {
		t.Error("an unknown operation must be refused")
	}
	if _, err := d.ResolveEndpoint("", nil); err == nil {
		t.Error("an empty ref must be refused")
	}
}

func TestResolveEndpoint_EscapedOperationKey(t *testing.T) {
	d := mustParse(t, `asyncapi: "3.0.0"
info: {title: t, version: "1"}
servers:
  s: {host: h.example, protocol: ws}
channels:
  c: {address: /c}
operations:
  "ns/op":
    action: send
    channel: {$ref: "#/channels/c"}
`)
	ep, err := d.ResolveEndpoint("#/operations/ns~1op", nil)
	if err != nil {
		t.Fatal(err)
	}
	if ep.URL != "ws://h.example/c" {
		t.Errorf("URL = %q", ep.URL)
	}
}

// TestResolveEndpoint_ServerOrdering: the effective set is the document's
// servers map in lexicographic key order, and the default is the first
// candidate whose protocol revision 1 binds — an out-of-revision protocol is
// skipped, never dialed (§9.2).
func TestResolveEndpoint_ServerOrdering(t *testing.T) {
	d := mustParse(t, `asyncapi: "3.0.0"
info: {title: t, version: "1"}
servers:
  a-broker: {host: mq.example, protocol: mqtt}
  b-web: {host: ws.example, protocol: wss}
  c-web: {host: other.example, protocol: ws}
channels:
  c: {address: /c}
operations:
  op:
    action: send
    channel: {$ref: "#/channels/c"}
`)
	if _, err := d.ResolveEndpoint("#/operations/op", nil); err == nil {
		t.Fatal("several bindable servers must require selection, never declaration-order defaulting")
	}
	ep, err := d.ResolveEndpoint("#/operations/op",
		configuration(map[string]any{"server": map[string]any{"key": "b-web"}}))
	if err != nil {
		t.Fatal(err)
	}
	if ep.URL != "wss://ws.example/c" || ep.Protocol != "wss" {
		t.Errorf("got %+v, want explicitly selected b-web", ep)
	}
}

// TestResolveEndpoint_ChannelServersSubset: a channel's declared servers
// subset IS the effective set, in the artifact's own array order.
func TestResolveEndpoint_ChannelServersSubset(t *testing.T) {
	d := mustParse(t, `asyncapi: "3.0.0"
info: {title: t, version: "1"}
servers:
  a: {host: a.example, protocol: ws}
  b: {host: b.example, protocol: ws}
channels:
  c:
    address: /c
    servers:
      - {$ref: "#/servers/b"}
operations:
  op:
    action: send
    channel: {$ref: "#/channels/c"}
`)
	ep, err := d.ResolveEndpoint("#/operations/op", nil)
	if err != nil {
		t.Fatal(err)
	}
	if ep.URL != "ws://b.example/c" {
		t.Errorf("URL = %q, want the channel's declared subset to win over key order", ep.URL)
	}
}

func TestResolveEndpoint_NoResolvableServer(t *testing.T) {
	d := mustParse(t, `asyncapi: "3.0.0"
info: {title: t, version: "1"}
servers:
  broker: {host: mq.example, protocol: mqtt}
channels:
  c: {address: /c}
operations:
  op:
    action: send
    channel: {$ref: "#/channels/c"}
`)
	if _, err := d.ResolveEndpoint("#/operations/op", nil); err == nil {
		t.Fatal("expected a pre-dispatch refusal when no server speaks a bound protocol")
	}
}

// TestResolveEndpoint_PathnameJoin: §9.2's URL-assembly rule — pathname
// concatenated with the address, exactly one `/` at each join, the pathname
// prefix preserved (concatenation, not RFC 3986 resolution).
func TestResolveEndpoint_PathnameJoin(t *testing.T) {
	d := mustParse(t, `asyncapi: "3.0.0"
info: {title: t, version: "1"}
servers:
  s: {host: h.example, protocol: wss, pathname: /base/}
channels:
  c: {address: /rooms/general}
operations:
  op:
    action: send
    channel: {$ref: "#/channels/c"}
`)
	ep, err := d.ResolveEndpoint("#/operations/op", nil)
	if err != nil {
		t.Fatal(err)
	}
	if ep.URL != "wss://h.example/base/rooms/general" {
		t.Errorf("URL = %q", ep.URL)
	}
}

const variableDoc = `asyncapi: "3.0.0"
info: {title: t, version: "1"}
servers:
  tiered:
    host: "{env}.example.com"
    protocol: wss
    variables:
      env:
        default: prod
        enum: [prod, staging]
  bare:
    host: "{region}.example.com"
    protocol: ws
    variables:
      region: {}
channels:
  c: {address: /c}
operations:
  op:
    action: send
    channel: {$ref: "#/channels/c"}
`

// TestResolveEndpoint_ServerVariables: supplied-else-default substitution
// (the key form's `variables` member, §9.2); the refusals for an
// undefaulted, unsupplied variable, a supplied out-of-enum value, and a
// supplied name the selected server does not declare (ASYNC-P-04).
func TestResolveEndpoint_ServerVariables(t *testing.T) {
	d := mustParse(t, variableDoc)

	// Default resolution: lexicographic order puts "bare" first, but its
	// {region} has no default — a refusal, not a fall-through to "tiered".
	if _, err := d.ResolveEndpoint("#/operations/op", nil); err == nil {
		t.Fatal("an unsubstitutable variable must refuse, never guess")
	}

	// Selecting "tiered" by key resolves its declared default.
	ep, err := d.ResolveEndpoint("#/operations/op",
		configuration(map[string]any{"server": map[string]any{"key": "tiered"}}))
	if err != nil {
		t.Fatal(err)
	}
	if ep.URL != "wss://prod.example.com/c" {
		t.Errorf("URL = %q", ep.URL)
	}

	// A supplied value at the key form's `variables` member wins over the
	// declared default.
	ep, err = d.ResolveEndpoint("#/operations/op",
		configuration(map[string]any{"server": map[string]any{
			"key":       "tiered",
			"variables": map[string]any{"env": "staging"},
		}}))
	if err != nil {
		t.Fatal(err)
	}
	if ep.URL != "wss://staging.example.com/c" {
		t.Errorf("URL = %q", ep.URL)
	}

	// An undefaulted variable is satisfiable only by supply — AsyncAPI
	// declares Server Variable defaults OPTIONAL.
	ep, err = d.ResolveEndpoint("#/operations/op",
		configuration(map[string]any{"server": map[string]any{
			"key":       "bare",
			"variables": map[string]any{"region": "eu"},
		}}))
	if err != nil {
		t.Fatal(err)
	}
	if ep.URL != "ws://eu.example.com/c" {
		t.Errorf("URL = %q", ep.URL)
	}

	// A supplied value outside the declared enum is refused. A full-URL
	// override remains distinct and is tested below.
	_, err = d.ResolveEndpoint("#/operations/op",
		configuration(map[string]any{"server": map[string]any{
			"key":       "tiered",
			"variables": map[string]any{"env": "qa"},
		}}))
	if err == nil {
		t.Fatal("an out-of-enum value must be refused")
	}

	// A supplied name the selected server does not declare is refused,
	// never ignored.
	if _, err := d.ResolveEndpoint("#/operations/op",
		configuration(map[string]any{"server": map[string]any{
			"key":       "tiered",
			"variables": map[string]any{"nope": "x"},
		}})); err == nil {
		t.Error("an undeclared supplied name must be refused")
	} else if !strings.Contains(err.Error(), `names no declared variable of server "tiered"`) {
		t.Errorf("the refusal must name the undeclared variable, got %v", err)
	}

	// A URL replacement bypasses unresolved URL-template variables but still
	// names the selected artifact server whose declarations govern.
	ep, err = d.ResolveEndpoint("#/operations/op",
		configuration(map[string]any{"server": map[string]any{"key": "tiered", "url": "wss://staging.example.com"}}))
	if err != nil {
		t.Fatal(err)
	}
	if ep.URL != "wss://staging.example.com/c" {
		t.Errorf("URL = %q", ep.URL)
	}
}

// TestResolveEndpoint_ServerConfiguration exercises member selection and a
// same-scheme URL replacement of that selected member.
func TestResolveEndpoint_ServerConfiguration(t *testing.T) {
	d := mustParse(t, `asyncapi: "3.0.0"
info: {title: t, version: "1"}
servers:
  a: {host: a.example, protocol: ws}
  b: {host: b.example, protocol: wss}
channels:
  c: {address: /c}
operations:
  op:
    action: send
    channel: {$ref: "#/channels/c"}
`)

	// Member selection by servers-map key, the pinned form.
	ep, err := d.ResolveEndpoint("#/operations/op",
		configuration(map[string]any{"server": map[string]any{"key": "b"}}))
	if err != nil {
		t.Fatal(err)
	}
	if ep.URL != "wss://b.example/c" || ep.Protocol != "wss" {
		t.Errorf("key selection: got %+v", ep)
	}

	// The retired bare-string form is refused with the teaching error.
	if _, err := d.ResolveEndpoint("#/operations/op",
		configuration(map[string]any{"server": "b"})); err == nil {
		t.Error("the retired bare-string form must be refused")
	} else if !strings.Contains(err.Error(), "must be an object") {
		t.Errorf("the refusal must teach the pin, got %v", err)
	}

	// Selection and replacement compose.
	ep, err = d.ResolveEndpoint("#/operations/op",
		configuration(map[string]any{"server": map[string]any{
			"key": "b",
			"url": "wss://override.example",
		}}))
	if err != nil {
		t.Fatal(err)
	}
	if ep.URL != "wss://override.example/c" || ep.Protocol != "wss" {
		t.Errorf("selected URL replacement: got %+v", ep)
	}

	// URL alone cannot choose between several artifact servers.
	if _, err := d.ResolveEndpoint("#/operations/op",
		configuration(map[string]any{"server": map[string]any{"url": "wss://override.example/v2"}})); err == nil {
		t.Error("URL replacement without member selection must refuse when several remain")
	}

	// An out-of-revision scheme on the override is refused pre-dispatch.
	if _, err := d.ResolveEndpoint("#/operations/op",
		configuration(map[string]any{"server": map[string]any{"key": "b", "url": "mqtt://mq.example"}})); err == nil {
		t.Error("an out-of-revision override scheme must be refused")
	}

	// A key that matches no effective-set member is refused.
	if _, err := d.ResolveEndpoint("#/operations/op",
		configuration(map[string]any{"server": map[string]any{"key": "nope"}})); err == nil {
		t.Error("a non-member server key must be refused")
	}
}

// TestResolveEndpoint_AddressPoint: parameter expansion from declared
// defaults and consumer-supplied values; the no-guess refusals (an absent
// address, an unresolved expression, a non-concrete supplied address).
func TestResolveEndpoint_AddressPoint(t *testing.T) {
	d := mustParse(t, `asyncapi: "3.0.0"
info: {title: t, version: "1"}
servers:
  s: {host: h.example, protocol: ws}
channels:
  rooms:
    address: "/rooms/{roomId}"
    parameters:
      roomId: {default: general}
  bare:
    address: "/queues/{queueId}"
    parameters:
      queueId: {}
  addressless: {}
operations:
  joinRoom:
    action: send
    channel: {$ref: "#/channels/rooms"}
  drain:
    action: send
    channel: {$ref: "#/channels/bare"}
  lost:
    action: send
    channel: {$ref: "#/channels/addressless"}
`)

	// Declared default expands.
	ep, err := d.ResolveEndpoint("#/operations/joinRoom", nil)
	if err != nil {
		t.Fatal(err)
	}
	if ep.URL != "ws://h.example/rooms/general" {
		t.Errorf("URL = %q", ep.URL)
	}

	// Consumer-supplied parameter wins over the default.
	ep, err = d.ResolveEndpoint("#/operations/joinRoom",
		configuration(map[string]any{"address": map[string]any{"parameters": map[string]any{"roomId": "ops"}}}))
	if err != nil {
		t.Fatal(err)
	}
	if ep.URL != "ws://h.example/rooms/ops" {
		t.Errorf("URL = %q", ep.URL)
	}

	// A consumer-supplied concrete address is used verbatim.
	ep, err = d.ResolveEndpoint("#/operations/joinRoom",
		configuration(map[string]any{"address": "/rooms/override"}))
	if err != nil {
		t.Fatal(err)
	}
	if ep.URL != "ws://h.example/rooms/override" {
		t.Errorf("URL = %q", ep.URL)
	}

	// An expression with no supplied value and no default is a refusal.
	if _, err := d.ResolveEndpoint("#/operations/drain", nil); err == nil {
		t.Error("an unresolved address expression must be refused")
	}

	// An absent address is a refusal, never a channel-key guess.
	if _, err := d.ResolveEndpoint("#/operations/lost", nil); err == nil {
		t.Error("an absent channel address must be refused")
	}

	// Literal braces never reach the wire, supplied addresses included.
	if _, err := d.ResolveEndpoint("#/operations/joinRoom",
		configuration(map[string]any{"address": "/rooms/{roomId}"})); err == nil {
		t.Error("a non-concrete supplied address must be refused")
	}
}

// TestResolveEndpoint_DanglingOperationReference: an operations-map entry
// that is a Reference Object resolves through it (ASYNC-D-03); one whose
// target does not resolve is refused, never treated as an operation.
func TestResolveEndpoint_DanglingOperationReference(t *testing.T) {
	d := mustParse(t, `asyncapi: "3.0.0"
info: {title: t, version: "1"}
servers:
  s: {host: h.example, protocol: ws}
channels:
  c: {address: /c}
operations:
  aliased: {$ref: "#/components/operations/real"}
  dangling: {$ref: "#/components/operations/missing"}
components:
  operations:
    real:
      action: send
      channel: {$ref: "#/channels/c"}
`)
	ep, err := d.ResolveEndpoint("#/operations/aliased", nil)
	if err != nil {
		t.Fatal(err)
	}
	if ep.URL != "ws://h.example/c" {
		t.Errorf("URL = %q", ep.URL)
	}
	if _, err := d.ResolveEndpoint("#/operations/dangling", nil); err == nil {
		t.Error("a dangling operation Reference Object must be refused")
	}
}
