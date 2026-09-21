package openapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
	"github.com/openbindings/openbindings-go/synthesize"
)

func BenchmarkHTTPValueMigration(b *testing.B) {
	payload := make([]byte, 1<<20)
	for n := range payload {
		payload[n] = byte(n)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	document := fmt.Sprintf(`{"openapi":"3.0.3","info":{"title":"Pictures","version":"1"},"servers":[{"url":%q}],"paths":{"/picture":{"get":{"operationId":"getPicture","responses":{"200":{"description":"image","content":{"image/png":{"schema":{"type":"string","format":"binary"}}}}}}}}}`, server.URL)
	ctx := context.Background()
	iface, err := NewSynthesizer().SynthesizeInterface(ctx, &synthesize.SynthesizeInput{Sources: []synthesize.SynthesizeSource{{BindingSpec: BindingSpecOpenAPI30, Content: json.RawMessage(document)}}})
	if err != nil {
		b.Fatal(err)
	}
	operation := iface.Operations["getPicture"]
	operation.Output = map[string]any{"type": "object", "properties": map[string]any{"photoData": map[string]any{"type": "string"}}}
	iface.Operations["getPicture"] = operation
	for key, binding := range iface.Bindings {
		binding.OutputTransform = &openbindings.TransformOrRef{Inline: `{"photoData":$}`}
		iface.Bindings[key] = binding
	}
	op := invoke.NewOperationInvoker(NewInvokerWithClient(server.Client()))
	op.TransformEvaluator = openAPIJSONataEvaluator{}
	type result struct {
		PhotoData []byte `json:"photoData"`
	}
	for _, hook := range []bool{false, true} {
		b.Run(fmt.Sprintf("Image1MiBHook%v", hook), func(b *testing.B) {
			var options []invoke.InvokeOption
			if hook {
				options = append(options, invoke.WithOutputDecoder(func(_ invoke.InvokeSite, raw invoke.RawResult) (any, error) { return raw.Body, nil }))
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(payload)))
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				call := invoke.Invoke(ctx, op, iface, invoke.NewOperationSignature[any, result]("getPicture"), options...)
				_ = call.Close()
				got, err := invoke.Single(ctx, call.Outputs())
				if err != nil || len(got.PhotoData) != len(payload) {
					b.Fatalf("length=%d error=%v", len(got.PhotoData), err)
				}
			}
		})
	}
}
