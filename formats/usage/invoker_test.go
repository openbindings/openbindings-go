package usage

import (
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestFormats(t *testing.T) {
	e := NewInvoker()
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
