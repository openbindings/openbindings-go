package grpc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/openbindings/openbindings-go/invoke"
	"github.com/openbindings/openbindings-go/synthesize"

	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// TestNativeDifferential_InvocationFidelity is the first gRPC native-oracle
// slice: discover a brownfield server through reflection, invoke the selectors in
// the synthesized OBI, and compare caller-visible values and lifecycle with
// direct grpc-go calls against the same service. Concrete status evidence is
// checked only through diagnostics; correct application behavior depends on
// the protocol-independent unsuccessful-completion signal.
func TestNativeDifferential_InvocationFidelity(t *testing.T) {
	const differentialLocation = "grpc://127.0.0.1:50051"
	dialer, _ := setupTestServer(t)
	nativeConn := dialTestServer(t, dialer)
	invoker := newTestInvoker(t, dialer)
	// The synthesizer uses a resolver-valid loopback address while the custom
	// dialer still targets bufconn. Make the invocation lane reuse that same
	// in-memory channel under the synthesized source identity.
	invoker.conns.Store(connKey("127.0.0.1:50051", "cfg:plaintext"), nativeConn)
	defer invoker.Close()

	iface, err := NewSynthesizer(
		WithSynthesizerTransportCredentials(insecure.NewCredentials()),
		WithSynthesizerDialOptions(grpcgo.WithContextDialer(dialer)),
	).SynthesizeInterface(testCtx(t), &synthesize.SynthesizeInput{
		Sources: []synthesize.SynthesizeSource{{BindingSpec: BindingSpec, Location: differentialLocation}},
	})
	if err != nil {
		t.Fatalf("synthesize reflected server: %v", err)
	}
	refFor := func(operation string) string {
		t.Helper()
		binding, ok := iface.Bindings[operation+"."+DefaultSourceName]
		if !ok {
			t.Fatalf("synthesized OBI has no binding for %s", operation)
		}
		return binding.Selector
	}
	argsFor := func(selector string) *invoke.BindingInvocationArgs {
		args := bufconnArgs(selector, nil)
		args.Source.Location = differentialLocation
		return args
	}

	_, fd := testItemsRegistry(t)
	service := fd.Services().ByName("ItemService")
	request := func(method string) protoreflect.MessageDescriptor {
		t.Helper()
		return service.Methods().ByName(protoreflect.Name(method)).Input()
	}
	response := func(method string) protoreflect.MessageDescriptor {
		t.Helper()
		return service.Methods().ByName(protoreflect.Name(method)).Output()
	}

	t.Run("unary value and clean completion", func(t *testing.T) {
		selector := refFor("GetItem")
		nativeReq := dynamicpb.NewMessage(request("GetItem"))
		nativeReq.Set(request("GetItem").Fields().ByName("id"), protoreflect.ValueOfString("42"))
		nativeResp := dynamicpb.NewMessage(response("GetItem"))
		if err := nativeConn.Invoke(testCtx(t), "/"+selector, nativeReq, nativeResp); err != nil {
			t.Fatalf("native invoke: %v", err)
		}

		inv := invoker.InvokeBinding(testCtx(t), argsFor(selector))
		if err := inv.Write(testCtx(t), map[string]any{"id": "42"}); err != nil {
			t.Fatalf("OpenBindings write: %v", err)
		}
		values, terminal := drainInvocation(t, inv)
		if terminal != nil || len(values) != 1 {
			t.Fatalf("OpenBindings unary completion = %d values, %v", len(values), terminal)
		}
		if want := protoMessageMap(t, nativeResp); !reflect.DeepEqual(values[0], want) {
			t.Fatalf("OpenBindings value = %#v, native value = %#v", values[0], want)
		}
	})

	t.Run("server stream ordering and cardinality", func(t *testing.T) {
		selector := refFor("ListItems")
		nativeValues, nativeErr := nativeServerStream(
			t, nativeConn, selector, request("ListItems"), response("ListItems"), nil,
		)
		if nativeErr != nil {
			t.Fatalf("native stream: %v", nativeErr)
		}

		inv := invoker.InvokeBinding(testCtx(t), argsFor(selector))
		values, terminal := drainInvocation(t, inv)
		if terminal != nil {
			t.Fatalf("OpenBindings stream terminal: %v", terminal)
		}
		if !reflect.DeepEqual(values, nativeValues) {
			t.Fatalf("OpenBindings stream = %#v, native stream = %#v", values, nativeValues)
		}
	})

	t.Run("partial outputs survive unsuccessful completion", func(t *testing.T) {
		selector := refFor("FailItems")
		nativeValues, nativeErr := nativeServerStream(
			t, nativeConn, selector, request("FailItems"), response("FailItems"), nil,
		)
		if status.Code(nativeErr) != codes.ResourceExhausted {
			t.Fatalf("native terminal = %v, want ResourceExhausted", nativeErr)
		}

		inv := invoker.InvokeBinding(testCtx(t), argsFor(selector))
		values, terminal := drainInvocation(t, inv)
		if !reflect.DeepEqual(values, nativeValues) {
			t.Fatalf("OpenBindings partial values = %#v, native partial values = %#v", values, nativeValues)
		}
		if terminal == nil || terminal.Code != invoke.ErrCodeExecutionFailed {
			t.Fatalf("OpenBindings terminal = %v, want protocol-independent unsuccessful completion", terminal)
		}
		if terminal.HasData() {
			t.Fatalf("native status leaked as abstract data: %#v", terminal.Data)
		}
	})

	t.Run("caller cancellation after a partial output", func(t *testing.T) {
		selector := refFor("WatchItems")
		nativeCtx, nativeCancel := context.WithCancel(testCtx(t))
		nativeStream, err := nativeConn.NewStream(nativeCtx, &grpcgo.StreamDesc{ServerStreams: true}, "/"+selector)
		if err != nil {
			t.Fatalf("native stream: %v", err)
		}
		if err := nativeStream.SendMsg(dynamicpb.NewMessage(request("WatchItems"))); err != nil {
			t.Fatalf("native send: %v", err)
		}
		if err := nativeStream.CloseSend(); err != nil {
			t.Fatalf("native half-close: %v", err)
		}
		nativeFirst := dynamicpb.NewMessage(response("WatchItems"))
		if err := nativeStream.RecvMsg(nativeFirst); err != nil {
			t.Fatalf("native first output: %v", err)
		}
		nativeCancel()
		nativeTail := dynamicpb.NewMessage(response("WatchItems"))
		if err := nativeStream.RecvMsg(nativeTail); status.Code(err) != codes.Canceled {
			t.Fatalf("native cancellation terminal = %v", err)
		}

		inv := invoker.InvokeBinding(testCtx(t), argsFor(selector))
		outputs := inv.Outputs()
		first, err := outputs.Read(testCtx(t))
		if err != nil {
			t.Fatalf("OpenBindings first output: %v", err)
		}
		if want := protoMessageMap(t, nativeFirst); !reflect.DeepEqual(first, want) {
			t.Fatalf("OpenBindings first = %#v, native first = %#v", first, want)
		}
		inv.Cancel()
		_, err = outputs.Read(testCtx(t))
		var terminal *invoke.InvocationError
		if !errors.As(err, &terminal) || terminal.Code != invoke.ErrCodeCancelled {
			t.Fatalf("OpenBindings cancellation terminal = %v", err)
		}
	})
}

func nativeServerStream(
	t *testing.T,
	conn *grpcgo.ClientConn,
	selector string,
	requestDesc protoreflect.MessageDescriptor,
	responseDesc protoreflect.MessageDescriptor,
	populate func(protoreflect.Message),
) ([]any, error) {
	t.Helper()
	stream, err := conn.NewStream(testCtx(t), &grpcgo.StreamDesc{ServerStreams: true}, "/"+selector)
	if err != nil {
		return nil, err
	}
	req := dynamicpb.NewMessage(requestDesc)
	if populate != nil {
		populate(req)
	}
	if err := stream.SendMsg(req); err != nil {
		return nil, err
	}
	if err := stream.CloseSend(); err != nil {
		return nil, err
	}
	var values []any
	for {
		msg := dynamicpb.NewMessage(responseDesc)
		err := stream.RecvMsg(msg)
		if err == io.EOF {
			return values, nil
		}
		if err != nil {
			return values, err
		}
		values = append(values, protoMessageMap(t, msg))
	}
}

func protoMessageMap(t *testing.T, msg proto.Message) map[string]any {
	t.Helper()
	encoded, err := protojson.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal native protobuf value: %v", err)
	}
	var value map[string]any
	if err := json.Unmarshal(encoded, &value); err != nil {
		t.Fatalf("decode native ProtoJSON value: %v", err)
	}
	return value
}
