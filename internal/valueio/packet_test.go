package valueio_test

import (
	"context"
	"errors"
	"testing"

	"github.com/openbindings/openbindings-go/internal/value"
	"github.com/openbindings/openbindings-go/internal/valueio"
)

func TestCaptureAppliesPerValueLimits(t *testing.T) {
	limits := value.Limits{MaxUnits: 512}
	var limit *value.LimitError
	if _, err := valueio.Capture(context.Background(), limits, make([]any, 30)); !errors.As(err, &limit) {
		t.Fatalf("oversized value accepted: %v", err)
	}
	if _, err := valueio.Capture(context.Background(), limits, map[string]any{"x": "y"}); err != nil {
		t.Fatal(err)
	}
}

func TestViewBorrowsOrdinaryTreesAndDetachesMutableViews(t *testing.T) {
	limits := value.Limits{}
	p, err := valueio.Capture(context.Background(), limits, map[string]any{"child": map[string]any{"name": "before"}})
	if err != nil {
		t.Fatal(err)
	}
	shared, err := p.View(context.Background(), limits, false)
	if err != nil {
		t.Fatal(err)
	}
	owned, err := p.View(context.Background(), limits, true)
	if err != nil {
		t.Fatal(err)
	}
	owned.(map[string]any)["child"].(map[string]any)["name"] = "after"
	if shared.(map[string]any)["child"].(map[string]any)["name"] != "before" {
		t.Fatal("mutable view aliased the snapshot")
	}
	again, err := p.View(context.Background(), limits, true)
	if err != nil {
		t.Fatal(err)
	}
	if again.(map[string]any)["child"].(map[string]any)["name"] != "before" {
		t.Fatal("snapshot changed under a mutable view")
	}
}

func TestViewNeverLendsNativeLeaves(t *testing.T) {
	limits := value.Limits{}
	p, err := valueio.Capture(context.Background(), limits, map[string]any{"data": []byte{1, 2, 3}})
	if err != nil {
		t.Fatal(err)
	}
	for _, mutable := range []bool{false, true} {
		v, err := p.View(context.Background(), limits, mutable)
		if err != nil {
			t.Fatal(err)
		}
		if v.(map[string]any)["data"] != "AQID" {
			t.Fatalf("private byte carrier escaped: %#v", v)
		}
	}
	typed, err := valueio.Construct[struct {
		Data []byte `json:"data"`
	}](context.Background(), p, limits)
	if err != nil {
		t.Fatal(err)
	}
	if len(typed.Data) != 3 || typed.Data[0] != 1 {
		t.Fatalf("typed construction lost bytes: %v", typed.Data)
	}
}

type access = valueio.Access
type facade struct{ access }
type foreign struct{ *facade }

func TestPrivateAccessRejectsForeignWrapper(t *testing.T) {
	own := &facade{}
	ep := &valueio.Endpoint{}
	own.access = valueio.NewAccess(own, ep)
	if valueio.From(own) != ep {
		t.Fatal("owned endpoint unavailable")
	}
	if valueio.From(&foreign{own}) != nil {
		t.Fatal("foreign wrapper inherited bypass")
	}
}
