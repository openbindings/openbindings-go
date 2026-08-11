package asyncapi

import (
	"context"
	"errors"
	"io"
	"testing"

	asyncapiclient "github.com/openbindings/asyncapi-client/go"
	openbindings "github.com/openbindings/openbindings-go"
)

type bridgeTestDriver struct{ seen []any }

func (d *bridgeTestDriver) Protocols() []string { return []string{"mqtt"} }

func (d *bridgeTestDriver) Execute(ctx context.Context, request asyncapiclient.DriverRequest, session asyncapiclient.DriverSession) error {
	if request.Protocol != "mqtt" || request.Ref != "#/operations/publish" {
		return errors.New("adapter did not preserve the artifact target")
	}
	value, err := session.Receive(ctx)
	if err != nil {
		return err
	}
	d.seen = append(d.seen, value)
	if err := session.CloseInput(); err != nil {
		return err
	}
	return session.Emit(map[string]any{"accepted": true})
}

func TestOpenBindingsAdapterDelegatesArbitraryProtocolDriver(t *testing.T) {
	driver := &bridgeTestDriver{}
	invoker, err := NewInvokerWithDrivers(nil, driver)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = invoker.Close() }()
	artifact := openbindings.TextContent(`{
  "asyncapi":"3.1.0",
  "info":{"title":"MQTT bridge","version":"1"},
  "servers":{"broker":{"host":"broker.example","protocol":"mqtt"}},
  "channels":{"commands":{"address":"commands","messages":{"command":{"payload":{"type":"object"}}}}},
  "operations":{"publish":{"action":"receive","channel":{"$ref":"#/channels/commands"},"messages":[{"$ref":"#/channels/commands/messages/command"}]}}
}`)
	call := invoker.InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: artifact},
		Ref:    "#/operations/publish",
	})
	if err := call.Write(context.Background(), map[string]any{"id": float64(7)}); err != nil {
		t.Fatal(err)
	}
	_ = call.Close()
	outputs := call.Outputs()
	output, err := outputs.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if value, ok := output.(map[string]any); !ok || value["accepted"] != true {
		t.Fatalf("output = %#v", output)
	}
	if _, err := outputs.Read(context.Background()); err != io.EOF {
		t.Fatalf("completion = %v", err)
	}
	if len(driver.seen) != 1 {
		t.Fatalf("driver inputs = %#v", driver.seen)
	}
}
