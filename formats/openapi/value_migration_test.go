package openapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
	"github.com/openbindings/openbindings-go/jsonvalue"
	"github.com/openbindings/openbindings-go/synthesize"
)

func TestOwnedPictureTransformRecoveryAndUpload(t *testing.T) {
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	for _, payload := range [][]byte{picture.Bytes(), {}, {0, 0, 255, 128, 0}} {
		for _, hook := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d-bytes/hook=%v", len(payload), hook), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				uploaded := make(chan []byte, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == "GET" {
						w.Header().Set("Content-Type", "image/png")
						w.Write(payload)
						return
					}
					data, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					uploaded <- data
					w.WriteHeader(204)
				}))
				defer server.Close()
				document := fmt.Sprintf(`{"openapi":"3.0.3","info":{"title":"Pictures","version":"1"},"servers":[{"url":%q}],"paths":{"/picture":{"get":{"operationId":"getPicture","responses":{"200":{"description":"PNG","content":{"image/png":{"schema":{"type":"string","format":"binary"}}}}}},"post":{"operationId":"uploadPicture","requestBody":{"required":true,"content":{"image/png":{"schema":{"type":"string","format":"binary"}}}},"responses":{"204":{"description":"uploaded"}}}}}}`, server.URL)
				iface, err := NewSynthesizer().SynthesizeInterface(ctx, &synthesize.SynthesizeInput{Sources: []synthesize.SynthesizeSource{{BindingSpec: BindingSpecOpenAPI30, Content: json.RawMessage(document)}}})
				if err != nil {
					t.Fatal(err)
				}
				operation := iface.Operations["getPicture"]
				operation.Output = map[string]any{"type": "object", "required": []any{"nested"}, "properties": map[string]any{"nested": map[string]any{"type": "object", "properties": map[string]any{"photoData": map[string]any{"type": "string"}}}}}
				iface.Operations["getPicture"] = operation
				for key, binding := range iface.Bindings {
					if binding.Operation == "getPicture" {
						binding.OutputTransform = openbindings.InlineTransform(`{"nested":{"photoData":$}}`)
						iface.Bindings[key] = binding
					}
				}
				op := invoke.NewOperationInvoker(NewInvokerWithClient(server.Client()))
				op.TransformEvaluator = openAPIJSONataEvaluator{}
				var options []invoke.InvokeOption
				if hook {
					options = append(options, invoke.WithOutputDecoder(func(_ invoke.InvokeSite, raw invoke.RawResult) (any, error) { return raw.Body, nil }))
				}
				type result struct {
					Nested struct {
						PhotoData []byte `json:"photoData"`
					} `json:"nested"`
				}
				call := invoke.Invoke(ctx, op, iface, invoke.NewOperationSignature[any, result]("getPicture"), options...)
				_ = call.Close()
				var got result
				if len(payload) == 0 {
					// The existing HTTP correspondence treats a zero-length success body
					// as no output. Preserve that rule, then exercise explicit empty upload.
					if _, err := call.Outputs().Read(ctx); err != io.EOF {
						t.Fatal(err)
					}
					got.Nested.PhotoData = []byte{}
				} else {
					got, err = invoke.Single(ctx, call.Outputs())
					if err != nil {
						t.Fatal(err)
					}
				}
				if !bytes.Equal(got.Nested.PhotoData, payload) {
					t.Fatalf("recovered %x want %x", got.Nested.PhotoData, payload)
				}
				display, err := jsonvalue.MarshalWithOptions(got, jsonvalue.MarshalOptions{})
				if err != nil {
					t.Fatal(err)
				}
				var view map[string]any
				if err := jsonvalue.Unmarshal(display, &view); err != nil {
					t.Fatal(err)
				}
				if view["nested"].(map[string]any)["photoData"] != base64.StdEncoding.EncodeToString(payload) {
					t.Fatalf("display %s", display)
				}
				type request struct {
					Body []byte `json:"body"`
				}
				upload := invoke.Invoke(ctx, op, iface, invoke.NewOperationSignature[request, any]("uploadPicture"))
				if err := upload.Write(ctx, request{Body: got.Nested.PhotoData}); err != nil {
					t.Fatal(err)
				}
				_ = upload.Close()
				if v, err := upload.Outputs().Read(ctx); err != io.EOF {
					t.Fatalf("upload output %#v %v", v, err)
				}
				select {
				case actual := <-uploaded:
					if !bytes.Equal(actual, payload) {
						t.Fatalf("uploaded %x want %x", actual, payload)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			})
		}
	}
}
