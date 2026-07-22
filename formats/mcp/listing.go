package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
	openbindings "github.com/openbindings/openbindings-go"
)

// listing is this family's artifact (openbindings.mcp@1 §3): the aggregate of
// a server's declared tools, resources, resource templates, and prompts,
// always pagination-exhausted. Only entity identities are kept — ref matching
// is byte-exact against them (MCP-D-03), and the matched remainder itself is
// what dispatch uses — but multiplicity matters: MCP names are only
// SHOULD-unique, so ambiguity detection needs every occurrence.
type listing struct {
	tools     []string // Tool.name
	resources []string // Resource.uri
	templates []string // ResourceTemplate.uriTemplate
	prompts   []string // Prompt.name
	pinned    bool
}

// targetKind is the outcome of resolving a ref against the listing: which
// entity family the binding invokes through (§8).
type targetKind int

const (
	targetTool targetKind = iota
	targetPrompt
	targetStaticResource
	targetTemplateResource
)

// pinIdentityMember maps each allowed pinned-listing member (MCP-D-01) to
// the identity member of its 2025-11-25 entity shape — the one member
// resolution matches byte-exactly.
var pinIdentityMember = map[string]string{
	"tools":             "name",
	"resources":         "uri",
	"resourceTemplates": "uriTemplate",
	"prompts":           "name",
}

// parsePinnedListing validates source content as a pinned listing per
// openbindings.mcp@1 §3/§5 (MCP-D-01): a JSON object whose members are
// tools, resources, resourceTemplates, and prompts — each optional, each a
// pagination-exhausted entity array in the 2025-11-25 result shapes.
// Pagination members (nextCursor, _meta) and any other member are not part
// of the representation: their presence makes the content invalid, refused
// loudly here. Content arrives as raw JSON (presence-aware); any present
// value that is not an object — a present null included — is refused.
func parsePinnedListing(content json.RawMessage) (*listing, error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(content, &members); err != nil || members == nil {
		return nil, fmt.Errorf("MCP source content must be a pinned-listing object (MCP-D-01): a JSON object with entity arrays under tools/resources/resourceTemplates/prompts")
	}

	// Stray members are refused loudly, deterministically (sorted first
	// offender): nextCursor and _meta are pagination carriage, not part of
	// the pinned representation, and anything else is outside the grammar.
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, ok := pinIdentityMember[name]; !ok {
			return nil, fmt.Errorf("MCP pinned listing carries member %q, which is not part of the representation (MCP-D-01 allows only tools, resources, resourceTemplates, prompts; pagination members like nextCursor and _meta are excluded)", name)
		}
	}

	l := &listing{pinned: true}
	var perr error
	if l.tools, perr = pinEntityIdentities(members["tools"], "tools", "name"); perr != nil {
		return nil, perr
	}
	if l.resources, perr = pinEntityIdentities(members["resources"], "resources", "uri"); perr != nil {
		return nil, perr
	}
	if l.templates, perr = pinEntityIdentities(members["resourceTemplates"], "resourceTemplates", "uriTemplate"); perr != nil {
		return nil, perr
	}
	if l.prompts, perr = pinEntityIdentities(members["prompts"], "prompts", "name"); perr != nil {
		return nil, perr
	}
	return l, nil
}

// pinEntityIdentities extracts the identity strings from one pinned entity
// array. The member is optional; when present it must be an array of objects
// each carrying its identity member as a string (the 2025-11-25 result
// shapes) — anything else invalidates the pin loudly.
func pinEntityIdentities(raw json.RawMessage, member, idKey string) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("MCP pinned listing member %q must be an entity array (MCP-D-01)", member)
	}
	ids := make([]string, 0, len(entries))
	for i, entry := range entries {
		var obj map[string]any
		if err := json.Unmarshal(entry, &obj); err != nil || obj == nil {
			return nil, fmt.Errorf("MCP pinned listing %s[%d] must be an object in the 2025-11-25 result shape (MCP-D-01)", member, i)
		}
		id, ok := obj[idKey].(string)
		if !ok {
			return nil, fmt.Errorf("MCP pinned listing %s[%d] must carry a string %q (MCP-D-01)", member, i, idKey)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// pinnedDiscovery decodes a pinned listing (MCP-D-01) into the synthesis
// lanes' discovery view. The same grammar validation parsePinnedListing
// applies — stray members, entity-array shapes, identity members, all
// refused loudly — followed by decoding the full 2025-11-25 entity shapes
// the synthesis lanes read (descriptions, schemas, prompt arguments). The
// pin is authoritative (§6 content primacy): the server is never dialed. A
// pin carries no serverInfo, so ServerInfo stays nil — the interface's
// name/version, when wanted, come from SynthesizeInput.
func pinnedDiscovery(content json.RawMessage) (*discovery, error) {
	if _, err := parsePinnedListing(content); err != nil {
		return nil, err
	}
	var pin struct {
		Tools             []*gomcp.Tool             `json:"tools"`
		Resources         []*gomcp.Resource         `json:"resources"`
		ResourceTemplates []*gomcp.ResourceTemplate `json:"resourceTemplates"`
		Prompts           []*gomcp.Prompt           `json:"prompts"`
	}
	if err := json.Unmarshal(content, &pin); err != nil {
		return nil, fmt.Errorf("MCP pinned listing entities do not decode as the 2025-11-25 result shapes (MCP-D-01): %v", err)
	}
	return &discovery{
		Tools:             pin.Tools,
		Resources:         pin.Resources,
		ResourceTemplates: pin.ResourceTemplates,
		Prompts:           pin.Prompts,
	}, nil
}

// liveListing obtains the entity family a ref needs from the addressed
// server, capability-gated and followed to pagination exhaustion (MCP-P-02):
// the go-mcp iterators issue the list request repeatedly, feeding each
// nextCursor back, until the server stops returning one. Only the family the
// ref addresses is fetched — resolution consults nothing else, so the other
// families cannot affect it. The resources capability gates both resource
// lists.
// maxListItems is a defensive backstop on live-listing pagination. MCP-P-02
// mandates exhaustion, not unbounded trust: go-mcp owns the nextCursor loop
// (the iterators below page internally), so this module cannot see cursors to
// detect a repeat; instead it bounds the TOTAL items a family's exhausted
// listing may yield. A server whose pagination never terminates would
// otherwise iterate until the caller's context is cancelled. The ceiling is
// absurdly high — no real MCP server lists this many entities — so it fires
// only on a non-terminating server, refusing with the same ERR_PROTOCOL the
// TS SDK's exhaustPages backstop uses. A package var so tests can lower it.
var maxListItems = 1_000_000

func paginationOverflow() error {
	return &openbindings.InvocationError{
		Code:    openbindings.ErrCodeProtocol,
		Message: fmt.Sprintf("MCP server did not terminate pagination: exceeded %d items (openbindings.mcp@1 MCP-P-02)", maxListItems),
	}
}

func liveListing(ctx context.Context, s *mcpSession, entityType string) (*listing, error) {
	l := &listing{}
	init := s.session.InitializeResult()
	if init == nil {
		return l, nil
	}
	items := 0
	bound := func() error {
		items++
		if items > maxListItems {
			return paginationOverflow()
		}
		return nil
	}
	switch entityType {
	case "tools":
		if init.Capabilities.Tools == nil {
			return l, nil
		}
		for tool, err := range s.session.Tools(ctx, nil) {
			if err != nil {
				return nil, fmt.Errorf("list tools: %w", err)
			}
			if berr := bound(); berr != nil {
				return nil, berr
			}
			l.tools = append(l.tools, tool.Name)
		}
	case "prompts":
		if init.Capabilities.Prompts == nil {
			return l, nil
		}
		for prompt, err := range s.session.Prompts(ctx, nil) {
			if err != nil {
				return nil, fmt.Errorf("list prompts: %w", err)
			}
			if berr := bound(); berr != nil {
				return nil, berr
			}
			l.prompts = append(l.prompts, prompt.Name)
		}
	default: // resources
		if init.Capabilities.Resources == nil {
			return l, nil
		}
		for resource, err := range s.session.Resources(ctx, nil) {
			if err != nil {
				return nil, fmt.Errorf("list resources: %w", err)
			}
			if berr := bound(); berr != nil {
				return nil, berr
			}
			l.resources = append(l.resources, resource.URI)
		}
		for tmpl, err := range s.session.ResourceTemplates(ctx, nil) {
			if err != nil {
				return nil, fmt.Errorf("list resource templates: %w", err)
			}
			if berr := bound(); berr != nil {
				return nil, berr
			}
			l.templates = append(l.templates, tmpl.URITemplate)
		}
	}
	return l, nil
}

// resolveRef resolves a parsed ref against the (pinned or live, exhausted)
// listing BEFORE dispatch (§7, MCP-P-02): a remainder matching nothing makes
// the binding unresolvable, and a remainder matching more than one entry
// WITHIN its entity's collection is ambiguous and likewise unresolvable —
// loudly, never first-match. resources matches only declared resource URIs;
// resourceTemplates matches only declared template strings (§7, R5): the two
// are separate namespaces, so a resource URI and a byte-identical template
// string never collide — each is reached by its own entity token.
func resolveRef(l *listing, entityType, remainder string) (targetKind, *openbindings.InvocationError) {
	where := "server listing"
	if l.pinned {
		where = "pinned listing"
	}
	count := func(ids []string) int {
		n := 0
		for _, id := range ids {
			if id == remainder {
				n++
			}
		}
		return n
	}
	ref := entityType + "/" + remainder
	notFound := func(what string) (targetKind, *openbindings.InvocationError) {
		return 0, &openbindings.InvocationError{
			Code:    openbindings.ErrCodeRefNotFound,
			Message: fmt.Sprintf("MCP ref %q matches no %s in the %s", ref, what, where),
		}
	}
	ambiguous := func(what string, n int) (targetKind, *openbindings.InvocationError) {
		return 0, &openbindings.InvocationError{
			Code:    openbindings.ErrCodeRefNotFound,
			Message: fmt.Sprintf("MCP ref %q is ambiguous: %d %ss in the %s share that identity; an ambiguous ref is unresolvable", ref, n, what, where),
		}
	}

	switch entityType {
	case "tools":
		switch n := count(l.tools); {
		case n == 1:
			return targetTool, nil
		case n > 1:
			return ambiguous("tool", n)
		}
		return notFound("tool")
	case "prompts":
		switch n := count(l.prompts); {
		case n == 1:
			return targetPrompt, nil
		case n > 1:
			return ambiguous("prompt", n)
		}
		return notFound("prompt")
	case "resources":
		switch n := count(l.resources); {
		case n == 1:
			return targetStaticResource, nil
		case n > 1:
			return ambiguous("resource", n)
		}
		return notFound("resource")
	case "resourceTemplates":
		switch n := count(l.templates); {
		case n == 1:
			return targetTemplateResource, nil
		case n > 1:
			return ambiguous("resource template", n)
		}
		return notFound("resource template")
	default:
		return 0, &openbindings.InvocationError{
			Code:    openbindings.ErrCodeRefNotFound,
			Message: fmt.Sprintf("MCP ref %q names an unknown entity %q (expected tools, resources, resourceTemplates, or prompts)", ref, entityType),
		}
	}
}
