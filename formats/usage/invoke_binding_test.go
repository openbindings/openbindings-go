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
