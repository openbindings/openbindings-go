package grpc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
)

// The https:// prefix is the documented way to force TLS off port 443. It
// is our affordance, not gRPC target syntax: unstripped it reached the
// resolver as scheme https + malformed authority and failed with "too many
// colons in address", making the grpc format effectively plaintext-only
// for internal deployments.
func TestDial_TLSSchemeStripped(t *testing.T) {
	for _, addr := range []string{"https://127.0.0.1:54104", "grpcs://internal.host:8443"} {
		conn, err := dial(context.Background(), addr, dialConfig{})
		if err != nil {
			t.Fatalf("dial(%q) must accept the documented TLS affordance, got: %v", addr, err)
		}
		_ = conn.Close()
	}
	if !needsTLS("grpcs://h:50051") || !needsTLS("https://h:50051") || !needsTLS("h:443") {
		t.Error("TLS triggers: grpcs://, https://, and :443 must all fire")
	}
	if needsTLS("h:50051") {
		t.Error("bare host:port must stay plaintext")
	}
}

// A grpc location naming a local FILE that isn't a .proto (a compiled
// FileDescriptorSet, say) must refuse with the gap named, never dial the
// path as an address (which produced an unrelated resolver error).
func TestDiscover_RefusesLocalFileAddress(t *testing.T) {
	dir := t.TempDir()
	fds := filepath.Join(dir, "blend.pb")
	if err := os.WriteFile(fds, []byte{0x0a, 0x0b, 0xff}, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := discover(context.Background(), fds, dialConfig{})
	if err == nil {
		t.Fatal("a local file address must refuse discovery")
	}
	for _, want := range []string{"local file", "descriptor set", ".proto", "reflection"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal must mention %q, got: %v", want, err)
		}
	}
}

// An embed-mode grpc source carries the SCHEMA as content; the dial address
// resolves from location, else context metadata.baseURL (the same override
// key the openapi invoker honors), else a remedy-naming refusal.
func TestInvokeBinding_EmbedModeAddressResolution(t *testing.T) {
	proto := `syntax = "proto3";
package tiny;
service Tiny { rpc Ping(PingMsg) returns (PingMsg); }
message PingMsg { string msg = 1; }
`
	inv := NewInvoker()
	defer func() { _ = inv.Close() }()

	// No location, no metadata: refuse, naming both remedies.
	h := inv.InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: "grpc", Content: proto},
		Ref:    "tiny.Tiny/Ping",
	})
	_, err := openbindings.Single(context.Background(), h.Outputs())
	if err == nil {
		t.Fatal("embedded schema with no address must refuse")
	}
	var ie *openbindings.InvocationError
	if !errors.As(err, &ie) || ie.Code != openbindings.ErrCodeSourceConfigError {
		t.Fatalf("want ERR_SOURCE_CONFIG_ERROR, got %v", err)
	}
	for _, want := range []string{"embedded content", "location", "baseURL"} {
		if !strings.Contains(ie.Message, want) {
			t.Errorf("refusal must mention %q, got: %s", want, ie.Message)
		}
	}

	// metadata.baseURL supplies the dial address: the invocation proceeds
	// past the address gate (failing later at the unreachable endpoint,
	// not at configuration).
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	h2 := inv.InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
		Source:  openbindings.InvocationSource{Format: "grpc", Content: proto},
		Ref:     "tiny.Tiny/Ping",
		Context: map[string]any{"metadata": map[string]any{"baseURL": "127.0.0.1:1"}},
	})
	_ = h2.Write(ctx, map[string]any{"msg": "hi"})
	_, err2 := openbindings.Single(ctx, h2.Outputs())
	if err2 == nil {
		t.Fatal("dial of an unreachable address must fail")
	}
	var ie2 *openbindings.InvocationError
	if errors.As(err2, &ie2) && ie2.Code == openbindings.ErrCodeSourceConfigError {
		t.Fatalf("baseURL must satisfy the address gate; still got config error: %s", ie2.Message)
	}
}
