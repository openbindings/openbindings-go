package usage

import (
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestFormats(t *testing.T) {
	e := NewInvoker()
	formats := e.Formats()
	if len(formats) != 1 || formats[0].Token != WrapperRange {
		t.Errorf("Formats() = %v, want [{Token: %q}]", formats, WrapperRange)
	}

	c := NewSynthesizer()
	formats = c.Formats()
	if len(formats) != 2 || formats[0].Token != ArtifactRange || formats[1].Token != WrapperRange {
		t.Errorf("Synthesizer.Formats() = %v, want artifact + wrapper tokens", formats)
	}
}

func TestSynthesizer_NoSources(t *testing.T) {
	c := NewSynthesizer()
	_, err := c.SynthesizeInterface(nil, &openbindings.SynthesizeInput{})
	if err != openbindings.ErrNoSources {
		t.Errorf("err = %v, want ErrNoSources", err)
	}
}
