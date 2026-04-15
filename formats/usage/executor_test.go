package usage

import (
	"context"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestResolveUsageKey_FromBin(t *testing.T) {
	loader := func(_ context.Context, loc string, content any) (*Spec, error) {
		return ParseKDL([]byte(`bin "mycli"`))
	}
	got := resolveUsageKey(context.Background(),"test.kdl", nil, loader)
	if got != "exec:mycli" {
		t.Errorf("got %q, want exec:mycli", got)
	}
}

func TestResolveUsageKey_FromName(t *testing.T) {
	loader := func(_ context.Context, loc string, content any) (*Spec, error) {
		return ParseKDL([]byte(`name "mycli"`))
	}
	got := resolveUsageKey(context.Background(),"test.kdl", nil, loader)
	if got != "exec:mycli" {
		t.Errorf("got %q, want exec:mycli", got)
	}
}

func TestResolveUsageKey_FromExecLocation(t *testing.T) {
	loader := func(_ context.Context, loc string, content any) (*Spec, error) {
		return ParseKDL([]byte(`cmd "test"`))
	}
	got := resolveUsageKey(context.Background(),"exec:mycli --help", nil, loader)
	if got != "exec:mycli" {
		t.Errorf("got %q, want exec:mycli", got)
	}
}

func TestResolveUsageKey_Empty(t *testing.T) {
	loader := func(_ context.Context, loc string, content any) (*Spec, error) {
		return ParseKDL([]byte(`cmd "test"`))
	}
	got := resolveUsageKey(context.Background(),"test.kdl", nil, loader)
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestFormats(t *testing.T) {
	e := NewExecutor()
	formats := e.Formats()
	if len(formats) != 1 || formats[0].Token != FormatToken {
		t.Errorf("Formats() = %v, want [{Token: %q}]", formats, FormatToken)
	}

	c := NewCreator()
	formats = c.Formats()
	if len(formats) != 1 || formats[0].Token != FormatToken {
		t.Errorf("Creator.Formats() = %v, want [{Token: %q}]", formats, FormatToken)
	}
}

func TestCreator_NoSources(t *testing.T) {
	c := NewCreator()
	_, err := c.CreateInterface(nil, &openbindings.CreateInput{})
	if err != openbindings.ErrNoSources {
		t.Errorf("err = %v, want ErrNoSources", err)
	}
}
