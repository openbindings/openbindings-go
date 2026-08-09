package usage

import (
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestBindingSpecs(t *testing.T) {
	// One exact identifier (openbindings.usage@1); identifiers are exact
	// and opaque — no version ranges.
	e := NewInvoker()
	specs := e.BindingSpecs()
	if len(specs) != 1 || specs[0].BindingSpec != BindingSpec {
		t.Fatalf("BindingSpecs() = %v, want [%s]", specs, BindingSpec)
	}

	c := NewSynthesizer()
	specs = c.BindingSpecs()
	if len(specs) != 1 || specs[0].BindingSpec != BindingSpec {
		t.Errorf("Synthesizer.BindingSpecs() = %v, want [%s]", specs, BindingSpec)
	}
}

// The version gate spans both supported tool majors: a 3.x
// min_usage_version passes, one beyond MaxTestedVersion refuses.
func TestVersionGate_ToolMajors(t *testing.T) {
	for _, v := range []string{"1.3", "2.0.0", "2.13.1", "3.0.0", MaxTestedVersion} {
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
	iface, err := c.SynthesizeInterface(nil, &openbindings.SynthesizeInput{Name: "scaffold"})
	if err != nil {
		t.Fatal(err)
	}
	if iface.OpenBindings != openbindings.MaxTestedVersion || iface.Name != "scaffold" || len(iface.Operations) != 0 || len(iface.Sources) != 0 || len(iface.Bindings) != 0 {
		t.Fatalf("unexpected source-less scaffold: %+v", iface)
	}
}
