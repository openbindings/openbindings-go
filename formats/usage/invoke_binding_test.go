package usage

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestRunCLI_OutputCapEnforced(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	// Emit ~27MB of printable text (base64 of 20MB of zeros), exceeding the
	// 10MB cap. base64 avoids NUL bytes that BSD tr mishandles.
	_, err := runCLI(context.Background(), "sh",
		[]string{"-c", "head -c 20000000 /dev/zero | base64"}, nil, nil,
		openbindings.DefaultMaxDeliveryUnitBytes)
	if err == nil {
		t.Fatal("expected an overflow error for oversized output")
	}
	if !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("expected an 'exceeded' error, got: %v", err)
	}
}

func TestCappedBuffer_StopsAtLimit(t *testing.T) {
	b := &cappedBuffer{limit: 10}
	n, err := b.Write([]byte("0123456789ABCDEF"))
	if err != nil || n != 16 {
		t.Fatalf("Write reported (%d, %v), want (16, nil)", n, err)
	}
	if !b.overflow {
		t.Fatal("expected overflow flag set")
	}
	if b.Len() != 10 || b.String() != "0123456789" {
		t.Fatalf("buffer retained %q (len %d), want first 10 bytes", b.String(), b.Len())
	}
	// A subsequent write is dropped but still reported fully written.
	if n, err := b.Write([]byte("more")); err != nil || n != 4 {
		t.Fatalf("post-overflow Write reported (%d, %v), want (4, nil)", n, err)
	}
	if b.Len() != 10 {
		t.Fatalf("buffer grew past limit: len %d", b.Len())
	}
}

func TestBuiltinDecodeText_RefusesInvalidUTF8(t *testing.T) {
	_, err := builtinDecodeText(openbindings.InvokeSite{}, openbindings.RawResult{Body: []byte{0xff}})
	if err == nil || err.Error() != "Invocation result could not be decoded" {
		t.Fatalf("invalid UTF-8 must fail loudly, got %v", err)
	}
	got, err := builtinDecodeText(openbindings.InvokeSite{}, openbindings.RawResult{Body: []byte("ok\r\n\n")})
	if err != nil || got != "ok" {
		t.Fatalf("valid text decode = %q, %v; want ok", got, err)
	}
}

func rootCommandFromText(t *testing.T, text string) *Command {
	t.Helper()
	spec, err := ParseKDL([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	cmd := rootCommand(spec)
	if cmd == nil {
		t.Fatal("fixture has no callable root command")
	}
	return cmd
}

func TestApplyUsageConfiguration_ConditionalRequirementsUseCanonicalIdentity(t *testing.T) {
	cmd := rootCommandFromText(t, `
bin "tool"
flag "--file <file>" required_if="--dir"
flag "--dir <dir>"
flag "--identity <id>" required_unless="--anonymous"
flag "-a" {
  alias "--anonymous"
}
`)
	if _, ierr := applyUsageConfiguration(cmd, nil, map[string]any{"dir": "tmp", "anonymous": true}, nil, nil); ierr == nil || ierr.Code != openbindings.ErrCodeValidationFailed {
		t.Fatalf("required_if refusal = %v", ierr)
	}
	if _, ierr := applyUsageConfiguration(cmd, nil, nil, nil, nil); ierr == nil || ierr.Code != openbindings.ErrCodeValidationFailed {
		t.Fatalf("required_unless refusal = %v", ierr)
	}
}

func TestApplyUsageConfiguration_DynamicChoicesUnionLiteralAndEnvironment(t *testing.T) {
	cmd := rootCommandFromText(t, `
bin "tool"
flag "--environment" {
  arg "<environment>" {
    choices "dev" env="DEPLOY_ENVS"
  }
}
`)
	context := map[string]any{"environment": map[string]any{"DEPLOY_ENVS": "staging, prod"}}
	if _, ierr := applyUsageConfiguration(cmd, nil, map[string]any{"environment": "prod"}, context, nil); ierr != nil {
		t.Fatalf("dynamic choice must be accepted: %v", ierr)
	}
	if _, ierr := applyUsageConfiguration(cmd, nil, map[string]any{"environment": "qa"}, context, nil); ierr == nil || ierr.Code != openbindings.ErrCodeValidationFailed {
		t.Fatalf("out-of-set choice must refuse: %v", ierr)
	}
	if _, ierr := applyUsageConfiguration(cmd, nil, map[string]any{"environment": "dev"}, nil, nil); ierr != nil {
		t.Fatalf("literal choice remains valid without the dynamic environment: %v", ierr)
	}
}
