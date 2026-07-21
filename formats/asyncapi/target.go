package asyncapi

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	openbindings "github.com/openbindings/openbindings-go"
)

// This file implements the server and address configuration points of
// openbindings.asyncapi@1 §9.2 (ASYNC-P-04): the effective server set and
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
// whose declared security applies (§9.5: the server the connection actually
// goes to — or, under a full-URL override, the server the default selection
// would have targeted; nil when the artifact declares no such server).
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

// defaultServer returns the server the default selection targets: the first
// candidate of the effective set whose protocol revision 1 binds (§9.2), or
// nil when none exists.
func defaultServer(candidates []namedServer) *namedServer {
	for i := range candidates {
		if isBoundProtocol(strings.ToLower(candidates[i].Server.Protocol)) {
			return &candidates[i]
		}
	}
	return nil
}

// resolveTarget resolves the server configuration point for the operation's
// channel (ASYNC-P-04): consumer configuration may select another member of
// the effective set or supply a complete connection URL outright; the
// default is the effective set's first bound-protocol candidate. Under a
// full-URL override the URL's scheme decides the protocol (out-of-revision
// schemes refused) and the declared security of the server the default
// would have selected still applies (§9.5). No resolvable server is a
// pre-dispatch refusal.
//
// The `configuration.server` value SHAPE is this SDK's own carriage, not
// specification content: §9.2 pins what may be supplied and the refusals, but
// the concrete value a consumer hands to the point is implementation surface
// (R1). This SDK carries it as an object in exactly one of two mutually
// exclusive forms, and validates that shape as hygiene (a mistyped member is
// worth a loud error, not silent tolerance) — another implementation may
// carry it however it likes:
//
//	{"key": "<server-name>", "variables": {"<variable-name>": "<string-value>"}?}
//	                               // select a member of the effective server set,
//	                               // optionally supplying its declared server variables
//	{"url": "<connection-url>"}    // override with a complete connection URL
//
// Every other spelling (a bare string, the retired `name` member, key+url
// together, variables riding the url form) is refused loudly with a
// teaching error naming the two forms. Under {"key": ...} selection,
// server variables substitute supplied-else-default-else-refusal: names are
// the selected server's own declared variable names (an undeclared supplied
// name is refused, never ignored), values are strings. A declared enum does
// not gate the value — it is the author's expectation, not a boundary (§9.2).
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
			return fullURLOverride(base, def)
		}
	}

	if def == nil {
		return resolvedTarget{}, fmt.Errorf("no resolvable server: the effective server set declares no server with a supported protocol (http, https, ws, wss)")
	}
	return assembleServer(def, nil)
}

// serverConfigShapes is the teaching tail of every unrecognized-form refusal.
// The shape is this SDK's own carriage (§9.2 pins the semantics, not the
// value shape — R1); this SDK and the TS SDK keep the same shape by choice for
// a consistent developer experience, not because the specification requires
// it. Rejecting an unrecognized spelling is hygiene: a mistyped member should
// fail loudly, not be silently tolerated.
const serverConfigShapes = `this implementation's accepted shapes (openbindings.asyncapi@1 §9.2 semantics) are {"key": "<server-name>", "variables": {"<variable-name>": "<string-value>"}?} (select a member of the effective server set, "variables" optionally supplying its declared server variables) xor {"url": "<connection-url>"} (override with a complete connection URL); the two forms are mutually exclusive and "variables" composes only with "key"`

// resolveServerConfig applies one configured `server` value against the
// effective set, accepting exactly §9.2's pinned value shapes:
// {"key": "<server-name>", "variables": {...}?} xor
// {"url": "<connection-url>"}. Every other form is a loud refusal carrying
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
	switch {
	case hasKey && hasURL:
		return resolvedTarget{}, fmt.Errorf(`configuration.server carries both "key" and "url": %s`, serverConfigShapes)
	case !hasKey && !hasURL:
		return resolvedTarget{}, fmt.Errorf(`configuration.server carries neither "key" nor "url": %s`, serverConfigShapes)
	case hasVars && hasURL:
		return resolvedTarget{}, fmt.Errorf(`configuration.server carries "variables" with "url": %s`, serverConfigShapes)
	case hasKey:
		key, ok := rawKey.(string)
		if !ok || key == "" {
			return resolvedTarget{}, fmt.Errorf("configuration.server.key must be a non-empty string: %s", serverConfigShapes)
		}
		supplied, err := suppliedServerVariables(v)
		if err != nil {
			return resolvedTarget{}, err
		}
		member := serverByKey(candidates, key)
		if member == nil {
			return resolvedTarget{}, fmt.Errorf("configuration.server.key %q names no member of the effective server set", key)
		}
		return assembleServer(member, supplied)
	default:
		full, ok := rawURL.(string)
		if !ok || full == "" {
			return resolvedTarget{}, fmt.Errorf("configuration.server.url must be a non-empty string: %s", serverConfigShapes)
		}
		return fullURLOverride(full, def)
	}
}

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

// fullURLOverride resolves a consumer-supplied complete connection URL: the
// URL's scheme decides the protocol, and an out-of-revision scheme is a
// pre-dispatch refusal (§9.2). The declared security of the server the
// default selection would have targeted still applies (§9.5).
func fullURLOverride(full string, def *namedServer) (resolvedTarget, error) {
	u, err := url.Parse(full)
	if err != nil || !u.IsAbs() {
		return resolvedTarget{}, fmt.Errorf("connection URL %q is not an absolute URL", full)
	}
	scheme := strings.ToLower(u.Scheme)
	if !isBoundProtocol(scheme) {
		return resolvedTarget{}, fmt.Errorf("connection URL %q: scheme %q is not bound by openbindings.asyncapi@1 (supported: http, https, ws, wss)", full, scheme)
	}
	target := resolvedTarget{ServerURL: strings.TrimRight(full, "/"), Protocol: scheme}
	if def != nil {
		target.SecurityServer = &def.Server
	}
	return target, nil
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
		return resolvedTarget{}, fmt.Errorf("server %q: protocol %q is not bound by openbindings.asyncapi@1 (supported: http, https, ws, wss)", member.Name, srv.Protocol)
	}

	names := make([]string, 0, len(supplied))
	for name := range supplied {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, declared := srv.Variables[name]; !declared {
			return resolvedTarget{}, fmt.Errorf("configuration.server.variables[%q] names no declared variable of server %q", name, member.Name)
		}
		// A declared enum does not gate the supplied value (§9.2): it is the
		// author's expectation, not a boundary, and the same point admits a
		// full-URL override that bypasses the declaration. Undeclared names
		// still refuse (above); enum values do not.
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
// wire. A declared enum does not gate the value (§9.2): it is the author's
// expectation, not a boundary, and the same configuration point admits a
// full-URL override that bypasses the declaration entirely.
func substituteServerVariables(member *namedServer, template string, supplied map[string]string) (string, error) {
	out := template
	for _, name := range templateExpressions(template) {
		val, ok := supplied[name]
		if !ok {
			if v, declared := member.Server.Variables[name]; declared && v.Default != "" {
				val = v.Default
			} else {
				return "", fmt.Errorf(`server %q: variable %q has no supplied value and no declared default (supply one at the server configuration point's "variables" member)`, member.Name, name)
			}
		}
		out = strings.ReplaceAll(out, "{"+name+"}", val)
	}
	if strings.ContainsAny(out, "{}") {
		return "", fmt.Errorf("server %q: %q still carries an unexpanded expression after substitution", member.Name, out)
	}
	return out, nil
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
func resolveAddress(ch *channel, channelName string, cfg addressConfig) (string, error) {
	if cfg.Address != "" {
		if strings.ContainsAny(cfg.Address, "{}") {
			return "", fmt.Errorf("configuration.address %q is not concrete: literal braces never reach the wire", cfg.Address)
		}
		return cfg.Address, nil
	}
	if ch == nil || ch.Address == "" {
		return "", fmt.Errorf("channel %q declares no address and none was supplied at the address configuration point: an absent address is a refusal, never a guess", channelName)
	}
	return expandAddress(ch, channelName, cfg.Parameters)
}

// expandAddress expands every `{name}` expression in the channel's declared
// address from the consumer-supplied parameter value, else the declared
// parameter's `default`; anything left unresolved is a pre-dispatch
// refusal. A declared enum does not gate the value (§9.2): it is the author's
// expectation, not a boundary, consistent with the server-variable point.
func expandAddress(ch *channel, channelName string, supplied map[string]string) (string, error) {
	out := ch.Address
	for _, name := range templateExpressions(ch.Address) {
		val, ok := supplied[name]
		if !ok {
			if p, declared := ch.Parameters[name]; declared && p.Default != "" {
				val = p.Default
			} else {
				return "", fmt.Errorf("channel %q: address parameter %q has no supplied value and no declared default", channelName, name)
			}
		}
		out = strings.ReplaceAll(out, "{"+name+"}", val)
	}
	if strings.ContainsAny(out, "{}") {
		return "", fmt.Errorf("channel %q: address %q still carries an unexpanded expression after parameter expansion", channelName, out)
	}
	return out, nil
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

