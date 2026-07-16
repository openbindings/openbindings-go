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

// A grpc location naming a local FILE (a compiled binary FileDescriptorSet,
// say) must refuse with the remedy named, never dial the path as an address
// (which produced an unrelated resolver error). Raw binary descriptor bytes
// have no carriage under openbindings.grpc@1 §3; the JSON carriage does.
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
	for _, want := range []string{"local file", "descriptor sets", ".proto", "reflection"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal must mention %q, got: %v", want, err)
		}
	}
}

// An embed-mode grpc source carries the SCHEMA as content; the dial address
// resolves from the target configuration point (context
// configuration.target, §9.3) else the source's location, else a
// remedy-naming refusal (GRPC-D-02: a content-only source is not
// conformant).
func TestInvokeBinding_EmbedModeAddressResolution(t *testing.T) {
	proto := `syntax = "proto3";
package tiny;
service Tiny { rpc Ping(PingMsg) returns (PingMsg); }
message PingMsg { string msg = 1; }
`
	inv := NewInvoker()
	defer func() { _ = inv.Close() }()

	// No location, no configuration.target: refuse, naming both remedies.
	h := inv.InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: proto},
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
	for _, want := range []string{"embedded content", "location", "configuration.target"} {
		if !strings.Contains(ie.Message, want) {
			t.Errorf("refusal must mention %q, got: %s", want, ie.Message)
		}
	}

	// configuration.target supplies the dial address: the invocation
	// proceeds past the address gate (failing later at the unreachable
	// endpoint, not at configuration).
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	h2 := inv.InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
		Source:  openbindings.InvocationSource{BindingSpec: BindingSpec, Content: proto},
		Ref:     "tiny.Tiny/Ping",
		Context: map[string]any{"configuration": map[string]any{"target": "127.0.0.1:1"}},
	})
	_ = h2.Write(ctx, map[string]any{"msg": "hi"})
	_, err2 := openbindings.Single(ctx, h2.Outputs())
	if err2 == nil {
		t.Fatal("dial of an unreachable address must fail")
	}
	var ie2 *openbindings.InvocationError
	if errors.As(err2, &ie2) && ie2.Code == openbindings.ErrCodeSourceConfigError {
		t.Fatalf("configuration.target must satisfy the address gate; still got config error: %s", ie2.Message)
	}
}
