package openapi

// Executes the shared acceptance-floor case table (block 8d-1): the 8 policy
// mechanism fixtures (block 8b, record 72 §3) and the 68-cell OAS shape
// table, with expectations computed by the reviewed corpus-lab census
// instrument (censusOne + projectLadder restricted to the rostered classes,
// with the shipped §3 part-2 decision applied over its projection). The same
// table file, at the same digest, embeds in openbindings-go/formats/openapi,
// openapi-client/go, and openapi-client/typescript: three ports, one answer.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

// The embedded table's own digest, and the digests of the instruments it was
// generated from. A change to any of these is a change to the shared answer
// and must land in every engine simultaneously.
const (
	acceptanceFloorCaseTableSHA256 = "53d53b3f43e3ca88e0788e3cff2d45be9b9c50cc90eb0d4702f9712a51e277a1"
	oasShapeTableSHA256            = "4e8f5393e48868e2a9468d7232921e1c2f3b33efd941f605b9e328b23191d456"
)

type floorCaseExpect struct {
	Refuses bool `json:"refuses"`
	Hits    []struct {
		Class   string `json:"class"`
		Pointer string `json:"pointer"`
	} `json:"hits"`
	OperationsRepresented            int               `json:"operationsRepresented"`
	OperationsInvalid                int               `json:"operationsInvalid"`
	OperationsExcludedByRequestMedia int               `json:"operationsExcludedByRequestMedia"`
	InvalidRequestAlternatives       int               `json:"invalidRequestAlternatives"`
	ProjectionEntriesOnReachingUnits int               `json:"projectionEntriesOnReachingUnits"`
	Dispositions                     map[string]string `json:"dispositions"`
}

type floorCaseTable struct {
	SchemaVersion int `json:"schemaVersion"`
	GeneratedFrom struct {
		ShapeTableSHA256 string `json:"shapeTableSha256"`
	} `json:"generatedFrom"`
	Mechanisms []struct {
		Name   string          `json:"name"`
		Doc    json.RawMessage `json:"doc"`
		Expect floorCaseExpect `json:"expect"`
	} `json:"mechanisms"`
	ShapeCells []struct {
		ID     string          `json:"id"`
		Doc    json.RawMessage `json:"doc"`
		Expect floorCaseExpect `json:"expect"`
	} `json:"shapeCells"`
}

func loadFloorCaseTable(t *testing.T) floorCaseTable {
	t.Helper()
	data, err := os.ReadFile("testdata/acceptance-floor-case-table.json")
	if err != nil {
		t.Fatalf("read shared case table: %v", err)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != acceptanceFloorCaseTableSHA256 {
		t.Fatalf("shared case table digest %s, pinned %s: the table changed without a simultaneous three-engine landing", got, acceptanceFloorCaseTableSHA256)
	}
	var table floorCaseTable
	if err := json.Unmarshal(data, &table); err != nil {
		t.Fatalf("parse shared case table: %v", err)
	}
	if table.GeneratedFrom.ShapeTableSHA256 != oasShapeTableSHA256 {
		t.Fatalf("case table generated from shape table %s, pinned %s", table.GeneratedFrom.ShapeTableSHA256, oasShapeTableSHA256)
	}
	return table
}

func assertFloorCase(t *testing.T, name string, doc json.RawMessage, expect floorCaseExpect) {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(doc, &root); err != nil {
		t.Fatalf("%s: parse case document: %v", name, err)
	}
	floor := computeAcceptanceFloor(root)
	if floor == nil {
		t.Fatalf("%s: floor not applicable (edition gate); the table only carries accepted editions", name)
	}
	if got := floor.Refusal != ""; got != expect.Refuses {
		t.Errorf("%s: refuses=%v, expected %v (refusal %q)", name, got, expect.Refuses, floor.Refusal)
	}
	var represented, invalid, excluded, invalidAlts, onReaching int
	for _, ref := range floor.OpOrder {
		op := floor.Ops[ref]
		switch op.Disposition {
		case "represented":
			represented++
		case "invalid":
			invalid++
		case "excluded-request-media":
			excluded++
		}
		invalidAlts += len(op.InvalidAlternatives)
		for _, ds := range op.Projections {
			for _, d := range ds {
				if d.Class == floorD6 || d.Class == floorD11 {
					onReaching++
				}
			}
		}
		if want, listed := expect.Dispositions[ref]; listed && want != op.Disposition {
			t.Errorf("%s: %s disposition %q, expected %q", name, ref, op.Disposition, want)
		}
	}
	if represented != expect.OperationsRepresented {
		t.Errorf("%s: represented %d, expected %d", name, represented, expect.OperationsRepresented)
	}
	if invalid != expect.OperationsInvalid {
		t.Errorf("%s: invalid %d, expected %d", name, invalid, expect.OperationsInvalid)
	}
	if excluded != expect.OperationsExcludedByRequestMedia {
		t.Errorf("%s: excluded-request-media %d, expected %d", name, excluded, expect.OperationsExcludedByRequestMedia)
	}
	if invalidAlts != expect.InvalidRequestAlternatives {
		t.Errorf("%s: invalid request alternatives %d, expected %d", name, invalidAlts, expect.InvalidRequestAlternatives)
	}
	if onReaching != expect.ProjectionEntriesOnReachingUnits {
		t.Errorf("%s: reaching-unit projections %d, expected %d", name, onReaching, expect.ProjectionEntriesOnReachingUnits)
	}
	if len(expect.Dispositions) != len(floor.OpOrder) {
		t.Errorf("%s: raw inventory %d operations, expected %d", name, len(floor.OpOrder), len(expect.Dispositions))
	}
}

func TestAcceptanceFloorMechanismFixtures(t *testing.T) {
	table := loadFloorCaseTable(t)
	if len(table.Mechanisms) != 8 {
		t.Fatalf("mechanism fixtures: %d, expected the 8b eight", len(table.Mechanisms))
	}
	for _, m := range table.Mechanisms {
		assertFloorCase(t, m.Name, m.Doc, m.Expect)
	}
}

func TestAcceptanceFloorShapeTable(t *testing.T) {
	table := loadFloorCaseTable(t)
	if len(table.ShapeCells) != 68 {
		t.Fatalf("shape cells: %d, expected 68", len(table.ShapeCells))
	}
	for _, c := range table.ShapeCells {
		assertFloorCase(t, c.ID, c.Doc, c.Expect)
	}
}
