package mcp

import (
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

// TestLiveListing_PaginationBoundRefuses pins C7f: MCP-P-02 mandates
// pagination exhaustion, not unbounded trust. go-mcp owns the nextCursor
// loop, so this module bounds the total items a live listing may yield; a
// server that never terminates its pagination is refused with ERR_PROTOCOL —
// the same observable refusal the TS SDK's exhaustPages backstop produces.
// The bound is exercised by lowering the (absurdly high) ceiling below the
// server's tool count; without the bound the listing resolves and dispatches
// (no refusal), which is the pre-fix behavior on a non-terminating server.
func TestLiveListing_PaginationBoundRefuses(t *testing.T) {
	ts := setupDecodeServer(t) // registers several tools
	inv := NewInvoker()
	defer inv.Close()

	orig := maxListItems
	maxListItems = 1
	defer func() { maxListItems = orig }()

	_, err := invokeAndRead(t, inv, ts.URL, "tools/jsonAsText", nil)
	ie := openbindings.AsInvocationError(err)
	if ie == nil {
		t.Fatalf("expected a pagination refusal, got %v", err)
	}
	if ie.Code != openbindings.ErrCodeProtocol {
		t.Fatalf("want ERR_PROTOCOL, got %s: %s", ie.Code, ie.Message)
	}
	if !strings.Contains(ie.Message, "terminate pagination") {
		t.Errorf("message should name non-terminating pagination, got %q", ie.Message)
	}
}
