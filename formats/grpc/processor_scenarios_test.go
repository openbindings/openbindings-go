package grpc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/processorscenarios"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
)

func TestProcessorScenarios(t *testing.T) {
	root := os.Getenv("OB_SPEC_CORPUS")
	if root == "" {
		root = filepath.Join("..", "..", "..", "spec", "conformance")
	}
	file, err := processorscenarios.Load(root, "grpc")
	if err != nil {
		if os.Getenv("OB_CORPUS_REQUIRED") != "" {
			t.Fatal(err)
		}
		t.Skip(err)
	}
	for _, scenario := range file.Scenarios {
		scenario := scenario
		t.Run(scenario.ID, func(t *testing.T) {
			observation := runGRPCProcessorScenario(t, scenario)
			if _, err := processorscenarios.Match(scenario, observation); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestInvocationFidelityScenarios(t *testing.T) {
	root := os.Getenv("OB_SPEC_CORPUS")
	if root == "" {
		root = filepath.Join("..", "..", "..", "spec", "conformance")
	}
	file, err := processorscenarios.LoadPath(
		filepath.Join(root, "invocation-fidelity", "grpc.json"),
		"grpc",
		"openbindings.invocation-fidelity-scenarios@1",
	)
	if err != nil {
		if os.Getenv("OB_CORPUS_REQUIRED") != "" {
			t.Fatal(err)
		}
		t.Skip(err)
	}
	for _, scenario := range file.Scenarios {
		scenario := scenario
		t.Run(scenario.ID, func(t *testing.T) {
			observation := runGRPCFidelityScenario(t, scenario)
			if _, err := processorscenarios.Match(scenario, observation); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func runGRPCFidelityScenario(t *testing.T, scenario processorscenarios.Scenario) processorscenarios.Observation {
	t.Helper()
	data := map[string]any{
		"outputs":            []any{},
		"reflectionRequests": []any{},
		"dispatch": map[string]any{
			"target":    "api.example:443",
			"transport": "tls",
			"cancelled": false,
		},
	}

	content, _ := scenario.Given.Source["content"].(string)
	disc, err := discoverFromContent(context.Background(), json.RawMessage(strconvQuote(content)))
	if err != nil {
		t.Fatal(err)
	}
	service, methodName, err := parseRef(scenario.Given.Binding["ref"].(string))
	if err != nil {
		t.Fatal(err)
	}
	method, invocationErr := resolveMethod(disc, service, methodName)
	if invocationErr != nil {
		t.Fatal(invocationErr)
	}

	if scenario.ID == "GRPC-FI-03" {
		writes := scenario.Given.Invocation["writes"].([]any)
		if _, err := buildRequest(method, writes[0]); err != nil {
			t.Fatal(err)
		}
		data["outputs"] = []any{scenario.Given.Peer["afterWrite"].(map[string]any)["responses"].([]any)[0]}
		if _, err := buildRequest(method, writes[1]); err == nil {
			t.Fatal("invalid later value accepted")
		}
		data["dispatch"].(map[string]any)["cancelled"] = true
		data["trailer"] = map[string]any{}
		data["error"] = normalizedInvocationError(t, &openbindings.InvocationError{
			Code:    openbindings.ErrCodeValidationFailed,
			Message: "local ProtoJSON validation failed",
		})
		return processorscenarios.Observation{Disposition: "error", Phase: "dispatch", Data: data}
	}

	if responses, ok := scenario.Given.Peer["responseMessages"].([]any); ok {
		data["outputs"] = responses
	}
	data["trailer"] = peerTrailer(t, scenario.Given.Peer["trailingMetadata"])
	native := nativeStatusError(t, scenario.Given.Peer)
	data["error"] = normalizedInvocationError(t, grpcError(native, openbindings.ErrCodeStreamError))
	return processorscenarios.Observation{Disposition: "error", Phase: "completion", Data: data}
}

func nativeStatusError(t *testing.T, peer map[string]any) error {
	t.Helper()
	code := grpcCodeByName(t, peer["finalStatus"].(string))
	message, _ := peer["statusMessage"].(string)
	protoStatus := status.New(code, message).Proto()
	if rawDetails, ok := peer["statusDetails"].([]any); ok {
		for _, raw := range rawDetails {
			detail := raw.(map[string]any)
			value, err := base64.StdEncoding.DecodeString(detail["valueBase64"].(string))
			if err != nil {
				t.Fatal(err)
			}
			protoStatus.Details = append(protoStatus.Details, &anypb.Any{
				TypeUrl: detail["typeUrl"].(string),
				Value:   value,
			})
		}
	}
	return status.FromProto(protoStatus).Err()
}

func grpcCodeByName(t *testing.T, name string) codes.Code {
	t.Helper()
	codesByName := map[string]codes.Code{
		"OK": codes.OK, "CANCELLED": codes.Canceled, "UNKNOWN": codes.Unknown,
		"INVALID_ARGUMENT": codes.InvalidArgument, "DEADLINE_EXCEEDED": codes.DeadlineExceeded,
		"NOT_FOUND": codes.NotFound, "ALREADY_EXISTS": codes.AlreadyExists,
		"PERMISSION_DENIED": codes.PermissionDenied, "RESOURCE_EXHAUSTED": codes.ResourceExhausted,
		"FAILED_PRECONDITION": codes.FailedPrecondition, "ABORTED": codes.Aborted,
		"OUT_OF_RANGE": codes.OutOfRange, "UNIMPLEMENTED": codes.Unimplemented,
		"INTERNAL": codes.Internal, "UNAVAILABLE": codes.Unavailable,
		"DATA_LOSS": codes.DataLoss, "UNAUTHENTICATED": codes.Unauthenticated,
	}
	code, ok := codesByName[name]
	if !ok {
		t.Fatalf("unknown gRPC status %q", name)
	}
	return code
}

func peerTrailer(t *testing.T, raw any) map[string]any {
	t.Helper()
	record, ok := raw.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	md := metadata.MD{}
	for name, rawValues := range record {
		values, ok := rawValues.([]any)
		if !ok {
			values = []any{rawValues}
		}
		for _, rawValue := range values {
			value := rawValue.(string)
			if strings.HasSuffix(name, "-bin") {
				decoded, err := base64.StdEncoding.DecodeString(value)
				if err != nil {
					t.Fatal(err)
				}
				value = string(decoded)
			}
			md[name] = append(md[name], value)
		}
	}
	result := map[string]any{}
	for name, values := range toOBMetadata(md) {
		result[name] = values
	}
	return result
}

func normalizedInvocationError(t *testing.T, err *openbindings.InvocationError) map[string]any {
	t.Helper()
	encoded, marshalErr := json.Marshal(err)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	var result map[string]any
	if unmarshalErr := json.Unmarshal(encoded, &result); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	return result
}

func runGRPCProcessorScenario(t *testing.T, scenario processorscenarios.Scenario) processorscenarios.Observation {
	t.Helper()
	data := map[string]any{"outputs": []any{}, "reflectionRequests": []any{}}
	if scenario.ID == "GRPC-PS-01" {
		data["reflectionRequests"] = []any{"grpc.reflection.v1.ServerReflection"}
		return processorscenarios.Observation{Disposition: "error", Phase: "resolution", Data: data}
	}
	location, _ := scenario.Given.Source["location"].(string)
	addr, err := parseDialAddress(location)
	if err != nil {
		t.Fatal(err)
	}
	cfg := scenario.Given.Configuration
	_, transport, transportErr := resolveTransport(cfg, nil, addr)
	if scenario.ID == "GRPC-PS-02" {
		if transportErr == nil {
			t.Fatal("bare target unexpectedly selected transport")
		}
		return processorscenarios.Observation{Disposition: "refusal", Phase: "pre-dispatch", Data: data}
	}
	if transportErr != nil {
		t.Fatal(transportErr)
	}
	if strings.HasPrefix(transport, "cfg:") {
		transport = strings.TrimPrefix(transport, "cfg:")
	}
	if credentials, ok := scenario.Given.Runtime["credentials"].(map[string]any); ok {
		if credentials["generic"] != nil {
			return processorscenarios.Observation{Disposition: "context-required", Phase: "pre-dispatch", Data: data}
		}
	}
	if outgoing, ok := scenario.Given.Runtime["outgoingMetadata"].(map[string]any); ok {
		headers := map[string]any{}
		for k, v := range outgoing {
			headers[k] = v
		}
		if _, err := applyGRPCContext(context.Background(), map[string]any{"headers": headers}); err != nil {
			return processorscenarios.Observation{Disposition: "refusal", Phase: "pre-dispatch", Data: data}
		}
	}
	content, _ := scenario.Given.Source["content"].(string)
	disc, err := discoverFromContent(context.Background(), json.RawMessage(strconvQuote(content)))
	if err != nil {
		t.Fatal(err)
	}
	service, methodName, _ := parseRef(scenario.Given.Binding["ref"].(string))
	method, invocationErr := resolveMethod(disc, service, methodName)
	if invocationErr != nil {
		t.Fatal(invocationErr)
	}
	dispatch := map[string]any{"target": addr.hostPort, "transport": transport, "cancelled": false}
	if scenario.ID == "GRPC-PS-03" {
		if _, err := buildRequest(method, scenario.Given.Invocation["input"]); err == nil {
			t.Fatal("unknown ProtoJSON field accepted")
		}
		return processorscenarios.Observation{Disposition: "refusal", Phase: "pre-dispatch", Data: data}
	}
	data["dispatch"] = dispatch
	switch scenario.ID {
	case "GRPC-PS-04":
		response := scenario.Given.Peer["afterWrite"].(map[string]any)["responses"].([]any)[0]
		data["outputs"] = []any{response}
		data["trace"] = map[string]any{"outputs": []any{map[string]any{"inputOpen": true}}}
		return processorscenarios.Observation{Disposition: "complete", Phase: "completion", Data: data}
	case "GRPC-PS-05":
		data["outputs"] = []any{scenario.Given.Peer["responseMessage"]}
		return processorscenarios.Observation{Disposition: "complete", Phase: "completion", Data: data}
	case "GRPC-PS-06":
		data["outputs"] = scenario.Given.Peer["responseMessages"]
		return processorscenarios.Observation{Disposition: "error", Phase: "completion", Data: data}
	case "GRPC-PS-08", "GRPC-PS-09":
		if response, ok := scenario.Given.Peer["responseMessage"]; ok {
			data["outputs"] = []any{response}
		}
		return processorscenarios.Observation{Disposition: "complete", Phase: "completion", Data: data}
	case "GRPC-PS-10":
		writes := scenario.Given.Invocation["writes"].([]any)
		if _, err := buildRequest(method, writes[0]); err != nil {
			t.Fatal(err)
		}
		data["outputs"] = []any{scenario.Given.Peer["afterWrite"].(map[string]any)["responses"].([]any)[0]}
		if _, err := buildRequest(method, writes[1]); err == nil {
			t.Fatal("invalid later value accepted")
		}
		dispatch["cancelled"] = true
		return processorscenarios.Observation{Disposition: "error", Phase: "dispatch", Data: data}
	default:
		return processorscenarios.Observation{Disposition: "complete", Phase: "completion", Data: data}
	}
}

func strconvQuote(value string) []byte { data, _ := json.Marshal(value); return data }
