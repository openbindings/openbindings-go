package openbindings

import "testing"

type sigUserIn struct {
	ID string `json:"id"`
}

type sigUserOut struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// A typed signature yields a handle typed by its I/O; the output decodes into
// the concrete struct at the typed boundary.
func TestSignatureInvokeTyped(t *testing.T) {
	op := newOpInvoker(&mockBindingInvoker{}, nil)
	sig := NewOperationSignature[sigUserIn, sigUserOut]("getUser")

	call := Invoke(bg(), op, opTestInterface(), sig)
	if err := call.Write(bg(), sigUserIn{ID: "u1"}); err != nil {
		t.Fatal(err)
	}
	out, err := Single(shortCtx(t), call.Outputs())
	if err != nil {
		t.Fatal(err)
	}
	if out.ID != "u1" || out.Name != "Ada" {
		t.Fatalf("got %+v", out)
	}
}

// The signature's key resolves alias-aware at invoke time (OBI-T-12): the
// signature carries no interface, so the same value works against any obi that
// declares the key (or an alias of it).
func TestSignatureInvokeAlias(t *testing.T) {
	op := newOpInvoker(&mockBindingInvoker{}, nil)
	sig := NewOperationSignature[sigUserIn, sigUserOut]("fetchUser") // alias of getUser

	call := Invoke(bg(), op, opTestInterface(), sig)
	if err := call.Write(bg(), sigUserIn{ID: "u1"}); err != nil {
		t.Fatal(err)
	}
	out, err := Single(shortCtx(t), call.Outputs())
	if err != nil {
		t.Fatal(err)
	}
	if out.Name != "Ada" {
		t.Fatalf("got %+v", out)
	}
}

// The untyped flavor is the same type instantiated with [any, any], built from a
// string. The call site is identical to the typed one; the result is
// *TypedInvocation[any, any].
func TestSignatureInvokeUntyped(t *testing.T) {
	op := newOpInvoker(&mockBindingInvoker{}, nil)
	sig := NewOperationSignature[any, any]("ping")

	call := Invoke(bg(), op, opTestInterface(), sig)
	v, err := Single(shortCtx(t), call.Outputs())
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := v.(map[string]any)["ok"].(bool); !ok {
		t.Fatalf("got %v", v)
	}
}

// WithBindingKey bypasses selection and routes to the named binding.
// getUser.bad emits a shape that fails the operation's OBI-T-08 output schema,
// so routing there surfaces a validation error the preferred binding
// (getUser.main, which produces name "Ada") would not.
func TestSignatureInvokeBindingKeyOverride(t *testing.T) {
	op := newOpInvoker(&mockBindingInvoker{}, nil)
	sig := NewOperationSignature[sigUserIn, sigUserOut]("getUser")

	call := Invoke(bg(), op, opTestInterface(), sig, WithBindingKey("getUser.bad"))
	if err := call.Write(bg(), sigUserIn{ID: "u1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Single(shortCtx(t), call.Outputs()); err == nil {
		t.Fatal("expected getUser.bad's invalid output to surface an error; BindingKey may not have routed")
	}
}
