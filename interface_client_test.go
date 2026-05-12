package openbindings

import (
	"testing"
)

func makeTestInterface(name string, ops ...string) *Interface {
	iface := &Interface{
		OpenBindings: "0.1.0",
		Name:         name,
		Operations:   map[string]Operation{},
	}
	for _, op := range ops {
		iface.Operations[op] = Operation{}
	}
	return iface
}

func TestNewInterfaceClient_HoldsProvidedInterface(t *testing.T) {
	iface := makeTestInterface("svc", "ping", "pong")
	invoker := NewOperationInvoker()
	client := NewInterfaceClient(iface, invoker)

	if client.Interface() != iface {
		t.Error("Interface() must return the iface passed to the constructor")
	}
}

func TestNewInterfaceClient_PanicsOnNilInterface(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on nil iface")
		}
	}()
	NewInterfaceClient(nil, NewOperationInvoker())
}

func TestNewInterfaceClient_InterfaceJSONSerializesProvidedInterface(t *testing.T) {
	iface := makeTestInterface("svc", "ping")
	invoker := NewOperationInvoker()
	client := NewInterfaceClient(iface, invoker)

	js := client.InterfaceJSON()
	if js == "" || js == "null" {
		t.Errorf("InterfaceJSON should serialize the iface, got %q", js)
	}
}
