package openapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
	"github.com/openbindings/openbindings-go/synthesize"
)

// An authored `anyOf: [{}, {not: {}}]` at a form property or multipart part
// is a choice with two candidates under §5.2 of the 3.x binding
// specifications (`not` never participates in resolution; a choice supplies
// a single resolved member declaration only when exactly one candidate
// remains), so it has no Encoding default row and no part carriage: the
// alternative is accounted exactly as `oneOf: [{type: string}, {type:
// integer}]` is, and a value supplied for the member refuses before dispatch
// as the plain species. Until 2026-09-02 this engine read the structure as a
// literal `true`, because the loader encodes a literal boolean Schema Object
// in that shape; the loader now marks its own encoding, and only the marked
// encoding is restored to a boolean when the schema crosses the OpenBindings
// boundary -- an authored structure is emitted as written.

const ambiguousChoicePart = `{"anyOf":[{},{"not":{}}]}`

func ambiguousChoiceBindingSpec(edition string) string {
	return "openbindings.openapi-" + edition[:3] + "@1"
}

func ambiguousChoiceDocument(edition, media, part, server string) string {
	var schema string
	if media == "text/plain" {
		schema = part
	} else {
		schema = `{"type":"object","properties":{"ok":{"type":"string"},"choice":` + part + `}}`
	}
	return `{"openapi":"` + edition + `","info":{"title":"t","version":"1"},"servers":[{"url":"` + server + `"}],"paths":{"/up":{"post":{"operationId":"up","requestBody":{"required":true,"content":{"` + media + `":{"schema":` + schema + `}}},"responses":{"204":{"description":"ok"}}}}}}`
}

func TestAmbiguousChoiceMemberRefusesBeforeDispatchThroughTheInvoker(t *testing.T) {
	for _, edition := range []string{"3.0.4", "3.1.2", "3.2.0"} {
		for _, media := range []string{"multipart/form-data", "application/x-www-form-urlencoded", "text/plain"} {
			t.Run(edition+" "+media, func(t *testing.T) {
				dispatched := false
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					dispatched = true
					_, _ = io.ReadAll(r.Body)
					w.WriteHeader(http.StatusNoContent)
				}))
				defer srv.Close()
				doc := ambiguousChoiceDocument(edition, media, ambiguousChoicePart, srv.URL)
				input := map[string]any{"body": map[string]any{"ok": "fine", "choice": "eA=="}}
				if media == "text/plain" {
					input = map[string]any{"body": "eA=="}
				}
				inv := NewInvokerWithClient(srv.Client()).InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
					Source:   invoke.InvocationSource{BindingSpec: ambiguousChoiceBindingSpec(edition), Content: json.RawMessage(doc)},
					Selector: "#/paths/~1up/post",
					Context:  map[string]any{},
				})
				_ = inv.Write(context.Background(), input)
				_ = inv.Close()
				out := inv.Outputs()
				var terminal error
				for {
					_, err := out.Read(context.Background())
					if err != nil {
						if !errors.Is(err, io.EOF) {
							terminal = err
						}
						break
					}
				}
				if terminal == nil {
					t.Fatal("an ambiguous choice member admitted a supplied value")
				}
				if dispatched {
					t.Fatalf("an ambiguous choice member reached the wire (%v)", terminal)
				}
				var ie *invoke.InvocationError
				if !errors.As(terminal, &ie) || ie.Code == invoke.ErrCodeContextRequired {
					t.Fatalf("refusal species = %v, want a plain pre-dispatch refusal", terminal)
				}
			})
		}
	}
}

func TestAmbiguousChoiceMemberIsAccountedAsEveryOtherAmbiguousChoice(t *testing.T) {
	for _, edition := range []string{"3.0.4", "3.1.2", "3.2.0"} {
		for _, media := range []string{"multipart/form-data", "application/x-www-form-urlencoded"} {
			t.Run(edition+" "+media, func(t *testing.T) {
				coverage := func(part string) []string {
					doc := ambiguousChoiceDocument(edition, media, part, "https://api.example")
					result, err := NewSynthesizer().SynthesizeInterfaceWithCoverage(context.Background(), &synthesize.SynthesizeInput{
						Sources: []synthesize.SynthesizeSource{{BindingSpec: ambiguousChoiceBindingSpec(edition), Content: openbindings.TextContent(doc)}},
					})
					if err != nil {
						t.Fatal(err)
					}
					var cells []string
					for _, entry := range result.Coverage.Entries {
						cells = append(cells, string(entry.Scope)+":"+string(entry.Status)+":"+entry.ReasonCode)
					}
					return cells
				}
				got := coverage(ambiguousChoicePart)
				want := coverage(`{"oneOf":[{"type":"string"},{"type":"integer"}]}`)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("anyOf[{}, {not: {}}] coverage = %v, want the two-branch choice accounting %v", got, want)
				}
				for _, cell := range got {
					if cell == "alternative:represented:" {
						t.Fatalf("the ambiguous alternative is represented: %v", got)
					}
				}
			})
		}
	}
}

// Only the loader's marked encoding of a literal boolean is restored to the
// boolean when the schema crosses the OpenBindings boundary; an authored
// structure is carried as written, which is what the TypeScript SDK emits for
// the same document.
func TestOnlyTheLoaderEncodingIsRestoredToABooleanSchema(t *testing.T) {
	spec := `{"openapi":"3.1.2","info":{"title":"t","version":"1"},"servers":[{"url":"https://example.test"}],"paths":{"/x":{"post":{"operationId":"put","requestBody":{"required":true,"content":{"application/json":{"schema":{"type":"object","properties":{"literal":true,"authored":{"anyOf":[{},{"not":{}}]}}}}}},"responses":{"204":{"description":"ok"}}}}}}`
	iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &synthesize.SynthesizeInput{Sources: []synthesize.SynthesizeSource{{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)}}})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := iface.Operations["put"].Input.(map[string]any)
	members, _ := input["properties"].(map[string]any)
	if members["literal"] != true {
		t.Fatalf("literal true schema = %#v", members["literal"])
	}
	authored := map[string]any{"anyOf": []any{map[string]any{}, map[string]any{"not": map[string]any{}}}}
	if !reflect.DeepEqual(members["authored"], authored) {
		t.Fatalf("authored anyOf structure = %#v, want it carried as written", members["authored"])
	}
	for _, raw := range []any{members["literal"], members["authored"]} {
		if encoded, _ := json.Marshal(raw); strings.Contains(string(encoded), liftedBooleanLiteralMarker) {
			t.Fatalf("the loader's marker leaked into the emitted schema: %s", encoded)
		}
	}
}
