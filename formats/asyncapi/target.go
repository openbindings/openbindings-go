package asyncapi

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	openbindings "github.com/openbindings/openbindings-go"
)

// This file implements the server and address configuration points of
// openbindings.asyncapi@2 §9.2 (ASYNC-P-04): the effective server set and
// its deterministic ordering, server-variable substitution, channel-address
// parameter expansion, and the concatenation URL-assembly rule. Every
// unresolvable input is a pre-dispatch refusal, never a guess — this
// specification does not assume the channel key is an address, never dials
// literal braces, and refuses out-of-revision protocols.
//
// Consultation order per point is per-invocation configuration →
// consumer-level configuration → the default; both configuration tiers
// arrive merged in the binding context's `configuration` field (the same
// carriage the openapi format consults), so this file reads one merged
// value per point.

// boundProtocols are the protocols revision 1 of openbindings.asyncapi
// binds (§2). Everything else is a definition-level exclusion, refused
// pre-dispatch (ASYNC-P-02).
func isBoundProtocol(p string) bool {
	switch p {
	case "http", "https", "ws", "wss":
		return true
	}
	return false
}

// configRequired is the typed signal a resolution helper returns when a named
// configuration point cannot resolve because a value is absent (no default,
// no supplied value) — a resolvable-missing value, not a malformed one. The
// invoke path turns it into a config.value CONTEXT_REQUIRED challenge
// (retryable after resolution) rather than a terminal ERR_SOURCE_CONFIG_ERROR
// (R1a). It implements error so it rides the existing (…, error) returns and
// the exported ResolveEndpoint seam still surfaces it as a plain error to
// consumers (e.g. ob) that do not negotiate. hostHint is the strongest scope
// the artifact provides when no server URL has resolved yet. Configuration may
// be sensitive; a resolver decides whether that scope is sufficient before
// releasing a stored value.
type configRequired struct {
	point       string
	key         string
	description string
	choices     []string
	durable     *bool
	hostHint    string
}

func (c *configRequired) Error() string { return c.description }

// namedServer pairs a doc server with its `servers`-map key, so consumer
// configuration can select a member by key and so security derivation can
// name the server it describes.
type namedServer struct {
	Name   string
	Server server
}

// resolvedTarget is the outcome of the server configuration point: the
// assembled connection base (scheme://host[/pathname], variables
// substituted, no trailing slash), the deciding protocol, and the server
// whose declared security applies (§9.5: the selected artifact server,
// including when its target URL is replaced).
type resolvedTarget struct {
	ServerURL      string
	Protocol       string
	SecurityServer *server
}

// effectiveServers returns the operation's effective server set (§9.2): the
// channel's declared `servers` subset when present and non-empty, in the
// artifact's own array order; else the document's `servers` map in
// lexicographic key order (the map is unordered — this ordering is the
// specification's determinism rule). An empty channel `servers` array means
// ALL servers, the AsyncAPI rule. A channel `servers` $ref that does not
// resolve contributes nothing.
func effectiveServers(doc *document, ch *channel) []namedServer {
	if ch != nil && len(ch.Servers) > 0 {
		out := make([]namedServer, 0, len(ch.Servers))
		for _, ref := range ch.Servers {
			name := extractRefName(ref.Ref)
			if srv, ok := doc.Servers[name]; ok {
				out = append(out, namedServer{Name: name, Server: srv})
			}
		}
		return out
	}
	names := make([]string, 0, len(doc.Servers))
	for name := range doc.Servers {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]namedServer, 0, len(names))
	for _, name := range names {
		out = append(out, namedServer{Name: name, Server: doc.Servers[name]})
	}
	return out
}

// defaultServer returns the sole effective-set member whose protocol
// revision 1 binds (§9.2). With zero or several bound members there is no
// artifact-selected default; declaration order never chooses identity.
func defaultServer(candidates []namedServer) *namedServer {
	var selected *namedServer
	for i := range candidates {
		if isBoundProtocol(strings.ToLower(candidates[i].Server.Protocol)) {
			if selected != nil {
				return nil
			}
			selected = &candidates[i]
		}
	}
	return selected
}

func boundServerNames(candidates []namedServer) []string {
	var names []string
	for _, candidate := range candidates {
		if isBoundProtocol(strings.ToLower(candidate.Server.Protocol)) {
			names = append(names, candidate.Name)
		}
	}
	return names
}

// resolveTarget resolves the server configuration point for the operation's
// channel (ASYNC-P-04): the sole bound member selects itself; several
// require consumer selection. A complete URL may replace the selected
// member's target only with the same scheme, while that member's artifact
// declarations and security continue to govern. Declaration order never
// selects a member.
//
// The `configuration.server` value SHAPE is this SDK's own carriage, not
// specification content: §9.2 pins what may be supplied and the refusals, but
// the concrete value a consumer hands to the point is implementation surface
// (R1). This SDK carries it as one composable object and validates that shape
// as hygiene (a mistyped member is worth a loud error, not silent tolerance)
// — another implementation may carry it however it likes:
//
//	{"key": "<server-name>"?, "variables": {"<variable-name>": "<string-value>"}?, "url": "<connection-url>"?}
//
// `key` is required when several bound members remain and optional when one
// remains. `variables` and `url` complete or replace the selected member;
// an object with none of the three is refused. Server variables substitute
// supplied-else-default-else-refusal when no URL replacement is present.
//
// The legacy context.metadata.baseURL override is honored below the
// configuration point (the configuration point is the contract surface).
func resolveTarget(doc *document, ch *channel, bindCtx map[string]any) (resolvedTarget, error) {
	candidates := effectiveServers(doc, ch)
	def := defaultServer(candidates)

	if cfg := openbindings.ContextConfiguration(bindCtx); cfg != nil {
		if raw, ok := cfg["server"]; ok && raw != nil {
			return resolveServerConfig(raw, candidates, def)
		}
	}

	if meta := openbindings.ContextMetadata(bindCtx); meta != nil {
		if base, ok := meta["baseURL"].(string); ok && base != "" {
			if def == nil {
				names := boundServerNames(candidates)
				if len(names) == 0 {
					return resolvedTarget{}, fmt.Errorf("the effective server set declares no server with a protocol bound by the supported openbindings.asyncapi revisions")
				}
				return resolvedTarget{}, &configRequired{
					point: "server", key: "key",
					description: "configuration.server.key must select one artifact server before metadata.baseURL can replace its target",
					choices:     names, durable: boolPointer(true),
				}
			}
			return fullURLOverride(base, def)
		}
	}

	if def == nil {
		names := boundServerNames(candidates)
		if len(names) == 0 {
			return resolvedTarget{}, fmt.Errorf("the effective server set declares no server with a protocol bound by the supported openbindings.asyncapi revisions")
		}
		return resolvedTarget{}, &configRequired{
			point:       "server",
			key:         "key",
			description: "the effective server set declares several bindable servers; configuration.server.key must select one artifact member",
			choices:     names,
			durable:     boolPointer(true),
		}
	}
	return assembleServer(def, nil)
}

// serverConfigShapes is the teaching tail of every unrecognized-form refusal.
// The shape is this SDK's own carriage (§9.2 pins the semantics, not the
// value shape — R1); this SDK and the TS SDK keep the same shape by choice for
// a consistent developer experience, not because the specification requires
// it. Rejecting an unrecognized spelling is hygiene: a mistyped member should
// fail loudly, not be silently tolerated.
const serverConfigShapes = `this implementation accepts {"key": "<server-name>"?, "variables": {"<variable-name>": "<string-value>"}?, "url": "<connection-url>"?}; "key" selects an artifact member (required when several bindable members remain), "variables" completes that member, and "url" may replace only that selected member's target with the same scheme`

// resolveServerConfig applies one configured `server` value against the
// effective set using this SDK's composable object carriage. Every
// unrecognized or ambiguous form is a loud refusal carrying
// serverConfigShapes.
func resolveServerConfig(raw any, candidates []namedServer, def *namedServer) (resolvedTarget, error) {
	v, ok := raw.(map[string]any)
	if !ok {
		return resolvedTarget{}, fmt.Errorf("configuration.server must be an object: %s", serverConfigShapes)
	}

	var unpinned []string
	for member := range v {
		if member != "key" && member != "url" && member != "variables" {
			unpinned = append(unpinned, member)
		}
	}
	if len(unpinned) > 0 {
		sort.Strings(unpinned)
		quoted := make([]string, len(unpinned))
		for i, m := range unpinned {
			quoted[i] = fmt.Sprintf("%q", m)
		}
		noun, verb := "member", "is"
		if len(unpinned) > 1 {
			noun, verb = "members", "are"
		}
		return resolvedTarget{}, fmt.Errorf("configuration.server %s %s %s not pinned: %s", noun, strings.Join(quoted, ", "), verb, serverConfigShapes)
	}

	rawKey, hasKey := v["key"]
	rawURL, hasURL := v["url"]
	_, hasVars := v["variables"]
	if !hasKey && !hasURL && !hasVars {
		return resolvedTarget{}, fmt.Errorf(`configuration.server carries none of "key", "variables", or "url": %s`, serverConfigShapes)
	}
	supplied, err := suppliedServerVariables(v)
	if err != nil {
		return resolvedTarget{}, err
	}
	full := ""
	if hasURL {
		var ok bool
		full, ok = rawURL.(string)
		if !ok || full == "" {
			return resolvedTarget{}, fmt.Errorf("configuration.server.url must be a non-empty string: %s", serverConfigShapes)
		}
	}

	member := def
	if hasKey {
		key, ok := rawKey.(string)
		if !ok || key == "" {
			return resolvedTarget{}, fmt.Errorf("configuration.server.key must be a non-empty string: %s", serverConfigShapes)
		}
		member = serverByKey(candidates, key)
		if member == nil {
			return resolvedTarget{}, fmt.Errorf("configuration.server.key %q names no member of the effective server set", key)
		}
	}
	if member == nil {
		names := boundServerNames(candidates)
		if len(names) == 0 {
			return resolvedTarget{}, fmt.Errorf("the effective server set declares no server with a protocol bound by the supported openbindings.asyncapi revisions")
		}
		return resolvedTarget{}, &configRequired{
			point: "server", key: "key",
			description: "configuration.server.key must select one artifact server before variables or a URL replacement can be applied",
			choices:     names, durable: boolPointer(true),
		}
	}
	if err := validateSuppliedServerVariables(member, supplied); err != nil {
		return resolvedTarget{}, err
	}
	if hasURL {
		return fullURLOverride(full, member)
	}
	return assembleServer(member, supplied)
}

func boolPointer(value bool) *bool { return &value }

// suppliedServerVariables decodes the key form's optional `variables`
// member: an object of string values (§9.2 — upstream's Server Variable
// value space), any other shape a loud refusal carrying
// serverConfigShapes. Which NAMES are admissible is the selected
// server's business, checked in assembleServer.
func suppliedServerVariables(v map[string]any) (map[string]string, error) {
	rawVars, hasVars := v["variables"]
	if !hasVars {
		return nil, nil
	}
	m, ok := rawVars.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("configuration.server.variables must be an object of string values: %s", serverConfigShapes)
	}
	supplied := make(map[string]string, len(m))
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		s, ok := m[name].(string)
		if !ok {
			return nil, fmt.Errorf("configuration.server.variables[%q] must be a string value: %s", name, serverConfigShapes)
		}
		supplied[name] = s
	}
	return supplied, nil
}

// serverByKey selects the effective-set member with the given servers-map
// key, or nil.
func serverByKey(candidates []namedServer, key string) *namedServer {
	for i := range candidates {
		if candidates[i].Name == key {
			return &candidates[i]
		}
	}
	return nil
}

// fullURLOverride resolves a consumer-supplied complete connection URL as a
// replacement for an already-selected artifact server. Its scheme must equal
// that server's declared protocol, and the selected server's declarations,
// including security, continue to apply (§§9.2, 9.5).
func fullURLOverride(full string, selected *namedServer) (resolvedTarget, error) {
	if selected == nil {
		return resolvedTarget{}, fmt.Errorf("a connection URL can replace only a selected artifact server")
	}
	u, err := url.Parse(full)
	if err != nil || !u.IsAbs() {
		return resolvedTarget{}, fmt.Errorf("connection URL %q is not an absolute URL", full)
	}
	scheme := strings.ToLower(u.Scheme)
	if !isBoundProtocol(scheme) {
		return resolvedTarget{}, fmt.Errorf("connection URL %q: scheme %q is not bound by the supported openbindings.asyncapi revisions (supported: http, https, ws, wss)", full, scheme)
	}
	selectedProtocol := strings.ToLower(selected.Server.Protocol)
	if scheme != selectedProtocol {
		return resolvedTarget{}, fmt.Errorf(
			"connection URL %q uses scheme %q, but selected server %q declares protocol %q",
			full, scheme, selected.Name, selected.Server.Protocol,
		)
	}
	return resolvedTarget{ServerURL: strings.TrimRight(full, "/"), Protocol: scheme, SecurityServer: &selected.Server}, nil
}

func validateSuppliedServerVariables(member *namedServer, supplied map[string]string) error {
	names := make([]string, 0, len(supplied))
	for name := range supplied {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		declaration, declared := member.Server.Variables[name]
		if !declared {
			return fmt.Errorf("configuration.server.variables[%q] names no declared variable of server %q", name, member.Name)
		}
		if len(declaration.Enum) > 0 && !containsString(declaration.Enum, supplied[name]) {
			return fmt.Errorf(
				"configuration.server.variables[%q] value %q is outside server %q's artifact-declared enum",
				name, supplied[name], member.Name,
			)
		}
	}
	return nil
}

// assembleServer performs the target URL assembly (§9.2): scheme from the
// selected server's protocol; authority from its `host` with every variable
// substituted; path from its `pathname` (variables substituted the same
// way). A member the consumer selects by key must still speak a bound
// protocol — out-of-revision protocols are refused pre-dispatch, never
// dialed. Supplied server-variable values apply only to the server's own
// declared variable names: an undeclared supplied name is refused, never
// ignored (no-guess), and a supplied value outside the variable's declared
// enum is refused (upstream SHOULD, hardened to a refusal — the
// specification's own pin).
func assembleServer(member *namedServer, supplied map[string]string) (resolvedTarget, error) {
	srv := member.Server
	proto := strings.ToLower(srv.Protocol)
	if !isBoundProtocol(proto) {
		return resolvedTarget{}, fmt.Errorf("server %q: protocol %q is not bound by the supported openbindings.asyncapi revisions (supported: http, https, ws, wss)", member.Name, srv.Protocol)
	}

	if err := validateSuppliedServerVariables(member, supplied); err != nil {
		return resolvedTarget{}, err
	}

	host, err := substituteServerVariables(member, srv.Host, supplied)
	if err != nil {
		return resolvedTarget{}, err
	}
	pathname, err := substituteServerVariables(member, srv.PathName, supplied)
	if err != nil {
		return resolvedTarget{}, err
	}

	base := proto + "://" + host
	if pathname != "" {
		base = joinURL(base, pathname)
	}
	return resolvedTarget{
		ServerURL:      strings.TrimRight(base, "/"),
		Protocol:       proto,
		SecurityServer: &member.Server,
	}, nil
}

// substituteServerVariables expands every `{name}` expression in a server
// host or pathname template from the consumer-supplied value (the key
// form's `variables` member), else the variable's declared default
// (ASYNC-P-04). AsyncAPI declares a Server Variable's default OPTIONAL, so
// an undefaulted variable is satisfiable only by supply; unsupplied and
// undefaulted is a pre-dispatch refusal, and literal braces never reach the
// wire. Supplied values and declared defaults both honor the artifact's enum;
// a full-URL override is a distinct target replacement and does not claim to
// satisfy a variable declaration.
func substituteServerVariables(member *namedServer, template string, supplied map[string]string) (string, error) {
	out := template
	for _, name := range templateExpressions(template) {
		val, ok := supplied[name]
		if !ok {
			if v, declared := member.Server.Variables[name]; declared && v.Default != "" {
				val = v.Default
			} else {
				var choices []string
				if v, declared := member.Server.Variables[name]; declared {
					choices = v.Enum
				}
				return "", &configRequired{
					point:       "server",
					key:         name,
					description: fmt.Sprintf("server %q: variable %q has no supplied value and no declared default (supply one at the server configuration point's \"variables\" member)", member.Name, name),
					choices:     choices,
					hostHint:    member.Server.Host,
				}
			}
		}
		if declaration, declared := member.Server.Variables[name]; declared &&
			len(declaration.Enum) > 0 && !containsString(declaration.Enum, val) {
			return "", fmt.Errorf(
				"server %q: variable %q value %q is outside the artifact-declared enum",
				member.Name, name, val,
			)
		}
		out = strings.ReplaceAll(out, "{"+name+"}", val)
	}
	if strings.ContainsAny(out, "{}") {
		return "", fmt.Errorf("server %q: %q still carries an unexpanded expression after substitution", member.Name, out)
	}
	return out, nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Address configuration point
// ---------------------------------------------------------------------------

// addressConfig is the consumer's `configuration.address` value, decoded:
//
//	"/rooms/general"                             // the concrete address outright
//	{"address": "/rooms/general"}                // same, spelled as an object
//	{"parameters": {"roomId": "general"}}        // `{name}` parameter values
//
// A concrete address and parameters may co-occur only in the object form; a
// consumer-supplied concrete address is used verbatim (and must itself be
// concrete — braces are refused, never dialed).
type addressConfig struct {
	Address    string
	Parameters map[string]string
}

// addressConfiguration reads the address configuration point from the
// binding context. Returns a zero value when the point is not configured; a
// malformed value is a loud error, never ignored.
func addressConfiguration(bindCtx map[string]any) (addressConfig, error) {
	var out addressConfig
	cfg := openbindings.ContextConfiguration(bindCtx)
	if cfg == nil {
		return out, nil
	}
	if rawParams, ok := cfg["parameters"].(map[string]any); ok {
		out.Parameters = make(map[string]string, len(rawParams))
		for name, value := range rawParams {
			text, ok := value.(string)
			if !ok {
				return out, fmt.Errorf("configuration.parameters[%q] must be a string", name)
			}
			out.Parameters[name] = text
		}
	}
	raw, ok := cfg["address"]
	if !ok || raw == nil {
		return out, nil
	}
	switch v := raw.(type) {
	case string:
		out.Address = v
		return out, nil
	case map[string]any:
		if addr, ok := v["address"].(string); ok {
			out.Address = addr
		}
		if rawParams, ok := v["parameters"].(map[string]any); ok {
			if len(out.Parameters) > 0 {
				return out, fmt.Errorf("address parameters may not be supplied through both configuration.parameters and configuration.address.parameters")
			}
			out.Parameters = make(map[string]string, len(rawParams))
			for name, val := range rawParams {
				s, ok := val.(string)
				if !ok {
					return out, fmt.Errorf("configuration.address.parameters[%q] must be a string, got %T", name, val)
				}
				out.Parameters[name] = s
			}
		}
		return out, nil
	default:
		return out, fmt.Errorf("configuration.address must be a string or an object, got %T", raw)
	}
}

// resolveAddress resolves the address configuration point (ASYNC-P-04): the
// channel's declared `address` with every `{name}` expression expanded from
// consumer-supplied parameter values, else the declared parameter's
// `default`. An absent or null address with no consumer-supplied address,
// or any expression left unresolved after defaults, is a pre-dispatch
// refusal, never a guess — this specification does not assume the channel
// key is an address, and never dials literal braces.
func resolveAddress(ch *channel, channelName string, cfg addressConfig, payloads ...any) (string, error) {
	if cfg.Address != "" {
		if strings.ContainsAny(cfg.Address, "{}") {
			return "", fmt.Errorf("configuration.address %q is not concrete: literal braces never reach the wire", cfg.Address)
		}
		return cfg.Address, nil
	}
	if ch == nil || ch.Address == "" {
		// AsyncAPI's address:null "generated dynamically at runtime" case:
		// resolvable by consumer supply, and per-invocation (not persisted).
		no := false
		return "", &configRequired{
			point:       "address",
			key:         "address",
			description: fmt.Sprintf("channel %q declares no address and none was supplied at the address configuration point (AsyncAPI's runtime-generated address); supply one", channelName),
			durable:     &no,
		}
	}
	var payload any
	if len(payloads) > 0 {
		payload = payloads[0]
	}
	return expandAddress(ch, channelName, cfg.Parameters, payload)
}

// expandAddress expands every `{name}` expression in the channel's declared
// address from the consumer-supplied parameter value, else the declared
// parameter's `default`; anything left unresolved is a pre-dispatch
// refusal. A declared enum does not gate the value (§9.2): it is the author's
// expectation, not a boundary, consistent with the server-variable point.
func expandAddress(ch *channel, channelName string, supplied map[string]string, payloads ...any) (string, error) {
	var payload any
	if len(payloads) > 0 {
		payload = payloads[0]
	}
	for name := range supplied {
		declared, ok := ch.Parameters[name]
		if !ok {
			return "", fmt.Errorf("configuration parameter %q is not declared by channel %q", name, channelName)
		}
		if declared.Location != "" {
			return "", fmt.Errorf("configuration cannot override address parameter %q because the artifact declares location %q", name, declared.Location)
		}
	}
	out := ch.Address
	for _, name := range templateExpressions(ch.Address) {
		declared := ch.Parameters[name]
		val, ok := supplied[name]
		if declared.Location != "" {
			var err error
			val, err = evaluatePayloadLocation(declared.Location, payload, name)
			if err != nil {
				return "", err
			}
			ok = true
		}
		if !ok {
			if p, exists := ch.Parameters[name]; exists && p.Default != "" {
				val = p.Default
			} else {
				var choices []string
				if p, declared := ch.Parameters[name]; declared {
					choices = p.Enum
				}
				return "", &configRequired{
					point:       "address",
					key:         name,
					description: fmt.Sprintf("channel %q: address parameter %q has no supplied value and no declared default", channelName, name),
					choices:     choices,
				}
			}
		}
		if len(declared.Enum) > 0 {
			allowed := false
			for _, candidate := range declared.Enum {
				if candidate == val {
					allowed = true
					break
				}
			}
			if !allowed {
				return "", fmt.Errorf("address parameter %q value %q is outside the artifact-declared enum", name, val)
			}
		}
		out = strings.ReplaceAll(out, "{"+name+"}", url.PathEscape(val))
	}
	if strings.ContainsAny(out, "{}") {
		return "", fmt.Errorf("channel %q: address %q still carries an unexpanded expression after parameter expansion", channelName, out)
	}
	return out, nil
}

func evaluatePayloadLocation(location string, payload any, name string) (string, error) {
	const prefix = "$message.payload#"
	if !strings.HasPrefix(location, prefix) {
		return "", fmt.Errorf("address parameter %q uses unsupported runtime expression %q", name, location)
	}
	current := payload
	pointer := strings.TrimPrefix(location, prefix)
	if pointer != "" {
		if !strings.HasPrefix(pointer, "/") {
			return "", fmt.Errorf("runtime expression %q has an invalid JSON Pointer", location)
		}
		for _, raw := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
			segment := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
			object, ok := openbindings.ToStringAnyMap(current)
			if !ok {
				return "", fmt.Errorf("runtime expression %q did not resolve against the outgoing payload", location)
			}
			var present bool
			current, present = object[segment]
			if !present {
				return "", fmt.Errorf("runtime expression %q did not resolve against the outgoing payload", location)
			}
		}
	}
	value, ok := current.(string)
	if !ok {
		return "", fmt.Errorf("runtime expression %q must resolve to a string", location)
	}
	return value, nil
}

// templateExpressions returns the `{name}` expression names of a template,
// in order of first appearance, deduplicated.
func templateExpressions(template string) []string {
	var names []string
	seen := map[string]bool{}
	rest := template
	for {
		open := strings.Index(rest, "{")
		if open < 0 {
			return names
		}
		end := strings.Index(rest[open:], "}")
		if end < 0 {
			return names
		}
		name := rest[open+1 : open+end]
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
		rest = rest[open+end+1:]
	}
}

// joinURL concatenates a base URL and a path with exactly one `/` at the
// join (§9.2's URL-assembly rule: concatenation, not RFC 3986 resolution —
// the server's pathname prefix is preserved).
func joinURL(base, path string) string {
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
}
