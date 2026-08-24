package asyncapi

import (
	"errors"
	"strings"
	"testing"

	"github.com/openbindings/openbindings-go/invoke"
)

// TestParseRef_BareIDRefused pins ASYNC-D-03: the JSON Pointer
// `#/operations/<operation-key>` is the ONLY conformant spelling — a bare
// operation key (the former documented lenience) is refused loudly.
func TestParseRef_BareIDRefused(t *testing.T) {
	_, err := parseSelector("sendMessage")
	if err == nil {
		t.Fatal("expected a refusal for a bare operation key (ASYNC-D-03)")
	}
	if !strings.Contains(err.Error(), "ASYNC-D-03") {
		t.Errorf("refusal should cite ASYNC-D-03, got: %v", err)
	}
}

// TestParseRef_UnescapedSlashRefused pins ASYNC-D-03's escaping clause: an
// unescaped `/` after the pointer prefix addresses a deeper path, not an
// operations-map entry.
func TestParseRef_UnescapedSlashRefused(t *testing.T) {
	_, err := parseSelector("#/operations/tasks/create")
	if err == nil {
		t.Fatal("expected a refusal for an unescaped / in the operation-key position (ASYNC-D-03)")
	}
	if !strings.Contains(err.Error(), "ASYNC-D-03") {
		t.Errorf("refusal should cite ASYNC-D-03, got: %v", err)
	}
}

func TestParseRef_HashOperations(t *testing.T) {
	got, err := parseSelector("#/operations/receiveEvents")
	if err != nil {
		t.Fatal(err)
	}
	if got != "receiveEvents" {
		t.Errorf("parseSelector(%q) = %q, want %q", "#/operations/receiveEvents", got, "receiveEvents")
	}
}

func TestParseRef_Empty(t *testing.T) {
	_, err := parseSelector("")
	if err == nil {
		t.Error("expected error for empty selector")
	}
}

// TestParseRef_RFC6901Escaping verifies ASYNC-D-03: operation keys
// containing `/` or `~` carry RFC 6901 escaping in the pointer (~1 → /,
// ~0 → ~).
func TestParseRef_RFC6901Escaping(t *testing.T) {
	got, err := parseSelector("#/operations/orders~1create~0v2")
	if err != nil {
		t.Fatal(err)
	}
	if got != "orders/create~v2" {
		t.Errorf("parseSelector = %q, want %q", got, "orders/create~v2")
	}
}

func TestResolveTarget_HTTPServer(t *testing.T) {
	doc := &document{
		Servers: map[string]server{
			"prod": {Host: "api.example.com", Protocol: "https"},
		},
	}
	target, err := resolveTarget(doc, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if target.Protocol != "https" {
		t.Errorf("protocol = %q, want https", target.Protocol)
	}
	if target.ServerURL != "https://api.example.com" {
		t.Errorf("url = %q, want https://api.example.com", target.ServerURL)
	}
	if target.SecurityServer == nil || target.SecurityServer.Host != "api.example.com" {
		t.Errorf("SecurityServer = %+v, want the selected server", target.SecurityServer)
	}
}

func TestResolveTarget_MetadataOverride(t *testing.T) {
	doc := &document{
		Servers: map[string]server{
			"prod": {Host: "api.example.com", Protocol: "https"},
		},
	}
	ctx := map[string]any{"metadata": map[string]any{"baseURL": "https://localhost:8080"}}
	target, err := resolveTarget(doc, nil, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if target.Protocol != "https" {
		t.Errorf("protocol = %q, want https", target.Protocol)
	}
	if target.ServerURL != "https://localhost:8080" {
		t.Errorf("url = %q, want https://localhost:8080", target.ServerURL)
	}
	// A URL replacement retains the selected artifact server and protocol.
	if target.SecurityServer == nil || target.SecurityServer.Host != "api.example.com" {
		t.Errorf("SecurityServer = %+v, want the sole selected server", target.SecurityServer)
	}
}

// An artifact declaring no server is a complete target whose reachability is
// consumer configuration (ruled 2026-08-13, R1+R5): unconfigured invocation
// challenges CONTEXT_REQUIRED (config.value, point server, path /url), and a
// configured url alone supplies the whole connection URL, its scheme carrying
// the protocol.
func TestResolveTarget_NoServers(t *testing.T) {
	doc := &document{}
	_, err := resolveTarget(doc, nil, nil)
	var cr *configRequired
	if !errors.As(err, &cr) {
		t.Fatalf("expected a config-required challenge, got %v", err)
	}
	if cr.point != "server" || cr.path != "/url" {
		t.Fatalf("challenge = point %q path %q; want server /url", cr.point, cr.path)
	}

	cfg := func(value any) map[string]any {
		return map[string]any{"configuration": map[string]any{"server": value}}
	}
	target, err := resolveTarget(doc, nil, cfg(map[string]any{"url": "wss://broker.example.com/v1"}))
	if err != nil {
		t.Fatal(err)
	}
	if target.ServerURL != "wss://broker.example.com/v1" || target.Protocol != "wss" {
		t.Fatalf("direct url target = %+v", target)
	}

	// variables complete an artifact-declared server; with none declared the
	// spelling is a loud refusal, never ignored.
	if _, err := resolveTarget(doc, nil, cfg(map[string]any{"url": "wss://broker.example.com", "variables": map[string]any{"x": "y"}})); err == nil {
		t.Fatal("variables without an artifact server must refuse")
	}

	// An unbound scheme refuses pre-dispatch, never dials.
	if _, err := resolveTarget(doc, nil, cfg(map[string]any{"url": "kafka://broker.example.com:9092"})); err == nil {
		t.Fatal("an unbound scheme must refuse")
	}
}

// TestResolveTarget_PinnedShapes exercises this SDK's composable server
// carriage: key selects the artifact member and url may replace that same
// member's target without changing protocol or governing declarations.
func TestResolveTarget_PinnedShapes(t *testing.T) {
	doc := &document{
		Servers: map[string]server{
			"backup": {Host: "backup.example.com", Protocol: "wss"},
			"prod":   {Host: "api.example.com", Protocol: "https"},
		},
	}

	// {"key": ...}: member selection by servers-map key.
	target, err := resolveTarget(doc, nil, map[string]any{"configuration": map[string]any{
		"server": map[string]any{"key": "backup"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if target.ServerURL != "wss://backup.example.com" || target.Protocol != "wss" {
		t.Errorf("key selection: got %+v", target)
	}
	if target.SecurityServer == nil || target.SecurityServer.Host != "backup.example.com" {
		t.Errorf("SecurityServer = %+v, want the selected server", target.SecurityServer)
	}

	// {"key", "url"}: select the member and replace its connection URL.
	target, err = resolveTarget(doc, nil, map[string]any{"configuration": map[string]any{
		"server": map[string]any{"key": "backup", "url": "wss://localhost:9090/base"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if target.ServerURL != "wss://localhost:9090/base" || target.Protocol != "wss" {
		t.Errorf("url override: got %+v", target)
	}
	if target.SecurityServer == nil || target.SecurityServer.Host != "backup.example.com" {
		t.Errorf("SecurityServer = %+v, want the explicitly selected server", target.SecurityServer)
	}
}

// TestResolveTarget_ServerVariablesCarriage pins §9.2's `variables` member
// of the server pin's key form (ratified 2026-07-21): supplied values for
// the selected server's own declared variables, substitution
// supplied-else-default-else-refusal, a supplied value outside the declared
// enum refused (upstream SHOULD, hardened to a refusal — the
// specification's own pin), an undeclared supplied name refused, never
// ignored. AsyncAPI declares a Server Variable's default OPTIONAL, so an
// undefaulted variable is satisfiable only by consumer supply.
func TestResolveTarget_ServerVariablesCarriage(t *testing.T) {
	doc := &document{
		Servers: map[string]server{
			"tiered": {
				Host:     "{env}.example.com",
				Protocol: "wss",
				PathName: "/{version}",
				Variables: map[string]serverVariable{
					"env":     {Default: "prod", Enum: []string{"prod", "staging"}},
					"version": {Default: "v1"},
				},
			},
			"bare": {
				Host:     "{tenant}.example.com",
				Protocol: "ws",
				Variables: map[string]serverVariable{
					"tenant": {}, // no default: satisfiable only by supply
				},
			},
		},
	}
	cfg := func(server any) map[string]any {
		return map[string]any{"configuration": map[string]any{"server": server}}
	}

	// A supplied value wins over the declared default.
	target, err := resolveTarget(doc, nil, cfg(map[string]any{
		"key": "tiered", "variables": map[string]any{"env": "staging"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if target.ServerURL != "wss://staging.example.com/v1" {
		t.Errorf("supplied-over-default: url = %q, want wss://staging.example.com/v1", target.ServerURL)
	}

	// An unsupplied variable falls to its declared default; an empty
	// variables object is the same as none.
	target, err = resolveTarget(doc, nil, cfg(map[string]any{
		"key": "tiered", "variables": map[string]any{},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if target.ServerURL != "wss://prod.example.com/v1" {
		t.Errorf("default fallback: url = %q, want wss://prod.example.com/v1", target.ServerURL)
	}

	// An undefaulted variable is satisfiable by supply — the carriage the
	// assembly rule presupposes.
	target, err = resolveTarget(doc, nil, cfg(map[string]any{
		"key": "bare", "variables": map[string]any{"tenant": "acme"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if target.ServerURL != "ws://acme.example.com" {
		t.Errorf("undefaulted-by-supply: url = %q, want ws://acme.example.com", target.ServerURL)
	}

	// Undefaulted and unsupplied: a pre-dispatch refusal whose remedy is
	// supplying the variable.
	_, err = resolveTarget(doc, nil, cfg(map[string]any{"key": "bare"}))
	if err == nil {
		t.Fatal("an undefaulted, unsupplied variable must refuse")
	}
	if want := `server "bare": variable "tenant" has no supplied value and no declared default (supply one at the server configuration point's "variables" member)`; err.Error() != want {
		t.Errorf("undefaulted refusal = %q\n                 want %q", err.Error(), want)
	}

	// A supplied value outside the declared enum is refused.
	_, err = resolveTarget(doc, nil, cfg(map[string]any{
		"key": "tiered", "variables": map[string]any{"env": "qa"},
	}))
	if err == nil {
		t.Fatal("an out-of-enum value must be refused")
	}

	// A supplied name the selected server does not declare is refused,
	// never ignored — even when every template expression would resolve.
	_, err = resolveTarget(doc, nil, cfg(map[string]any{
		"key": "tiered", "variables": map[string]any{"region": "eu"},
	}))
	if err == nil {
		t.Fatal("an undeclared supplied name must refuse")
	}
	if want := `configuration.server.variables["region"] names no declared variable of server "tiered"`; err.Error() != want {
		t.Errorf("undeclared-name refusal = %q, want %q", err.Error(), want)
	}
}

// TestResolveTarget_ShapeTeachingErrors pins the refusal text for malformed
// values of this SDK's server-point carriage, byte-identical to the TS SDK.
func TestResolveTarget_ShapeTeachingErrors(t *testing.T) {
	const tail = `this implementation accepts {"key": "<server-name>"?, "variables": {"<variable-name>": "<string-value>"}?, "url": "<connection-url>"?}; "key" selects an artifact member (required when several bindable members remain), "variables" completes that member, "url" may replace only that selected member's target with the same scheme, and when the artifact declares no server "url" alone supplies the whole connection URL`
	doc := &document{
		Servers: map[string]server{
			"prod": {Host: "api.example.com", Protocol: "https"},
		},
	}
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"bare string (retired member-name form)", "prod",
			"configuration.server must be an object: " + tail},
		{"bare string (retired URL form)", "wss://api.example.com/v2",
			"configuration.server must be an object: " + tail},
		{"array", []any{"prod"},
			"configuration.server must be an object: " + tail},
		{"retired name member", map[string]any{"name": "prod"},
			`configuration.server member "name" is not pinned: ` + tail},
		{"two unpinned members", map[string]any{"mode": "fast", "name": "prod"},
			`configuration.server members "mode", "name" are not pinned: ` + tail},
		{"empty object", map[string]any{},
			`configuration.server carries none of "key", "variables", or "url": ` + tail},
		{"variables without key", map[string]any{"variables": map[string]any{"env": "staging"}},
			`configuration.server.variables["env"] names no declared variable of server "prod"`},
		{"variables with url", map[string]any{"url": "https://api.example.com/v2", "variables": map[string]any{"env": "staging"}},
			`configuration.server.variables["env"] names no declared variable of server "prod"`},
		{"key not a string", map[string]any{"key": 3},
			`configuration.server.key must be a non-empty string: ` + tail},
		{"key empty", map[string]any{"key": ""},
			`configuration.server.key must be a non-empty string: ` + tail},
		{"url not a string", map[string]any{"url": 3},
			`configuration.server.url must be a non-empty string: ` + tail},
		{"url empty", map[string]any{"url": ""},
			`configuration.server.url must be a non-empty string: ` + tail},
		{"variables not an object", map[string]any{"key": "prod", "variables": "staging"},
			`configuration.server.variables must be an object of string values: ` + tail},
		{"variables null", map[string]any{"key": "prod", "variables": nil},
			`configuration.server.variables must be an object of string values: ` + tail},
		{"variables entry not a string", map[string]any{"key": "prod", "variables": map[string]any{"env": 3}},
			`configuration.server.variables["env"] must be a string value: ` + tail},
		{"key names no member", map[string]any{"key": "nope"},
			`configuration.server.key "nope" names no member of the effective server set`},
		{"variables name not declared", map[string]any{"key": "prod", "variables": map[string]any{"env": "staging"}},
			`configuration.server.variables["env"] names no declared variable of server "prod"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveTarget(doc, nil, map[string]any{"configuration": map[string]any{"server": tc.value}})
			if err == nil {
				t.Fatalf("expected a loud refusal for the non-pinned form %#v", tc.value)
			}
			if err.Error() != tc.want {
				t.Errorf("error = %q\n     want %q", err.Error(), tc.want)
			}
		})
	}
}

// prodServer returns the doc's sole "prod" server, whose security applies
// when that artifact member is selected (§9.5).
func prodServer(doc *document) *server {
	s := doc.Servers["prod"]
	return &s
}

// secureDoc builds a doc whose server declares the named security schemes.
// Each name becomes its own $ref list entry — the AsyncAPI 3.0 shape, where
// `security` is a flat list of Security Scheme Objects or Reference
// Objects, never OpenAPI/2.x-style requirement maps.
func secureDoc(schemes map[string]securityScheme, names ...string) *document {
	reqs := make([]securityRequirement, 0, len(names))
	for _, n := range names {
		reqs = append(reqs, securityRequirement{Ref: "#/components/securitySchemes/" + n})
	}
	return &document{
		Servers: map[string]server{
			"prod": {Host: "api.example.com", Protocol: "https", Security: reqs},
		},
		Operations: map[string]asyncOperation{
			"op": {Action: "send", Channel: channelRef{Ref: "#/channels/ch"}},
		},
		Components: &components{SecuritySchemes: schemes},
	}
}

func TestRequiredContext_BearerRequirement(t *testing.T) {
	doc := secureDoc(map[string]securityScheme{
		"bearer": {Type: "http", Scheme: "bearer", Description: "API token"},
	}, "bearer")
	op := doc.Operations["op"]

	details := requiredContext(doc, &op, prodServer(doc), "https://api.example.com", nil)
	if details == nil {
		t.Fatal("expected CONTEXT_REQUIRED details, got nil")
	}
	if details.Target != "https://api.example.com" {
		t.Errorf("Target = %q, want https://api.example.com", details.Target)
	}
	if len(details.Alternatives) != 1 || len(details.Alternatives[0].Requirements) != 1 {
		t.Fatalf("alternatives = %+v", details.Alternatives)
	}
	req := details.Alternatives[0].Requirements[0]
	if req.Type != "auth.bearer" {
		t.Errorf("Type = %q, want auth.bearer", req.Type)
	}
	if req.Description != "API token" {
		t.Errorf("Description = %q, want %q", req.Description, "API token")
	}

	// Context carrying the bearer token satisfies the requirement.
	if got := requiredContext(doc, &op, prodServer(doc), "https://api.example.com", map[string]any{"bearerToken": "t"}); got != nil {
		t.Errorf("expected nil when context satisfies, got %+v", got)
	}
}

func TestRequiredContext_AlternativesAnyOneSuffices(t *testing.T) {
	doc := secureDoc(map[string]securityScheme{
		"key":   {Type: "apiKey", In: "header", Name: "X-Key"},
		"basic": {Type: "userPassword"},
	}, "key", "basic")
	op := doc.Operations["op"]

	details := requiredContext(doc, &op, prodServer(doc), "https://api.example.com", nil)
	if details == nil || len(details.Alternatives) != 2 {
		t.Fatalf("expected 2 alternatives, got %+v", details)
	}
	types := map[string]bool{}
	for _, alt := range details.Alternatives {
		types[alt.Requirements[0].Type] = true
	}
	if !types["auth.apiKey"] || !types["auth.basic"] {
		t.Errorf("alternative types = %v, want auth.apiKey and auth.basic", types)
	}

	// Satisfying any one alternative suffices.
	if got := requiredContext(doc, &op, prodServer(doc), "https://api.example.com", map[string]any{"apiKey": "k-123"}); got != nil {
		t.Errorf("apiKey alone should satisfy, got %+v", got)
	}
}

// TestRequiredContext_ServerAndOperationAreConjunctive verifies the
// ASYNC-P-07 derivation: the targeted server's security applies AND the
// operation's security, when declared, applies in addition — the operation
// never displaces the server. Within each list one entry suffices, so the
// challenge is the cross product: one server entry paired with one
// operation entry per alternative.
func TestRequiredContext_ServerAndOperationAreConjunctive(t *testing.T) {
	doc := secureDoc(map[string]securityScheme{
		"bearer": {Type: "http", Scheme: "bearer"},
		"key":    {Type: "httpApiKey", In: "query", Name: "k"},
	}, "bearer")
	op := doc.Operations["op"]
	op.Security = []securityRequirement{{Ref: "#/components/securitySchemes/key"}}

	details := requiredContext(doc, &op, prodServer(doc), "https://api.example.com", nil)
	if details == nil || len(details.Alternatives) != 1 {
		t.Fatalf("expected 1 alternative (1 server entry x 1 op entry), got %+v", details)
	}
	reqs := details.Alternatives[0].Requirements
	if len(reqs) != 2 || reqs[0].Type != "auth.bearer" || reqs[1].Type != "auth.apiKey" {
		t.Fatalf("expected conjunctive [auth.bearer auth.apiKey], got %+v", reqs)
	}

	// Either credential alone must NOT satisfy the conjunction.
	if got := requiredContext(doc, &op, prodServer(doc), "https://api.example.com", map[string]any{"bearerToken": "t"}); got == nil {
		t.Error("bearer alone must not satisfy server AND operation security")
	}
	if got := requiredContext(doc, &op, prodServer(doc), "https://api.example.com", map[string]any{"apiKeys": map[string]any{"key": "k"}}); got == nil {
		t.Error("apiKey alone must not satisfy server AND operation security")
	}
	// Both together satisfy.
	both := map[string]any{"bearerToken": "t", "apiKeys": map[string]any{"key": "k"}}
	if got := requiredContext(doc, &op, prodServer(doc), "https://api.example.com", both); got != nil {
		t.Errorf("bearer+apiKey should satisfy the conjunction, got %+v", got)
	}
}

// TestRequiredContext_SameSchemeBothLevelsIsOneRequirement verifies a scheme
// declared on both the server and the operation is one requirement in the
// paired alternative, never a duplicated conjunct.
func TestRequiredContext_SameSchemeBothLevelsIsOneRequirement(t *testing.T) {
	doc := secureDoc(map[string]securityScheme{
		"bearer": {Type: "http", Scheme: "bearer"},
	}, "bearer")
	op := doc.Operations["op"]
	op.Security = []securityRequirement{{Ref: "#/components/securitySchemes/bearer"}}

	details := requiredContext(doc, &op, prodServer(doc), "https://api.example.com", nil)
	if details == nil || len(details.Alternatives) != 1 {
		t.Fatalf("expected 1 alternative, got %+v", details)
	}
	if reqs := details.Alternatives[0].Requirements; len(reqs) != 1 || reqs[0].Type != "auth.bearer" {
		t.Fatalf("expected the shared scheme deduplicated to one requirement, got %+v", reqs)
	}
	if got := requiredContext(doc, &op, prodServer(doc), "https://api.example.com", map[string]any{"bearerToken": "t"}); got != nil {
		t.Errorf("the one bearer credential should satisfy, got %+v", got)
	}
}

// TestRequiredContext_MultipleEntriesAreIndependentAlternatives verifies the
// AsyncAPI 3.0 shape: `security` is a flat list, and each entry is its own
// standalone alternative (pure OR). There is no OpenAPI/2.x-style
// requirement-object that groups several scheme names into one AND'd
// alternative — a list of two entries always means "satisfy either", never
// "satisfy both".
func TestRequiredContext_MultipleEntriesAreIndependentAlternatives(t *testing.T) {
	doc := &document{
		Servers: map[string]server{
			"prod": {Host: "api.example.com", Protocol: "https", Security: []securityRequirement{
				{Ref: "#/components/securitySchemes/bearer"},
				{Ref: "#/components/securitySchemes/key"},
			}},
		},
		Operations: map[string]asyncOperation{"op": {Action: "send"}},
		Components: &components{SecuritySchemes: map[string]securityScheme{
			"bearer": {Type: "http", Scheme: "bearer"},
			"key":    {Type: "apiKey", In: "header", Name: "X-Key"},
		}},
	}
	op := doc.Operations["op"]

	details := requiredContext(doc, &op, prodServer(doc), "https://api.example.com", nil)
	if details == nil || len(details.Alternatives) != 2 {
		t.Fatalf("expected 2 independent alternatives, got %+v", details)
	}
	for _, alt := range details.Alternatives {
		if len(alt.Requirements) != 1 {
			t.Errorf("each AsyncAPI 3.0 security entry is a single-scheme alternative, got %+v", alt)
		}
	}

	// Either credential alone suffices (pure OR, never a conjunction).
	if got := requiredContext(doc, &op, prodServer(doc), "https://api.example.com", map[string]any{"bearerToken": "t"}); got != nil {
		t.Errorf("bearer alone should satisfy one of the alternatives, got %+v", got)
	}
	if got := requiredContext(doc, &op, prodServer(doc), "https://api.example.com", map[string]any{"apiKey": "k"}); got != nil {
		t.Errorf("apiKey alone should satisfy one of the alternatives, got %+v", got)
	}
}

// TestRequiredContext_InlineSchemeObject verifies that a `security` list
// entry MAY be an inline Security Scheme Object (not just a $ref to
// components.securitySchemes) — both forms are valid per the AsyncAPI 3.0
// spec, and an inline entry needs no components.securitySchemes section at
// all to be enforced.
func TestRequiredContext_InlineSchemeObject(t *testing.T) {
	doc := &document{
		Servers: map[string]server{
			"prod": {Host: "api.example.com", Protocol: "https", Security: []securityRequirement{
				{securityScheme: securityScheme{Type: "http", Scheme: "bearer", Description: "inline"}},
			}},
		},
		Operations: map[string]asyncOperation{"op": {Action: "send"}},
		// Deliberately no Components: the scheme is inline, not a $ref.
	}
	op := doc.Operations["op"]

	details := requiredContext(doc, &op, prodServer(doc), "https://api.example.com", nil)
	if details == nil || len(details.Alternatives) != 1 {
		t.Fatalf("expected 1 alternative from the inline scheme, got %+v", details)
	}
	req := details.Alternatives[0].Requirements[0]
	if req.Type != "auth.bearer" || req.Description != "inline" {
		t.Errorf("requirement = %+v, want auth.bearer/inline", req)
	}
}

// TestRequiredContext_UnresolvableRefSkipsEntry verifies that a $ref which
// cannot be resolved (dangling, or components.securitySchemes absent) is
// dropped rather than degraded into a weaker requirement — mirroring the
// "inexpressible scheme" rule, now expressed per-entry since AsyncAPI 3.0
// entries are standalone.
func TestRequiredContext_UnresolvableRefSkipsEntry(t *testing.T) {
	doc := &document{
		Servers: map[string]server{
			"prod": {Host: "api.example.com", Protocol: "https", Security: []securityRequirement{
				{Ref: "#/components/securitySchemes/bearer"},
				{Ref: "#/components/securitySchemes/missing"},
			}},
		},
		Operations: map[string]asyncOperation{"op": {Action: "send"}},
		Components: &components{SecuritySchemes: map[string]securityScheme{
			"bearer": {Type: "http", Scheme: "bearer"},
		}},
	}
	op := doc.Operations["op"]
	details := requiredContext(doc, &op, prodServer(doc), "https://api.example.com", nil)
	if details == nil || len(details.Alternatives) != 1 {
		t.Fatalf("expected exactly 1 alternative (the resolvable bearer entry), got %+v", details)
	}
	if details.Alternatives[0].Requirements[0].Type != "auth.bearer" {
		t.Errorf("expected the resolvable entry to survive, got %+v", details.Alternatives[0])
	}
}

// TestRequiredContext_DerivesFromConnectionServer verifies the requirements
// come from the explicitly selected artifact member (resolveTarget's
// SecurityServer), never from another server that happens to declare
// security.
func TestRequiredContext_DerivesFromConnectionServer(t *testing.T) {
	doc := &document{
		Servers: map[string]server{
			// "a" is explicitly selected below: no security.
			"a": {Host: "open.example.com", Protocol: "https"},
			// "b" declares security but is NOT the server dialed.
			"b": {Host: "secure.example.com", Protocol: "https", Security: []securityRequirement{{Ref: "#/components/securitySchemes/bearer"}}},
		},
		Operations: map[string]asyncOperation{"op": {Action: "send"}},
		Components: &components{SecuritySchemes: map[string]securityScheme{
			"bearer": {Type: "http", Scheme: "bearer"},
		}},
	}
	op := doc.Operations["op"]
	target, err := resolveTarget(doc, nil, map[string]any{"configuration": map[string]any{
		"server": map[string]any{"key": "a"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if target.ServerURL != "https://open.example.com" {
		t.Fatalf("selected server = %q, want a", target.ServerURL)
	}
	if got := requiredContext(doc, &op, target.SecurityServer, target.ServerURL, nil); got != nil {
		t.Errorf("requirements must derive from the connection's server (no security), got %+v", got)
	}
}

func TestRequiredContext_NoDeclaredSecurity(t *testing.T) {
	doc := &document{
		Servers:    map[string]server{"prod": {Host: "api.example.com", Protocol: "https"}},
		Operations: map[string]asyncOperation{"op": {Action: "send"}},
	}
	op := doc.Operations["op"]
	if got := requiredContext(doc, &op, prodServer(doc), "https://api.example.com", nil); got != nil {
		t.Errorf("expected nil without declared security, got %+v", got)
	}
}

// TestRequiredContext_UnknownSchemeSurfaced verifies the R2.c ruling
// (flipped from TestRequiredContext_UnknownSchemeNotEnforced, which pinned
// the old drop behavior): an unmapped scheme family (e.g. "futureSasl") is
// now SURFACED as "auth."+type with its components.securitySchemes key as
// Name (R2.a), instead of silently dropped — a document whose every
// alternative is unmapped now produces a challenge rather than dispatching
// unauthenticated.
func TestRequiredContext_UnknownSchemeSurfaced(t *testing.T) {
	doc := secureDoc(map[string]securityScheme{
		"custom": {Type: "futureSasl"},
	}, "custom")
	op := doc.Operations["op"]
	got := requiredContext(doc, &op, prodServer(doc), "https://api.example.com", nil)
	if got == nil {
		t.Fatal("expected a challenge surfacing the unmapped scheme, got nil")
	}
	if len(got.Alternatives) != 1 || len(got.Alternatives[0].Requirements) != 1 {
		t.Fatalf("unexpected challenge shape: %+v", got)
	}
	req := got.Alternatives[0].Requirements[0]
	if req.Type != "auth.futureSasl" {
		t.Errorf("Type = %q, want auth.futureSasl", req.Type)
	}
	if req.Name != "custom" {
		t.Errorf("Name = %q, want the securitySchemes key %q", req.Name, "custom")
	}
	// Unresolvable by the built-in satisfaction check (rule 10: no resolver
	// at this layer invented for an unmapped family).
	if invoke.ContextSatisfies(map[string]any{"bearerToken": "t"}, got) {
		t.Error("an unmapped requirement must never be satisfiable by the built-in check")
	}
}

// TestRequiredContext_UnmappedHTTPSchemeSurfaced verifies the R2.c ruling's
// http-scheme naming: an unmapped "http" scheme (e.g. digest) surfaces as
// "auth.http.<scheme>", mirroring openapi's naming exactly.
func TestRequiredContext_UnmappedHTTPSchemeSurfaced(t *testing.T) {
	doc := secureDoc(map[string]securityScheme{
		"digestAuth": {Type: "http", Scheme: "digest"},
	}, "digestAuth")
	op := doc.Operations["op"]
	got := requiredContext(doc, &op, prodServer(doc), "https://api.example.com", nil)
	if got == nil || len(got.Alternatives) != 1 {
		t.Fatalf("expected a surfaced challenge, got %+v", got)
	}
	req := got.Alternatives[0].Requirements[0]
	if req.Type != "auth.http.digest" {
		t.Errorf("Type = %q, want auth.http.digest", req.Type)
	}
	if req.Name != "digestAuth" {
		t.Errorf("Name = %q, want %q", req.Name, "digestAuth")
	}
}

// TestRequiredContext_NameFromRefVsInline verifies the R2.a ruling's naming
// rule for AsyncAPI: a $ref entry carries the components.securitySchemes key
// it resolved through as Name; an inline scheme object has no addressable
// name and carries an empty Name.
func TestRequiredContext_NameFromRefVsInline(t *testing.T) {
	doc := secureDoc(map[string]securityScheme{
		"bearer": {Type: "http", Scheme: "bearer"},
	}, "bearer")
	op := doc.Operations["op"]
	got := requiredContext(doc, &op, prodServer(doc), "https://api.example.com", nil)
	if got == nil || len(got.Alternatives) != 1 {
		t.Fatalf("expected 1 alternative, got %+v", got)
	}
	if name := got.Alternatives[0].Requirements[0].Name; name != "bearer" {
		t.Errorf("$ref entry Name = %q, want %q", name, "bearer")
	}

	inlineDoc := &document{
		Servers: map[string]server{
			"prod": {Host: "api.example.com", Protocol: "https", Security: []securityRequirement{
				{securityScheme: securityScheme{Type: "http", Scheme: "bearer"}},
			}},
		},
		Operations: map[string]asyncOperation{"op": {Action: "send"}},
	}
	inlineOp := inlineDoc.Operations["op"]
	gotInline := requiredContext(inlineDoc, &inlineOp, prodServer(inlineDoc), "https://api.example.com", nil)
	if gotInline == nil || len(gotInline.Alternatives) != 1 {
		t.Fatalf("expected 1 alternative, got %+v", gotInline)
	}
	if name := gotInline.Alternatives[0].Requirements[0].Name; name != "" {
		t.Errorf("inline scheme Name = %q, want empty (no addressable name)", name)
	}
}

func TestRequirementType_Families(t *testing.T) {
	cases := []struct {
		scheme securityScheme
		want   string
	}{
		{securityScheme{Type: "http", Scheme: "bearer"}, "auth.bearer"},
		{securityScheme{Type: "http", Scheme: "Bearer"}, "auth.bearer"},
		{securityScheme{Type: "http", Scheme: "basic"}, "auth.basic"},
		{securityScheme{Type: "http", Scheme: "digest"}, ""},
		{securityScheme{Type: "httpBearer"}, "auth.bearer"},
		{securityScheme{Type: "userPassword"}, "auth.basic"},
		{securityScheme{Type: "apiKey"}, "auth.apiKey"},
		{securityScheme{Type: "httpApiKey"}, "auth.apiKey"},
		{securityScheme{Type: "oauth2"}, "auth.oauth2"},
		{securityScheme{Type: "scramSha256"}, "auth.basic"},
		{securityScheme{Type: "scramSha512"}, "auth.basic"},
	}
	for _, tc := range cases {
		if got := requirementType(tc.scheme); got != tc.want {
			t.Errorf("requirementType(%+v) = %q, want %q", tc.scheme, got, tc.want)
		}
	}
}

// TestUnmappedRequirementType verifies the R2.c naming rule mirrors openapi
// exactly: an unmapped "http" scheme surfaces as "auth.http.<scheme>";
// anything else surfaces as "auth."+type verbatim.
func TestUnmappedRequirementType(t *testing.T) {
	cases := []struct {
		scheme securityScheme
		want   string
	}{
		{securityScheme{Type: "http", Scheme: "digest"}, "auth.http.digest"},
		{securityScheme{Type: "http", Scheme: "negotiate"}, "auth.http.negotiate"},
		{securityScheme{Type: "futureSasl"}, "auth.futureSasl"},
		{securityScheme{Type: "X509"}, "auth.X509"},
	}
	for _, tc := range cases {
		if got := unmappedRequirementType(tc.scheme); got != tc.want {
			t.Errorf("unmappedRequirementType(%+v) = %q, want %q", tc.scheme, got, tc.want)
		}
	}
}

// TestOAuth2Requirement_GrantTypeSurfacesSelectedFlow mirrors openapi's
// oauth2 grantType test (R2.b ruling): grantType names exactly the flow the
// fixed priority selects (authorizationCode > implicit > password >
// clientCredentials), alongside authorizeUrl/tokenUrl/scopes.
func TestOAuth2Requirement_GrantTypeSurfacesSelectedFlow(t *testing.T) {
	cases := []struct {
		name  string
		flows *oauthFlows
		want  string
	}{
		{
			"authorizationCode",
			&oauthFlows{AuthorizationCode: &oauthFlow{AuthorizationURL: "https://a.example.com/authorize", TokenURL: "https://a.example.com/token"}},
			"authorization_code",
		},
		{
			"implicit",
			&oauthFlows{Implicit: &oauthFlow{AuthorizationURL: "https://a.example.com/authorize"}},
			"implicit",
		},
		{
			"password",
			&oauthFlows{Password: &oauthFlow{TokenURL: "https://a.example.com/token"}},
			"password",
		},
		{
			"clientCredentials",
			&oauthFlows{ClientCredentials: &oauthFlow{TokenURL: "https://a.example.com/token"}},
			"client_credentials",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scheme := securityScheme{Type: "oauth2", Flows: tc.flows}
			req := oauth2Requirement(scheme, "https://api.example.com")
			if req.Type != "auth.oauth2" {
				t.Fatalf("Type = %q, want auth.oauth2", req.Type)
			}
			if got := req.Extra["grantType"]; got != tc.want {
				t.Errorf("grantType = %v, want %q", got, tc.want)
			}
		})
	}
}

// TestOAuth2Requirement_CarriesFlowFields verifies authorizeUrl/tokenUrl
// (relative ones absolutized against the server URL) and scopes ride
// alongside grantType — parity with openapi's oauth2Requirement.
func TestOAuth2Requirement_CarriesFlowFields(t *testing.T) {
	scheme := securityScheme{
		Type: "oauth2",
		Flows: &oauthFlows{
			AuthorizationCode: &oauthFlow{
				AuthorizationURL: "/oauth/authorize", // relative -> absolutized
				TokenURL:         "https://auth.example.com/oauth/token",
				Scopes:           map[string]string{"read": "Read", "write": "Write"},
			},
		},
	}
	req := oauth2Requirement(scheme, "https://api.example.com")
	if got := req.Extra["authorizeUrl"]; got != "https://api.example.com/oauth/authorize" {
		t.Errorf("authorizeUrl = %v, want absolutized host URL", got)
	}
	if got := req.Extra["tokenUrl"]; got != "https://auth.example.com/oauth/token" {
		t.Errorf("tokenUrl = %v", got)
	}
	scopes, _ := req.Extra["scopes"].([]string)
	if len(scopes) != 2 || scopes[0] != "read" || scopes[1] != "write" {
		t.Errorf("scopes = %v, want [read write]", req.Extra["scopes"])
	}
}

// TestOAuth2Requirement_NoFlows verifies a bare oauth2 scheme (no `flows`)
// carries only the type — no grantType, no Extra.
func TestOAuth2Requirement_NoFlows(t *testing.T) {
	req := oauth2Requirement(securityScheme{Type: "oauth2"}, "https://api.example.com")
	if req.Type != "auth.oauth2" || req.Extra != nil {
		t.Errorf("expected bare auth.oauth2 with no Extra, got %+v", req)
	}
}

// AsyncAPI 3.0: a message that omits contentType takes the document's
// defaultContentType (the per-message EFFECTIVE content-type rule,
// ASYNC-P-05) — still the declared lane, never payload sniffing.
// Regression: the model didn't parse defaultContentType, so JSON events
// from documents relying on it decoded as strings and failed OBI-T-08.
func TestEffectiveContentType_FallsBackToDocumentDefault(t *testing.T) {
	doc := &document{
		AsyncAPI:           "3.0.0",
		DefaultContentType: "application/json",
		Operations: map[string]asyncOperation{
			"sub": {Action: "send", Messages: []messageRef{{Ref: "#/components/messages/plain"}}},
		},
		Components: &components{
			Messages: map[string]message{"plain": {Name: "plain"}}, // no contentType
		},
	}
	op := doc.Operations["sub"]
	if got := decodeContentType(doc, governingMessages(doc, &op, nil)); got != "application/json" {
		t.Errorf("decodeContentType = %q, want the document default", got)
	}
	// A per-message declaration still wins over the default.
	doc.Components.Messages["plain"] = message{Name: "plain", ContentType: "text/plain"}
	if got := decodeContentType(doc, governingMessages(doc, &op, nil)); got != "text/plain" {
		t.Errorf("decodeContentType = %q, want the per-message declaration", got)
	}
}

// Direction-correct decode (ASYNC-P-05): a publish invocation's output (the
// response) decodes by the REPLY-side declarations, never the operation's
// own request-side messages.
func TestDecodeContentType_UsesReplySideDeclarations(t *testing.T) {
	doc := &document{
		AsyncAPI: "3.0.0",
		Operations: map[string]asyncOperation{
			"pub": {
				Action:   "receive",
				Messages: []messageRef{{Ref: "#/components/messages/req"}},
				Reply:    &operationReply{Messages: []messageRef{{Ref: "#/components/messages/rep"}}},
			},
		},
		Components: &components{
			Messages: map[string]message{
				"req": {Name: "req", ContentType: "text/plain"},
				"rep": {Name: "rep", ContentType: "application/json"},
			},
		},
	}
	op := doc.Operations["pub"]
	if got := decodeContentType(doc, replyGoverningMessages(doc, &op)); got != "application/json" {
		t.Errorf("reply decode type = %q, want the reply-side declaration", got)
	}
	if got := decodeContentType(doc, governingMessages(doc, &op, nil)); got != "text/plain" {
		t.Errorf("request-side type = %q, want the request-side declaration", got)
	}
}

// TestDistinctEffectiveTypes_AmbiguityFallsToTextLane pins §9.3's conflict
// rule (ASYNC-P-05): a governing set resolving to MORE than one distinct
// effective content type is ambiguous, and decode falls to the text lane
// ("") rather than guessing — a mixed declared/undeclared set included.
func TestDistinctEffectiveTypes_AmbiguityFallsToTextLane(t *testing.T) {
	doc := &document{
		AsyncAPI: "3.0.0",
		Operations: map[string]asyncOperation{
			"sub": {Action: "send", Messages: []messageRef{
				{Ref: "#/components/messages/a"},
				{Ref: "#/components/messages/b"},
			}},
		},
		Components: &components{
			Messages: map[string]message{
				"a": {Name: "a", ContentType: "application/json"},
				"b": {Name: "b", ContentType: "text/plain"},
			},
		},
	}
	op := doc.Operations["sub"]
	if got := decodeContentType(doc, governingMessages(doc, &op, nil)); got != "" {
		t.Errorf("two distinct effective types must fall to the text lane, got %q", got)
	}

	// A charset parameter never makes a type distinct.
	doc.Components.Messages["b"] = message{Name: "b", ContentType: "application/json; charset=utf-8"}
	if got := decodeContentType(doc, governingMessages(doc, &op, nil)); got != "application/json" {
		t.Errorf("parameter-only variation is ONE distinct type, got %q", got)
	}

	// A declared message alongside an undeclared one (no contentType, no
	// document default) is a mixed set: ambiguous, text lane.
	doc.Components.Messages["b"] = message{Name: "b"}
	if got := decodeContentType(doc, governingMessages(doc, &op, nil)); got != "" {
		t.Errorf("mixed declared/undeclared set must fall to the text lane, got %q", got)
	}
}

// TestGoverningMessages_ChannelFallback verifies the AsyncAPI rule: an
// operation that declares no `messages` supports ALL the channel's
// messages — the channel's set governs its lanes.
func TestGoverningMessages_ChannelFallback(t *testing.T) {
	doc := &document{AsyncAPI: "3.0.0"}
	ch := &channel{Messages: map[string]message{
		"one": {Name: "one", ContentType: "application/json"},
		"two": {Name: "two", ContentType: "application/json"},
	}}
	op := &asyncOperation{Action: "send"}
	msgs := governingMessages(doc, op, ch)
	if len(msgs) != 2 {
		t.Fatalf("expected the channel's 2 messages, got %d", len(msgs))
	}
	if got := decodeContentType(doc, msgs); got != "application/json" {
		t.Errorf("decodeContentType = %q, want application/json", got)
	}
}

// TestResolveInputCodec pins §9.1 (ASYNC-P-03): exactly one selected message
// governs; JSON family → JSON; no declaration requires an explicit lane;
// every other declared type uses the raw-string lane. Arbitrary bytes remain
// unrepresentable because that lane accepts strings, not byte conventions.
func TestResolveInputCodec(t *testing.T) {
	mk := func(cts ...string) (*document, []message) {
		doc := &document{AsyncAPI: "3.0.0"}
		msgs := make([]message, 0, len(cts))
		for i, ct := range cts {
			msgs = append(msgs, message{Name: string(rune('a' + i)), ContentType: ct})
		}
		return doc, msgs
	}

	doc, msgs := mk("application/json")
	codec, err := resolveInputCodec(doc, msgs)
	if err != nil || !codec.JSON || codec.ContentType != "application/json" {
		t.Errorf("json family: codec = %+v, err = %v", codec, err)
	}

	doc, msgs = mk()
	if _, err = resolveInputCodec(doc, msgs); err == nil || !strings.Contains(err.Error(), "exactly one selected message") {
		t.Errorf("no selected message must refuse, got %v", err)
	}

	doc, msgs = mk("")
	if _, err = resolveInputCodec(doc, msgs); err == nil || !strings.Contains(err.Error(), "configuration.encode") {
		t.Errorf("an absent declaration must require configuration.encode, got %v", err)
	}
	jsonContext := map[string]any{"configuration": map[string]any{"encode": "json"}}
	codec, err = resolveInputCodec(doc, msgs, jsonContext)
	if err != nil || !codec.JSON || codec.ContentType != "application/json" {
		t.Errorf("configured JSON lane: codec = %+v, err = %v", codec, err)
	}
	textContext := map[string]any{"configuration": map[string]any{"encode": "text"}}
	codec, err = resolveInputCodec(doc, msgs, textContext)
	if err != nil || codec.JSON || codec.ContentType != "text/plain; charset=utf-8" {
		t.Errorf("configured text lane: codec = %+v, err = %v", codec, err)
	}

	doc, msgs = mk("text/plain")
	codec, err = resolveInputCodec(doc, msgs)
	if err != nil || codec.JSON || codec.ContentType != "text/plain" {
		t.Errorf("text family: codec = %+v, err = %v", codec, err)
	}
	if _, verr := encodeInput(codec, map[string]any{"not": "a string"}); verr == nil {
		t.Error("text lane must refuse a non-string value")
	}
	if b, verr := encodeInput(codec, "raw text"); verr != nil || string(b) != "raw text" {
		t.Errorf("text lane must send a string raw, got %q err %v", b, verr)
	}

	doc, msgs = mk("application/json", "text/plain")
	if _, err = resolveInputCodec(doc, msgs); err == nil || !strings.Contains(err.Error(), "exactly one selected message") {
		t.Errorf("an unselected message set must refuse, got %v", err)
	}

	doc, msgs = mk("avro/binary")
	if _, err = resolveInputCodec(doc, msgs); err == nil || !strings.Contains(err.Error(), "no candidate application-value carriage") {
		t.Errorf("binary/codec-specific media must be refused, got %v", err)
	}

	doc, msgs = mk("text/plain; charset=iso-8859-1")
	if _, err = resolveInputCodec(doc, msgs); err == nil || !strings.Contains(err.Error(), "non-UTF-8 charset") {
		t.Errorf("non-UTF-8 text carriage must be refused, got %v", err)
	}
}

// assertConfigValue narrows err to a config.value CONTEXT_REQUIRED challenge
// (R1a) and checks its point and relative path. Returns the requirement for further
// assertions (schema, durable).
func assertConfigValue(t *testing.T, err error, point, path string) invoke.ContextRequirement {
	t.Helper()
	var ie *invoke.InvocationError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *InvocationError, got %v", err)
	}
	details := invoke.ContextRequiredFrom(ie)
	if details == nil {
		t.Fatalf("expected a CONTEXT_REQUIRED challenge, got %v", err)
	}
	if len(details.Alternatives) != 1 || len(details.Alternatives[0].Requirements) != 1 {
		t.Fatalf("expected one alternative with one requirement, got %+v", details.Alternatives)
	}
	req := details.Alternatives[0].Requirements[0]
	if req.Type != "config.value" {
		t.Fatalf("requirement type = %q, want config.value", req.Type)
	}
	if got, _ := req.Extra["point"].(string); got != point {
		t.Errorf("config.value point = %q, want %q", got, point)
	}
	if got, _ := req.Extra["path"].(string); got != path {
		t.Errorf("config.value path = %q, want %q", got, path)
	}
	return req
}
