package openbindings_test

import (
	"os"
	"testing"

	"github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/openbindingstest"
)

// TestKitOnTheTestEvaluator runs the conformance kit on core's own test
// evaluator, for developing the kit; set OB_KIT_DEV=1.
func TestKitOnTheTestEvaluator(t *testing.T) {
	if os.Getenv("OB_KIT_DEV") == "" {
		t.Skip("set OB_KIT_DEV=1")
	}
	openbindingstest.TestSchemaEvaluator(t, openbindings.KitTestEvaluator, openbindingstest.Options{})
}
