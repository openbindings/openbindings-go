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
// configuration can select a member by name and so security derivation can
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
// Accepted `configuration.server` shapes:
//
//	"prod"                                  // select the effective-set member by name
//	"wss://api.example.com/v2"              // complete connection URL outright
//	{"name": "prod"}                        // select by name
//	{"url": "wss://api.example.com/v2"}     // complete connection URL outright
//	{"variables": {"env": "staging"}}       // server-variable values (compose with name)
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

// resolveServerConfig applies one configured `server` value against the
// effective set.
func resolveServerConfig(raw any, candidates []namedServer, def *namedServer) (resolvedTarget, error) {
	switch v := raw.(type) {
	case string:
		if member := serverByName(candidates, v); member != nil {
			return assembleServer(member, nil)
		}
		if u, err := url.Parse(v); err == nil && u.IsAbs() {
			return fullURLOverride(v, def)
		}
		return resolvedTarget{}, fmt.Errorf("configuration.server %q names no member of the effective server set and is not an absolute connection URL", v)
	case map[string]any:
		if full, ok := v["url"].(string); ok && full != "" {
			return fullURLOverride(full, def)
		}
		selected := def
		if name, ok := v["name"].(string); ok && name != "" {
			selected = serverByName(candidates, name)
			if selected == nil {
				return resolvedTarget{}, fmt.Errorf("configuration.server.name %q names no member of the effective server set", name)
			}
		}
		if selected == nil {
			return resolvedTarget{}, fmt.Errorf("no resolvable server: the effective server set declares no server with a supported protocol (http, https, ws, wss)")
		}
		var vars map[string]string
		if rawVars, ok := v["variables"].(map[string]any); ok {
			vars = make(map[string]string, len(rawVars))
			for name, val := range rawVars {
				s, ok := val.(string)
				if !ok {
					return resolvedTarget{}, fmt.Errorf("configuration.server.variables[%q] must be a string, got %T", name, val)
				}
				vars[name] = s
			}
		}
		return assembleServer(selected, vars)
	default:
		return resolvedTarget{}, fmt.Errorf("configuration.server must be a string or an object, got %T", raw)
	}
}

// serverByName selects the effective-set member with the given servers-map
// key, or nil.
func serverByName(candidates []namedServer, name string) *namedServer {
	for i := range candidates {
		if candidates[i].Name == name {
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
// way). A member the consumer selects by name must still speak a bound
// protocol — out-of-revision protocols are refused pre-dispatch, never
// dialed.
func assembleServer(member *namedServer, supplied map[string]string) (resolvedTarget, error) {
	srv := member.Server
	proto := strings.ToLower(srv.Protocol)
	if !isBoundProtocol(proto) {
		return resolvedTarget{}, fmt.Errorf("server %q: protocol %q is not bound by openbindings.asyncapi@1 (supported: http, https, ws, wss)", member.Name, srv.Protocol)
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
// host or pathname template from the consumer-supplied value, else the
// variable's declared default (ASYNC-P-04). An unsubstitutable variable is
// a pre-dispatch refusal, and literal braces never reach the wire. A
// supplied value outside a declared non-empty enum is refused loudly (the
// declaration's own constraint, incorporated).
func substituteServerVariables(member *namedServer, template string, supplied map[string]string) (string, error) {
	out := template
	for _, name := range templateExpressions(template) {
		val, ok := supplied[name]
		if !ok {
			if v, declared := member.Server.Variables[name]; declared && v.Default != "" {
				val = v.Default
			} else {
				return "", fmt.Errorf("server %q: variable %q has no supplied value and no declared default", member.Name, name)
			}
		}
		if v, declared := member.Server.Variables[name]; declared && len(v.Enum) > 0 && !containsString(v.Enum, val) {
			return "", fmt.Errorf("server %q: variable %q value %q is not in the declared enum %v", member.Name, name, val, v.Enum)
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
// refusal. A supplied value outside a declared non-empty enum is refused
// loudly (the Parameter Object's own constraint, incorporated).
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
		if p, declared := ch.Parameters[name]; declared && len(p.Enum) > 0 && !containsString(p.Enum, val) {
			return "", fmt.Errorf("channel %q: address parameter %q value %q is not in the declared enum %v", channelName, name, val, p.Enum)
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

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
