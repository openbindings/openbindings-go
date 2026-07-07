package usage

import (
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestFormats(t *testing.T) {
	// One caret range per supported tool major (2.x and 3.x — the token
	// grammar has no compound ranges).
	e := NewInvoker()
	formats := e.Formats()
	if len(formats) != len(FormatRanges) {
		t.Fatalf("Formats() = %v, want one entry per range %v", formats, FormatRanges)
	}
	for i, want := range FormatRanges {
		if formats[i].Token != want {
			t.Errorf("Formats()[%d] = %q, want %q", i, formats[i].Token, want)
		}
	}

	c := NewSynthesizer()
	formats = c.Formats()
	if len(formats) != len(FormatRanges) || formats[0].Token != FormatRanges[0] {
		t.Errorf("Synthesizer.Formats() = %v, want the bare usage token ranges", formats)
	}
}

// The version gate spans both supported tool majors: a 3.x
// min_usage_version passes, one beyond MaxTestedVersion refuses.
func TestVersionGate_ToolMajors(t *testing.T) {
	for _, v := range []string{"2.0.0", "2.13.1", "3.0.0", MaxTestedVersion} {
		ok, err := IsSupportedVersion(v)
		if err != nil || !ok {
			t.Errorf("IsSupportedVersion(%q) = %v, %v; want true", v, ok, err)
		}
	}
	if ok, _ := IsSupportedVersion("99.0.0"); ok {
		t.Error("a version beyond MaxTestedVersion must refuse")
	}
}

func TestSynthesizer_NoSources(t *testing.T) {
	c := NewSynthesizer()
	_, err := c.SynthesizeInterface(nil, &openbindings.SynthesizeInput{})
	if err != openbindings.ErrNoSources {
		t.Errorf("err = %v, want ErrNoSources", err)
	}
}
