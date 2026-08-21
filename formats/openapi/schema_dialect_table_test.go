package openapi

// Executes the shared Schema Object dialect case table
// (testdata/schema-object-dialect-cases.json) through the shipped acceptance
// floor. The same file, at the same digest, embeds in openapi-client/go,
// openapi-client/typescript, openbindings-go/formats/openapi and
// openbindings-ts/packages/openapi: four engines, one answer.
//
// Each cell places one Schema Object at a success response's only media
// alternative beside a clean sibling operation, so every cell asserts three
// things at once: which positions the governing dialect finds defective, which
// class owns each position, and that the confinement stays inside the unit that
// earned it.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"

	"github.com/openbindings/openbindings-go/synthesize"
)

// The embedded table's own digest. A change here is a change to the shared
// answer and must land in every engine simultaneously.
const schemaObjectDialectTableSHA256 = "f3b84e690c1a77cd7710a704876ebc00129824dd8ec11a011c9b447dcde1b58c"

type schemaDialectPosition struct {
	Position string `json:"position"`
	Class    string `json:"class"`
}

type schemaDialectCell struct {
	ID             string                  `json:"id"`
	Line           string                  `json:"line"`
	OpenAPI        string                  `json:"openapi"`
	Schema         map[string]any          `json:"schema"`
	Positions      []schemaDialectPosition `json:"positions"`
	Disposition    string                  `json:"disposition"`
	Downstream     string                  `json:"downstream"`
	DownstreamNote string                  `json:"downstreamNote"`
	Why            string                  `json:"why"`
}

type schemaDialectTable struct {
	SchemaVersion int                 `json:"schemaVersion"`
	Cells         []schemaDialectCell `json:"cells"`
}

const schemaDialectSubjectRef = "#/paths/~1a/get"
const schemaDialectSchemaPtr = "#/paths/~1a/get/responses/200/content/application~1json/schema"
const schemaDialectCleanRef = "#/paths/~1b/get"

func loadSchemaDialectTable(t *testing.T) schemaDialectTable {
	t.Helper()
	data, err := os.ReadFile("testdata/schema-object-dialect-cases.json")
	if err != nil {
		t.Fatalf("read shared dialect case table: %v", err)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != schemaObjectDialectTableSHA256 {
		t.Fatalf("shared dialect case table digest %s, pinned %s: the table changed without a simultaneous four-engine landing", got, schemaObjectDialectTableSHA256)
	}
	var table schemaDialectTable
	if err := json.Unmarshal(data, &table); err != nil {
		t.Fatalf("parse shared dialect case table: %v", err)
	}
	if len(table.Cells) == 0 {
		t.Fatal("shared dialect case table carries no cells")
	}
	return table
}

// schemaDialectDocument is the one document shape every engine builds for a
// cell: the cell's Schema Object as the sole media alternative of the subject
// operation's success response, beside a clean sibling.
func schemaDialectDocument(cell schemaDialectCell) map[string]any {
	response := func(schema any) map[string]any {
		return map[string]any{
			"200": map[string]any{
				"description": "ok",
				"content":     map[string]any{"application/json": map[string]any{"schema": schema}},
			},
		}
	}
	return map[string]any{
		"openapi": cell.OpenAPI,
		"info":    map[string]any{"title": "schema object dialect", "version": "1"},
		"paths": map[string]any{
			"/a": map[string]any{"get": map[string]any{"responses": response(cell.Schema)}},
			"/b": map[string]any{"get": map[string]any{"responses": response(map[string]any{"type": "object"})}},
		},
	}
}

func TestSchemaObjectDialectTable(t *testing.T) {
	table := loadSchemaDialectTable(t)
	for _, cell := range table.Cells {
		t.Run(cell.ID, func(t *testing.T) {
			floor := computeAcceptanceFloor(schemaDialectDocument(cell))
			if floor == nil {
				t.Fatalf("no floor for edition %s", cell.OpenAPI)
			}
			if floor.Line != cell.Line {
				t.Fatalf("edition %s read as line %s, want %s", cell.OpenAPI, floor.Line, cell.Line)
			}
			if floor.Refusal != "" {
				t.Fatalf("a confined defect refused the whole source: %s", floor.Refusal)
			}

			subject := floor.opVerdict(schemaDialectSubjectRef)
			if subject == nil {
				t.Fatalf("no verdict for %s", schemaDialectSubjectRef)
			}
			if subject.Disposition != cell.Disposition {
				t.Fatalf("%s is %s, want %s\n%s", schemaDialectSubjectRef, subject.Disposition, cell.Disposition, cell.Why)
			}

			// The evidence a consumer sees: the defective positions that
			// climbed to the unit, each with the class that owns it.
			want := make([]string, 0, len(cell.Positions))
			for _, position := range cell.Positions {
				want = append(want, position.Class+" "+schemaDialectSchemaPtr+position.Position)
			}
			got := make([]string, 0, len(subject.Defects))
			for _, d := range subject.Defects {
				got = append(got, d.Class+" "+d.Position)
			}
			sort.Strings(want)
			sort.Strings(got)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("defective positions %v, want %v\n%s", got, want, cell.Why)
			}

			// Confinement: the clean sibling never pays for the cell's defect.
			clean := floor.opVerdict(schemaDialectCleanRef)
			if clean == nil || clean.Disposition != "represented" || len(clean.Defects) != 0 {
				t.Fatalf("the clean sibling operation did not survive intact: %+v", clean)
			}
		})
	}
}

// The same cells through the SHIPPED synthesis path: the floor's verdict has to
// reach a consumer as a coverage entry, not merely exist inside the instrument.
func TestSchemaObjectDialectTableThroughSynthesis(t *testing.T) {
	table := loadSchemaDialectTable(t)
	for _, cell := range table.Cells {
		t.Run(cell.ID, func(t *testing.T) {
			document, err := json.Marshal(schemaDialectDocument(cell))
			if err != nil {
				t.Fatalf("marshal document: %v", err)
			}
			result, err := NewSynthesizer().SynthesizeInterfaceWithCoverage(context.Background(), &synthesize.SynthesizeInput{
				Name: "dialect",
				Sources: []synthesize.SynthesizeSource{{
					BindingSpec: "openbindings.openapi@1",
					Name:        "dialect",
					Content:     json.RawMessage(document),
					Embed:       true,
				}},
			})
			// Two cell classes never reach coverage at the current heads, each
			// for a reason the table names and neither owned by this class.
			switch cell.Downstream {
			case "obi-invalid", "go-loader-refusal":
				if err == nil {
					t.Fatalf("the cell is pinned as %s and the synthesis succeeded: the filed defect is fixed and the table must be updated in every engine\n%s", cell.Downstream, cell.DownstreamNote)
				}
				return
			}
			if err != nil {
				t.Fatalf("a confined defect failed the whole synthesis: %v\n%s", err, cell.Why)
			}
			if verr := result.Interface.Validate(); verr != nil {
				t.Fatalf("the synthesized OBI does not satisfy the document rules: %v\n%s", verr, cell.Why)
			}
			subject, clean := "", ""
			for _, entry := range result.Coverage.Entries {
				if entry.Scope != "target" {
					continue
				}
				switch entry.SourceRef {
				case schemaDialectSubjectRef:
					subject = string(entry.Status) + " " + string(entry.ReasonCode)
				case schemaDialectCleanRef:
					clean = string(entry.Status)
				}
			}
			wantSubject := "represented "
			if cell.Disposition == "invalid" {
				wantSubject = "invalid openapi.invalid_unit"
			}
			if subject != wantSubject {
				t.Fatalf("%s covered as %q, want %q\n%s", schemaDialectSubjectRef, subject, wantSubject, cell.Why)
			}
			if clean != "represented" {
				t.Fatalf("the clean sibling covered as %q, want represented", clean)
			}
		})
	}
}
