package openbindings

import (
	"reflect"
	"testing"
)

func TestCheckBindingSpecsExactDeduplicatedAndOrdered(t *testing.T) {
	warranted := []BindingSpecInfo{
		{BindingSpec: "openbindings.openapi@1"},
		{BindingSpec: "openbindings.grpc@1"},
	}
	input := []string{
		"openbindings.grpc@1",
		"openbindings.openapi",
		"openbindings.grpc@1",
		"openbindings.openapi@1",
		"openbindings.openapi@10",
	}
	want := []BindingSpecVerdict{
		{BindingSpec: "openbindings.grpc@1", Supported: true},
		{BindingSpec: "openbindings.openapi", Supported: false},
		{BindingSpec: "openbindings.openapi@1", Supported: true},
		{BindingSpec: "openbindings.openapi@10", Supported: false},
	}

	if got := CheckBindingSpecs(input, warranted); !reflect.DeepEqual(got, want) {
		t.Fatalf("CheckBindingSpecs() = %#v, want %#v", got, want)
	}
}

func TestCheckBindingSpecsEmptyInputReturnsArray(t *testing.T) {
	got := CheckBindingSpecs(nil, []BindingSpecInfo{{BindingSpec: "openbindings.openapi@1"}})
	if got == nil {
		t.Fatal("CheckBindingSpecs(nil) returned nil, want non-nil empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("CheckBindingSpecs(nil) = %#v, want empty", got)
	}
}
