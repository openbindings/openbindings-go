package openapi

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	openbindings "github.com/openbindings/openbindings-go"
)

// This file implements §9.3 of openbindings.openapi@1 (OAPI-P-05): the
// target URL's server half. Server resolution is a named configuration
// point; consultation order (§9.4) is per-invocation configuration →
// consumer-level configuration → the default. Both configuration tiers
// arrive merged in the binding context's `configuration` field (the
// operation invoker resolves consumer-level context into the same carrier),
// so this file consults one merged value.

// resolveServer resolves the operation's server per OAPI-P-05:
//
//   - The effective server list is the OAS's: the operation's `servers`,
//     else the path item's, else the document's, else the implied
//     `url: "/"`.
//
//   - A sole effective entry selects itself with declared variable defaults;
//     multiple artifact alternatives require configuration to select one.
//
//   - Consumer configuration (context.configuration.server) may instead
//     select another entry, supply variable values, or supply a complete
//     base URL outright. Accepted shapes:
//
//     "https://api.example.com"                        // absolute base URL outright
//     "https://{env}.example.com"                      // string matching a declared entry's url
//     {"baseUrl": "https://api.example.com"}           // absolute base URL outright
//     {"url": "https://{env}.example.com"}             // select the declared entry with that url
//     {"index": 1}                                     // select the effective list's Nth entry
//     {"variables": {"env": "staging"}}                // server-variable values (validated against enum)
//
//     `url`/`index` and `variables` compose; `baseUrl` stands alone.
//
//   - A relative effective-server URL (the implied "/" included) resolves
//     against the artifact's base URI (§6: the source's location) per
//     RFC 3986. The one pre-dispatch refusal is a server URL that cannot
//     resolve to an absolute URL.
//
// The legacy context.metadata.baseURL override is honored below the
// configuration point (the configuration point is the contract surface).
func resolveServer(doc *openapi3.T, pathItem *openapi3.PathItem, op *openapi3.Operation, bindCtx map[string]any, sourceLocation string) (string, error) {
	servers := effectiveServers(doc, pathItem, op)

	if cfg := openbindings.ContextConfiguration(bindCtx); cfg != nil {
		if raw, ok := cfg["server"]; ok && raw != nil {
			resolved, err := resolveServerConfig(raw, servers)
			if err != nil {
				return "", err
			}
			return absolutizeServerURL(resolved, sourceLocation)
		}
	}

	if meta := openbindings.ContextMetadata(bindCtx); meta != nil {
		if base, ok := meta["baseURL"].(string); ok && base != "" {
			return absolutizeServerURL(base, sourceLocation)
		}
	}
	if len(servers) != 1 {
		// Declared alternatives without a selection are missing consumer
		// configuration, not source misconfiguration (ruled 2026-08-13,
		// R1+R5): §9.3 calls this "the retryable context challenge above,
		// never a terminal refusal", so the signal is a configRequired, which
		// the twin's invoke path turns into a CONTEXT_REQUIRED challenge.
		//
		// The class is unobservable in THIS package — invocation delegates to
		// openapi-client/go and the only caller here is synthesis coverage,
		// which emits `configuration.server` on any error. It is twinned
		// because the shared case table asserts the class in both engines, and
		// because an untwinned line in twinned files is how two engines that
		// are supposed to agree stop agreeing (filed as residue (b) of
		// F-O1-11, closed here).
		choices := make([]string, 0, len(servers))
		for _, srv := range servers {
			if srv != nil {
				choices = append(choices, srv.URL)
			}
		}
		return "", &configRequired{
			point:       "server",
			path:        "/url",
			description: fmt.Sprintf("the effective server list has %d alternatives; configuration.server must select one (openbindings.openapi@1 OAPI-P-05)", len(servers)),
			choices:     choices,
			durable:     &serverChoiceDurable,
		}
	}

	substituted, err := substituteServerVariables(servers[0], nil)
	if err != nil {
		return "", err
	}
	return absolutizeServerURL(substituted, sourceLocation)
}

// effectiveServers returns the OAS effective server list: operation servers,
// else path-item servers, else document servers, else the OAS-defined
// implied server of url "/".
func effectiveServers(doc *openapi3.T, pathItem *openapi3.PathItem, op *openapi3.Operation) openapi3.Servers {
	if op != nil && op.Servers != nil && len(*op.Servers) > 0 {
		return *op.Servers
	}
	if pathItem != nil && len(pathItem.Servers) > 0 {
		return pathItem.Servers
	}
	if doc != nil && len(doc.Servers) > 0 {
		return doc.Servers
	}
	return openapi3.Servers{&openapi3.Server{URL: "/"}}
}

// resolveServerConfig applies one configured `server` value against the
// effective list, returning the (possibly still relative) server URL.
func resolveServerConfig(raw any, servers openapi3.Servers) (string, error) {
	switch v := raw.(type) {
	case string:
		if srv := serverByURL(servers, v); srv != nil {
			return substituteServerVariables(srv, nil)
		}
		if denotesTargetBase(v) {
			return v, nil
		}
		return "", fmt.Errorf("configuration.server %q matches no declared server entry and is not an absolute base URL", v)
	case map[string]any:
		if base, ok := v["baseUrl"].(string); ok && base != "" {
			if !denotesTargetBase(base) {
				return "", fmt.Errorf("configuration.server.baseUrl %q is not an absolute URL", base)
			}
			return base, nil
		}
		srv := servers[0]
		if entryURL, ok := v["url"].(string); ok && entryURL != "" {
			srv = serverByURL(servers, entryURL)
			if srv == nil {
				return "", fmt.Errorf("configuration.server.url %q matches no declared server entry", entryURL)
			}
		} else if idxRaw, ok := v["index"]; ok {
			idx, ok := configIndex(idxRaw)
			if !ok || idx < 0 || idx >= len(servers) {
				return "", fmt.Errorf("configuration.server.index %v is not a valid index into the effective server list (%d entries)", idxRaw, len(servers))
			}
			srv = servers[idx]
		}
		var vars map[string]string
		if rawVars, ok := v["variables"].(map[string]any); ok {
			vars = make(map[string]string, len(rawVars))
			for name, val := range rawVars {
				s, ok := val.(string)
				if !ok {
					return "", fmt.Errorf("configuration.server.variables[%q] must be a string, got %T", name, val)
				}
				vars[name] = s
			}
		}
		return substituteServerVariables(srv, vars)
	default:
		return "", fmt.Errorf("configuration.server must be a string or an object, got %T", raw)
	}
}

// serverByURL selects the declared entry whose url template matches exactly.
func serverByURL(servers openapi3.Servers, u string) *openapi3.Server {
	for _, srv := range servers {
		if srv != nil && srv.URL == u {
			return srv
		}
	}
	return nil
}

func configIndex(raw any) (int, bool) {
	switch n := raw.(type) {
	case int:
		return n, true
	case float64:
		if n == float64(int(n)) {
			return int(n), true
		}
	}
	return 0, false
}

// substituteServerVariables substitutes each declared server variable with
// the supplied value or its declared default. A variable with neither a
// supplied value nor a declared default, and a supplied variable the entry
// does not declare, are loud errors. A declared enum constrains substitution
// values exactly as the artifact declares; the separate complete-base-URL
// override is an explicit configuration choice, not permission to weaken the
// selected Server Object.
func substituteServerVariables(srv *openapi3.Server, supplied map[string]string) (string, error) {
	u := srv.URL
	names := make([]string, 0, len(srv.Variables))
	for name := range srv.Variables {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		v := srv.Variables[name]
		if v == nil {
			continue
		}
		val, ok := supplied[name]
		if !ok {
			val = v.Default
		}
		if val == "" && v.Default == "" {
			return "", &configRequired{
				point:       "server",
				path:        "/variables/" + escapeJSONPointerSegment(name),
				description: fmt.Sprintf("server %q: variable %q has no supplied value and no declared default", srv.URL, name),
				choices:     v.Enum,
			}
		}
		if len(v.Enum) > 0 {
			allowed := false
			for _, candidate := range v.Enum {
				if candidate == val {
					allowed = true
					break
				}
			}
			if !allowed {
				return "", fmt.Errorf("server %q variable %q value %q is outside its declared enum", srv.URL, name, val)
			}
		}
		u = strings.ReplaceAll(u, "{"+name+"}", val)
	}
	for name := range supplied {
		if _, declared := srv.Variables[name]; !declared {
			return "", fmt.Errorf("server %q declares no variable %q", srv.URL, name)
		}
	}
	return u, nil
}

// absolutizeServerURL resolves a (possibly relative) server URL to an
// absolute target base: a URL that already denotes a target address passes
// through; a relative reference resolves against the artifact's base URI —
// the source's location (§6) — per RFC 3986. A server URL that cannot
// resolve to an absolute URL is the §9.3 pre-dispatch refusal. The returned
// URL carries no trailing slash, so joining with the operation's path
// template is concatenation.
//
// Whether a string denotes a target address is decided by denotesTargetBase
// (target_base.go), which reads RFC 3986's URI production and RFC 9110's
// non-empty-host requirement for the http and https schemes, rather than by
// net/url. See that file for why the host parser was the wrong authority.
//
// THE OUTCOME CLASS IS A REFUSAL, NOT A RETRYABLE CHALLENGE. §9.3 partitions
// the two ways the `server` point can go unanswered inside one sentence: "A
// missing selection from a multi-entry list is the retryable context challenge
// above, never a terminal refusal; an out-of-enum variable value, or a server
// URL that cannot resolve to an absolute URL — the implied `/` with no base
// URI, for instance — is a pre-dispatch refusal." OAPI-P-05 restates it
// ("unresolvable targets refuse before dispatch"). This function is the second
// half of that sentence, so it MUST NOT return a configRequired: that type is
// the first half's signal, which the invoke path turns into a retryable
// CONTEXT_REQUIRED challenge.
//
// In THIS package the class is unobservable — invocation delegates to
// openapi-client/go, and the only caller here is synthesis coverage
// (coverage.go), which emits `configuration.server` on any error and is
// therefore unmoved. It is twinned anyway, because an untwinned line in twinned
// files is how the two engines drift apart.
func absolutizeServerURL(serverURL, sourceLocation string) (string, error) {
	if denotesTargetBase(serverURL) {
		return strings.TrimRight(serverURL, "/"), nil
	}
	// Only a relative reference can be completed by a base URI. A string
	// carrying a scheme has already named an address, so failing the
	// predicate means that address does not exist and no base can supply it.
	if !hasURIScheme(serverURL) && sourceLocation != "" && denotesTargetBase(sourceLocation) {
		base, berr := url.Parse(sourceLocation)
		ref, rerr := url.Parse(serverURL)
		if berr == nil && rerr == nil {
			return strings.TrimRight(base.ResolveReference(ref).String(), "/"), nil
		}
	}
	return "", fmt.Errorf("server URL %q cannot resolve to an absolute URL: supply a base URL at the server configuration point (openbindings.openapi@1 §9.3, OAPI-P-05)", serverURL)
}

// configRequired is the typed signal a resolution helper returns when a named
// configuration point cannot resolve because a value is absent (no default,
// no supplied value) — a resolvable-missing value, not a malformed one. The
// invoke path turns it into a config.value CONTEXT_REQUIRED challenge
// (retryable after resolution, R1a) rather than a terminal
// ERR_SOURCE_CONFIG_ERROR. It implements error so it rides the existing
// (…, error) returns unchanged. Configuration may be sensitive according to
// its meaning; consumers decide whether the challenge target is sufficient
// for any stored-value release.
type configRequired struct {
	point       string
	path        string
	description string
	choices     []string
	durable     *bool
}

func (c *configRequired) Error() string { return c.description }

var serverChoiceDurable = true
