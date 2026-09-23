package synthesize

import (
	"context"
	"reflect"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/bindingsupport"
)

type supportTestSynthesizer struct {
	listed    []bindingsupport.BindingSpecInfo
	warranted []bindingsupport.BindingSpecInfo
	name      string
}

func (s *supportTestSynthesizer) BindingSpecs() []bindingsupport.BindingSpecInfo {
	return append([]bindingsupport.BindingSpecInfo(nil), s.listed...)
}

func (s *supportTestSynthesizer) CheckBindingSpecs(bindingSpecs []string) []bindingsupport.BindingSpecVerdict {
	return bindingsupport.CheckBindingSpecs(bindingSpecs, s.warranted)
}

func (s *supportTestSynthesizer) SynthesizeInterface(context.Context, *SynthesizeInput) (*openbindings.Interface, error) {
	return &openbindings.Interface{OpenBindings: "0.2.0", Name: openbindings.Present(s.name)}, nil
}

func TestCombineSynthesizersChecksAuthoritativeSupport(t *testing.T) {
	hidden := &supportTestSynthesizer{
		warranted: []bindingsupport.BindingSpecInfo{{BindingSpec: "example.hidden@1"}},
		name:      "hidden",
	}
	listed := &supportTestSynthesizer{
		listed:    []bindingsupport.BindingSpecInfo{{BindingSpec: "example.listed@1"}},
		warranted: []bindingsupport.BindingSpecInfo{{BindingSpec: "example.listed@1"}},
		name:      "listed",
	}
	combined := CombineSynthesizers(hidden, listed)

	input := []string{"example.hidden@1", "example.hidden", "example.listed@1", "example.hidden@1"}
	want := []bindingsupport.BindingSpecVerdict{
		{BindingSpec: "example.hidden@1", Supported: true},
		{BindingSpec: "example.hidden", Supported: false},
		{BindingSpec: "example.listed@1", Supported: true},
	}
	if got := combined.CheckBindingSpecs(input); !reflect.DeepEqual(got, want) {
		t.Fatalf("CheckBindingSpecs() = %#v, want %#v", got, want)
	}
	if got := combined.BindingSpecs(); len(got) != 1 || got[0].BindingSpec != "example.listed@1" {
		t.Fatalf("BindingSpecs() = %#v, want only advisory listed identifier", got)
	}

	iface, err := combined.SynthesizeInterface(context.Background(), &SynthesizeInput{
		Sources: []SynthesizeSource{{BindingSpec: "example.hidden@1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if iface.Name == nil || *iface.Name != "hidden" {
		t.Fatalf("synthesized interface name is not hidden: %+v", iface)
	}
}
