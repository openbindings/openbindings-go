package openbindings

import (
	"encoding/json"
	"fmt"

	"github.com/openbindings/openbindings-go/internal/jsonpointer"
	"github.com/openbindings/openbindings-go/internal/kithook"
)

func init() {
	kithook.Bundle = func(c any, operation, direction string, spelling int) (json.RawMessage, error) {
		contracts := c.(*ValueContracts)
		key, found := contracts.keys[operation]
		if !found {
			return nil, fmt.Errorf("%w: %q", ErrOperationNotFound, operation)
		}
		document, refusal := contracts.space.bundle(jsonpointer.Format("operations", key, direction), bundleSpelling{spelling})
		if refusal != nil {
			return nil, refusal
		}
		return document, nil
	}
}
