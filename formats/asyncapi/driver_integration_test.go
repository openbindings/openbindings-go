package asyncapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	asyncapiclient "github.com/openbindings/asyncapi-client/go"
	kafkadriver "github.com/openbindings/asyncapi-client/go/kafka"
	mqttdriver "github.com/openbindings/asyncapi-client/go/mqtt"
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
  "defaultContentType":"application/json",
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

// TestLiveOpenBindingsAdapterUsesMQTTDriver pins the complete abstraction
// boundary against a real broker: the caller supplies only an operation ref,
// an application value, and portable context. MQTT topics, QoS, retain, and
// authentication are interpreted below the OpenBindings invocation surface.
func TestLiveOpenBindingsAdapterUsesMQTTDriver(t *testing.T) {
	brokerURL := os.Getenv("ASYNCAPI_MQTT_TEST_URL")
	if brokerURL == "" {
		t.Skip("ASYNCAPI_MQTT_TEST_URL is not set")
	}
	parsed, err := url.Parse(brokerURL)
	if err != nil {
		t.Fatal(err)
	}
	artifact := mqttBridgeArtifact(t, parsed.Host)
	invoker, err := NewInvokerWithDrivers(nil, mqttdriver.New(mqttdriver.Options{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = invoker.Close() }()
	args := func(ref string) *openbindings.BindingInvocationArgs {
		return &openbindings.BindingInvocationArgs{
			Source:  openbindings.InvocationSource{BindingSpec: BindingSpec, Content: artifact},
			Ref:     ref,
			Context: map[string]any{"basic": map[string]any{"username": "sensor", "password": "secret"}},
		}
	}

	// Retaining the publish makes this deterministic without exposing or
	// polling protocol readiness through the abstract invocation handle.
	publish := invoker.InvokeBinding(context.Background(), args("#/operations/publish"))
	if err := publish.Write(context.Background(), map[string]any{"id": "through-openbindings"}); err != nil {
		t.Fatal(err)
	}
	_ = publish.Close()
	if _, err := publish.Outputs().Read(context.Background()); err != io.EOF {
		t.Fatalf("publish completion = %v", err)
	}

	subscribe := invoker.InvokeBinding(context.Background(), args("#/operations/observe"))
	outputs := subscribe.Outputs()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := outputs.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	value, ok := output.(map[string]any)
	if !ok || value["id"] != "through-openbindings" {
		t.Fatalf("output = %#v", output)
	}
	outputs.Stop()
}

func TestLiveOpenBindingsAdapterPreservesMQTTOutputBeforeConnectionLoss(t *testing.T) {
	brokerURL := os.Getenv("ASYNCAPI_MQTT_TEST_URL")
	if brokerURL == "" {
		t.Skip("ASYNCAPI_MQTT_TEST_URL is not set")
	}
	parsed, err := url.Parse(brokerURL)
	if err != nil {
		t.Fatal(err)
	}
	artifact := mqttBridgeArtifactAt(t, parsed.Host, "failure/{tenant}")
	invoker, err := NewInvokerWithDrivers(nil, mqttdriver.New(mqttdriver.Options{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = invoker.Close() }()
	call := invoker.InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source:  openbindings.InvocationSource{BindingSpec: BindingSpec, Content: artifact},
		Ref:     "#/operations/observe",
		Context: map[string]any{"basic": map[string]any{"username": "sensor", "password": "secret"}},
	})
	outputs := call.Outputs()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := outputs.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	value, ok := output.(map[string]any)
	if !ok || value["id"] != "before-disconnect" {
		t.Fatalf("output = %#v", output)
	}
	if _, err := outputs.Read(ctx); err == nil || !strings.Contains(strings.ToLower(err.Error()), "mqtt connection lost") {
		t.Fatalf("terminal error = %v", err)
	}
}

// TestLiveOpenBindingsAdapterUsesKafkaDriver pins the complete Kafka
// abstraction boundary. The OpenBindings caller sees only an operation ref
// and application values; topic, key, group, client identity, partitions,
// and broker protocol remain below the adapter.
func TestLiveOpenBindingsAdapterUsesKafkaDriver(t *testing.T) {
	brokerURL := os.Getenv("ASYNCAPI_KAFKA_TEST_URL")
	topic := os.Getenv("ASYNCAPI_KAFKA_TEST_TOPIC_GO_BRIDGE")
	if brokerURL == "" || topic == "" {
		t.Skip("ASYNCAPI_KAFKA_TEST_URL and ASYNCAPI_KAFKA_TEST_TOPIC_GO_BRIDGE are not set")
	}
	parsed, err := url.Parse(brokerURL)
	if err != nil {
		t.Fatal(err)
	}
	artifact := kafkaBridgeArtifact(t, parsed.Host, topic, "ob-kafka-go-bridge")
	invoker, err := NewInvokerWithDrivers(nil, kafkadriver.New(kafkadriver.Options{FromBeginning: true}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = invoker.Close() }()
	args := func(ref string) *openbindings.BindingInvocationArgs {
		return &openbindings.BindingInvocationArgs{
			Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: artifact},
			Ref:    ref,
		}
	}

	publish := invoker.InvokeBinding(context.Background(), args("#/operations/publish"))
	if err := publish.Write(context.Background(), map[string]any{"id": "through-openbindings-kafka"}); err != nil {
		t.Fatal(err)
	}
	_ = publish.Close()
	if _, err := publish.Outputs().Read(context.Background()); err != io.EOF {
		t.Fatalf("publish completion = %v", err)
	}

	subscribe := invoker.InvokeBinding(context.Background(), args("#/operations/observe"))
	outputs := subscribe.Outputs()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := outputs.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	value, ok := output.(map[string]any)
	if !ok || value["id"] != "through-openbindings-kafka" {
		t.Fatalf("output = %#v", output)
	}
	outputs.Stop()
}

func TestLiveOpenBindingsAdapterUsesKafkaSCRAMWithoutProtocolFields(t *testing.T) {
	brokerURL := os.Getenv("ASYNCAPI_KAFKA_TEST_URL")
	topic := os.Getenv("ASYNCAPI_KAFKA_TEST_TOPIC_GO_BRIDGE_SECURITY")
	if brokerURL == "" || topic == "" {
		t.Skip("Kafka bridge SCRAM qualification environment is not set")
	}
	parsed, err := url.Parse(brokerURL)
	if err != nil {
		t.Fatal(err)
	}
	artifact := kafkaBridgeArtifactWithSecurity(t, parsed.Host, topic, "ob-kafka-go-bridge-security", true)
	invoker, err := NewInvokerWithDrivers(nil, kafkadriver.New(kafkadriver.Options{FromBeginning: true}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = invoker.Close() }()
	args := func(ref string) *openbindings.BindingInvocationArgs {
		return &openbindings.BindingInvocationArgs{
			Source:  openbindings.InvocationSource{BindingSpec: BindingSpec, Content: artifact},
			Ref:     ref,
			Context: map[string]any{"basic": map[string]any{"username": "orders", "password": "secret-password"}},
		}
	}
	publish := invoker.InvokeBinding(context.Background(), args("#/operations/publish"))
	if err := publish.Write(context.Background(), map[string]any{"id": "secured-through-openbindings"}); err != nil {
		t.Fatal(err)
	}
	_ = publish.Close()
	if _, err := publish.Outputs().Read(context.Background()); err != io.EOF {
		t.Fatalf("publish completion = %v", err)
	}
	subscribe := invoker.InvokeBinding(context.Background(), args("#/operations/observe"))
	outputs := subscribe.Outputs()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := outputs.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	value, ok := output.(map[string]any)
	if !ok || value["id"] != "secured-through-openbindings" {
		t.Fatalf("output = %#v", output)
	}
	outputs.Stop()
}

func mqttBridgeArtifact(t *testing.T, host string) json.RawMessage {
	return mqttBridgeArtifactAt(t, host, "events/{tenant}")
}

func mqttBridgeArtifactAt(t *testing.T, host, address string) json.RawMessage {
	t.Helper()
	document := map[string]any{
		"asyncapi":           "3.0.0",
		"info":               map[string]any{"title": "MQTT bridge qualification", "version": "1"},
		"defaultContentType": "application/json",
		"servers": map[string]any{"production": map[string]any{
			"host": host, "protocol": "mqtt", "protocolVersion": "3.1.1",
			"security": []any{map[string]any{"type": "userPassword"}},
			"bindings": map[string]any{"mqtt": map[string]any{
				"clientId": "ob-mqtt-bridge-test", "cleanSession": true, "keepAlive": 15, "bindingVersion": "0.2.0",
			}},
		}},
		"channels": map[string]any{"events": map[string]any{
			"address":    address,
			"parameters": map[string]any{"tenant": map[string]any{"default": "acme"}},
			"messages": map[string]any{"Event": map[string]any{
				"contentType": "application/json",
				"payload": map[string]any{"type": "object", "required": []any{"id"}, "properties": map[string]any{
					"id": map[string]any{"type": "string"},
				}},
			}},
		}},
		"operations": map[string]any{
			"publish": map[string]any{
				"action": "receive", "channel": map[string]any{"$ref": "#/channels/events"},
				"messages": []any{map[string]any{"$ref": "#/channels/events/messages/Event"}},
				"bindings": map[string]any{"mqtt": map[string]any{"qos": 1, "retain": true, "bindingVersion": "0.2.0"}},
			},
			"observe": map[string]any{
				"action": "send", "channel": map[string]any{"$ref": "#/channels/events"},
				"messages": []any{map[string]any{"$ref": "#/channels/events/messages/Event"}},
				"bindings": map[string]any{"mqtt": map[string]any{"qos": 1, "bindingVersion": "0.2.0"}},
			},
		},
	}
	content, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return openbindings.TextContent(string(content))
}

func kafkaBridgeArtifact(t *testing.T, host, topic, groupID string) json.RawMessage {
	return kafkaBridgeArtifactWithSecurity(t, host, topic, groupID, false)
}

func kafkaBridgeArtifactWithSecurity(t *testing.T, host, topic, groupID string, secured bool) json.RawMessage {
	t.Helper()
	var security []any
	if secured {
		security = []any{map[string]any{"type": "scramSha256"}}
	}
	document := map[string]any{
		"asyncapi":           "3.0.0",
		"info":               map[string]any{"title": "Kafka bridge qualification", "version": "1"},
		"defaultContentType": "application/json",
		"servers": map[string]any{"production": map[string]any{
			"host": host, "protocol": "kafka", "security": security, "bindings": map[string]any{"kafka": map[string]any{"bindingVersion": "0.5.0"}},
		}},
		"channels": map[string]any{"events": map[string]any{
			"address": "orders/{tenant}", "parameters": map[string]any{"tenant": map[string]any{"default": "acme"}},
			"bindings": map[string]any{"kafka": map[string]any{"topic": topic, "partitions": 3, "replicas": 1, "bindingVersion": "0.5.0"}},
			"messages": map[string]any{"Event": map[string]any{
				"contentType": "application/json",
				"payload":     map[string]any{"type": "object", "required": []any{"id"}, "properties": map[string]any{"id": map[string]any{"type": "string"}}},
				"bindings":    map[string]any{"kafka": map[string]any{"key": map[string]any{"type": "string", "const": "tenant-a"}, "bindingVersion": "0.5.0"}},
			}},
		}},
		"operations": map[string]any{
			"publish": map[string]any{
				"action": "receive", "channel": map[string]any{"$ref": "#/channels/events"}, "messages": []any{map[string]any{"$ref": "#/channels/events/messages/Event"}},
				"bindings": map[string]any{"kafka": map[string]any{"clientId": map[string]any{"type": "string", "const": "ob-kafka-go-bridge-producer"}, "bindingVersion": "0.5.0"}},
			},
			"observe": map[string]any{
				"action": "send", "channel": map[string]any{"$ref": "#/channels/events"}, "messages": []any{map[string]any{"$ref": "#/channels/events/messages/Event"}},
				"bindings": map[string]any{"kafka": map[string]any{
					"clientId": map[string]any{"type": "string", "const": "ob-kafka-go-bridge-consumer"},
					"groupId":  map[string]any{"type": "string", "const": groupID}, "bindingVersion": "0.5.0",
				}},
			},
		},
	}
	content, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return openbindings.TextContent(string(content))
}
