package openapi

import (
	"context"
	"testing"

	openapiclient "github.com/openbindings/openapi-client/go"
)

func TestNativeServerSelectionAcceptsConfigValueURLShape(t *testing.T) {
	const artifact = `{
		"openapi":"3.1.0",
		"info":{"title":"servers","version":"1"},
		"servers":[{"url":"https://one.example"},{"url":"https://two.example"}],
		"paths":{"/data":{"get":{"operationId":"read","responses":{"204":{"description":"ok"}}}}}
	}`
	client, err := openapiclient.Load(context.Background(), openapiclient.Source{Content: []byte(artifact)}, openapiclient.Options{})
	if err != nil {
		t.Fatalf("load client: %v", err)
	}
	selector := openapiclient.OperationRef("#/paths/~1data/get")
	selection, err := nativeServerSelection(map[string]any{
		"server": map[string]any{"url": "https://two.example"},
	}, client, selector)
	if err != nil {
		t.Fatalf("map config.value server URL: %v", err)
	}
	requirements, err := client.Preflight(context.Background(), selector, openapiclient.Input{}, openapiclient.CallOptions{Server: selection})
	if err != nil {
		t.Fatalf("preflight selected server: %v", err)
	}
	if requirements != nil && len(requirements.Alternatives) != 0 {
		t.Fatalf("selected server still requires configuration: %#v", requirements)
	}
}
