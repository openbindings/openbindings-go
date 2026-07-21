package asyncapi

import "fmt"

// This file is the format seam's endpoint-resolution export: the server and
// address configuration points of openbindings.asyncapi@1 §9.2 (ASYNC-P-04),
// resolved for one operation WITHOUT dispatching an invocation. It exists for
// consumers that dial an AsyncAPI-described endpoint with their own transport
// (the ob CLI's delegate frame lane is the consumer) and must not re-derive
// the specification's server-selection rules outside this package. The
// resolution slice is exactly the one runBinding executes before dispatch —
// same helpers, same order — so an exported resolution and an invocation
// through Invoker can never disagree.
//
// GO-ONLY EXPORT — a named gap, not an oversight: the TS
// @openbindings/asyncapi package keeps its document model and target
// resolution internal. ob is this seam's sole consumer, and ob is Go; a TS
// consumer with the same need delegates to a running `ob start` instead
// (the delegate model). Recorded in this module's README ("Endpoint
// resolution") and CHANGELOG.

// Document is a parsed AsyncAPI 3.0 document: an opaque handle for endpoint
// resolution. Obtain one with ParseDocument. The document model itself stays
// internal — the handle exposes resolution, not the artifact's shape.
type Document struct {
	doc *document
}

// ParseDocument parses AsyncAPI document bytes — JSON or YAML, discriminated
// by the leading non-whitespace byte, never by content sniffing beyond that —
// into a Document handle. The artifact's own `asyncapi` field discriminates
// the accepted line: 3.0.x only, anything else (AsyncAPI 2.x included) is
// refused loudly (ASYNC-P-01). Internal Reference Objects are resolved per
// the artifact's own reference rules — an operations-map entry that is a
// Reference Object resolves through it before the operation-object test
// (ASYNC-D-03); a dangling reference is refused at resolution time, not
// here. Pure: no I/O — fetching the document is the caller's business.
func ParseDocument(data []byte) (*Document, error) {
	doc, err := parseDocument(data)
	if err != nil {
		return nil, err
	}
	return &Document{doc: doc}, nil
}

// Endpoint is one operation's resolved connection endpoint: the target URL
// assembly of openbindings.asyncapi@1 §9.2, with the deciding protocol.
type Endpoint struct {
	// URL is the complete connection URL: scheme from the deciding
	// protocol; authority from the resolved server's `host` with every
	// variable substituted; path from the server's `pathname` (if any)
	// concatenated with the expanded channel address, normalized to exactly
	// one `/` at each join. Concatenation, not RFC 3986 resolution — the
	// pathname prefix is preserved (§9.2's URL-assembly rule).
	URL string

	// Protocol is the deciding protocol: the resolved server's `protocol`,
	// or the scheme of a consumer-supplied connection URL. Always one of
	// the protocols revision 1 binds — http, https, ws, wss — anything
	// else is refused during resolution, never returned.
	Protocol string
}

// ResolveEndpoint resolves the connection endpoint of the operation a
// binding ref names, per the server and address configuration points of
// openbindings.asyncapi@1 §9.2 (ASYNC-P-04). Pure: nothing is dialed, and
// every unresolvable input is an error, never a guess.
//
// ref is ASYNC-D-03's JSON Pointer `#/operations/<operation-key>` — the only
// conformant spelling (RFC 6901-escaped; a bare operation key is refused).
//
// The server point: the default is the first candidate of the operation's
// effective server set whose protocol revision 1 binds — the channel's
// declared `servers` subset when present and non-empty (in the artifact's
// own array order), else the document's `servers` map in lexicographic key
// order, this specification's determinism rule. Consumer configuration
// (bindingContext's `configuration.server`) may select another member of the
// effective set or supply a complete connection URL outright; under a
// full-URL override the URL's scheme decides the protocol, out-of-revision
// schemes refused. Server variables substitute from consumer-supplied
// values, else declared defaults — an unsubstitutable variable is a refusal.
//
// The address point: the channel's declared `address` with every `{name}`
// expression expanded from consumer-supplied parameter values
// (`configuration.address`), else the declared parameter's default. An
// absent or null address with no consumer-supplied address, or any
// expression left unresolved after defaults, is a refusal — this
// specification does not assume the channel key is an address, and literal
// braces never reach the wire.
//
// A nil bindingContext resolves the defaults. NOT consulted here — the
// invoker's business at dispatch, not part of the endpoint: declared
// protocol `bindings` objects governing the upgrade request (§8: a
// websockets channel binding's method/query/headers) and credential
// application (§9.5).
func (d *Document) ResolveEndpoint(ref string, bindingContext map[string]any) (Endpoint, error) {
	opID, err := parseRef(ref)
	if err != nil {
		return Endpoint{}, err
	}
	asyncOp, ok := d.doc.Operations[opID]
	if !ok {
		return Endpoint{}, fmt.Errorf("operation %q not in AsyncAPI doc", opID)
	}
	if asyncOp.Ref != "" {
		// resolveRefs leaves Ref set only when the reference dangles
		// (ASYNC-D-03: resolution precedes the operation-object test).
		return Endpoint{}, fmt.Errorf("operation %q is a Reference Object whose target %q does not resolve in the AsyncAPI doc", opID, asyncOp.Ref)
	}

	// The binding target is the addressed operation's channel (§8), reached
	// through a resolved server and expanded address (§9.2) — the same
	// resolution slice runBinding executes before dispatch.
	channelName := extractRefName(asyncOp.Channel.Ref)
	var ch *channel
	if c, ok := d.doc.Channels[channelName]; ok {
		ch = &c
	}

	target, err := resolveTarget(d.doc, ch, bindingContext)
	if err != nil {
		return Endpoint{}, err
	}
	addrCfg, err := addressConfiguration(bindingContext)
	if err != nil {
		return Endpoint{}, err
	}
	address, err := resolveAddress(ch, channelName, addrCfg)
	if err != nil {
		return Endpoint{}, err
	}

	return Endpoint{URL: joinURL(target.ServerURL, address), Protocol: target.Protocol}, nil
}
