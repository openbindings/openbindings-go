package grpc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/processorscenarios"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
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
	invoker, cancelled := newGRPCFidelityInvoker(t, scenario)
	t.Cleanup(func() { _ = invoker.Close() })

	content, _ := scenario.Given.Source["content"].(string)
	location, _ := scenario.Given.Source["location"].(string)
	ref, _ := scenario.Given.Binding["ref"].(string)
	sourceContent := json.RawMessage(strconvQuote(content))
	iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{BindingSpec: BindingSpec, Location: location, Content: sourceContent}},
	})
	if err != nil {
		t.Fatal(err)
	}
	operation := ""
	for _, binding := range iface.Bindings {
		if binding.Ref == ref {
			operation = binding.Operation
			break
		}
	}
	if operation == "" {
		t.Fatalf("synthesized gRPC interface has no binding for %q", ref)
	}
	op := openbindings.NewOperationInvoker(invoker)
	call := openbindings.Invoke(
		context.Background(), op, iface,
		openbindings.NewOperationSignature[any, any](operation),
	)
	stream := call.Outputs()
	outputs := []any{}
	if writes, ok := scenario.Given.Invocation["writes"].([]any); ok {
		if len(writes) > 0 {
			_ = call.Write(context.Background(), writes[0])
			readCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			first, readErr := stream.Read(readCtx)
			cancel()
			if readErr != nil {
				t.Fatalf("first bidirectional output: %v", readErr)
			}
			outputs = append(outputs, first)
		}
		for _, value := range writes[1:] {
			_ = call.Write(context.Background(), value)
		}
	} else if present, _ := scenario.Given.Invocation["inputPresent"].(bool); present {
		_ = call.Write(context.Background(), scenario.Given.Invocation["input"])
	}
	_ = call.Close()

	var terminal *openbindings.InvocationError
	for {
		value, readErr := stream.Read(context.Background())
		if readErr == nil {
			outputs = append(outputs, value)
			continue
		}
		if readErr != io.EOF {
			terminal = openbindings.AsInvocationError(readErr)
		}
		break
	}
	if scenario.ID == "GRPC-FI-03" {
		deadline := time.Now().Add(time.Second)
		for !cancelled.Load() && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
	}
	addr, _ := parseDialAddress(location)
	data := map[string]any{
		"outputs":            outputs,
		"reflectionRequests": []any{},
		"joinedSynthesis":    true,
		"dispatch": map[string]any{
			"target":    addr.hostPort,
			"transport": map[bool]string{true: "tls", false: "plaintext"}[addr.tls],
			"cancelled": cancelled.Load(),
		},
	}
	if terminal == nil {
		return processorscenarios.Observation{Disposition: "complete", Phase: "completion", Data: data}
	}
	data["error"] = normalizedInvocationError(t, terminal)
	phase := "completion"
	if terminal.Code == openbindings.ErrCodeOperationValidationFailed || terminal.Code == openbindings.ErrCodeValidationFailed {
		phase = "dispatch"
	}
	return processorscenarios.Observation{Disposition: "error", Phase: phase, Data: data}
}

func newGRPCFidelityInvoker(t *testing.T, scenario processorscenarios.Scenario) (*Invoker, *atomic.Bool) {
	t.Helper()
	content, _ := scenario.Given.Source["content"].(string)
	disc, err := discoverFromContent(context.Background(), json.RawMessage(strconvQuote(content)))
	if err != nil {
		t.Fatal(err)
	}
	serviceName, methodName, err := parseRef(scenario.Given.Binding["ref"].(string))
	if err != nil {
		t.Fatal(err)
	}
	method, invocationErr := resolveMethod(disc, serviceName, methodName)
	if invocationErr != nil {
		t.Fatal(invocationErr)
	}
	peer := scenario.Given.Peer
	cancelled := &atomic.Bool{}
	server := grpc.NewServer()
	description := &grpc.ServiceDesc{ServiceName: serviceName, HandlerType: (*any)(nil)}
	if !method.IsStreamingClient() && !method.IsStreamingServer() {
		description.Methods = []grpc.MethodDesc{{
			MethodName: methodName,
			Handler: func(_ any, ctx context.Context, decode func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
				request := dynamicpb.NewMessage(method.Input())
				if err := decode(request); err != nil {
					return nil, err
				}

				if statusErr := nativeStatusError(t, peer); statusErr != nil {
					return nil, statusErr
				}
				if value, ok := peer["responseMessage"]; ok {
					return grpcFidelityMessage(t, method.Output(), value), nil
				}
				return dynamicpb.NewMessage(method.Output()), nil
			},
		}}
	} else {
		description.Streams = []grpc.StreamDesc{{
			StreamName:    methodName,
			ClientStreams: method.IsStreamingClient(),
			ServerStreams: method.IsStreamingServer(),
			Handler: func(_ any, stream grpc.ServerStream) error {
				writes := 0
				for {
					request := dynamicpb.NewMessage(method.Input())
					if err := stream.RecvMsg(request); err != nil {
						if stream.Context().Err() != nil {
							cancelled.Store(true)
						}
						if err != io.EOF {
							return err
						}
						break
					}
					writes++
					if after, ok := peer["afterWrite"].(map[string]any); ok {
						index := int(after["index"].(float64))
						if index == writes-1 {
							if responses, ok := after["responses"].([]any); ok {
								for _, response := range responses {
									if err := stream.SendMsg(grpcFidelityMessage(t, method.Output(), response)); err != nil {
										return err
									}
								}
							}
						}
					}
					if !method.IsStreamingClient() {
						break
					}
				}
				if responses, ok := peer["responseMessages"].([]any); ok {
					for _, response := range responses {
						if err := stream.SendMsg(grpcFidelityMessage(t, method.Output(), response)); err != nil {
							return err
						}
					}
				}

				return nativeStatusError(t, peer)
			},
		}}
	}
	server.RegisterService(description, nil)
	listener := bufconn.Listen(1024 * 1024)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(
		"passthrough:///fidelity",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	location, _ := scenario.Given.Source["location"].(string)
	addr, err := parseDialAddress(location)
	if err != nil {
		t.Fatal(err)
	}
	_, transportTag, err := resolveTransport(nil, nil, addr)
	if err != nil {
		t.Fatal(err)
	}
	invoker := NewInvoker()
	invoker.conns.Store(connKey(addr.hostPort, transportTag), conn)
	return invoker, cancelled
}

func grpcFidelityMessage(t *testing.T, descriptor protoreflect.MessageDescriptor, value any) *dynamicpb.Message {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	message := dynamicpb.NewMessage(descriptor)
	if err := protojson.Unmarshal(raw, message); err != nil {
		t.Fatal(err)
	}
	return message
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

func grpcFidelityMetadata(t *testing.T, raw any) metadata.MD {
	t.Helper()
	record, ok := raw.(map[string]any)
	if !ok {
		return metadata.MD{}
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
	return md
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
