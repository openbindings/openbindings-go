package canonicaljson_test

import (
	"fmt"

	"github.com/openbindings/openbindings-go/canonicaljson"
)

func ExampleMarshal() {
	data := map[string]any{
		"z": 1,
		"a": 2,
		"m": 3,
	}

	out, _ := canonicaljson.Marshal(data)
	fmt.Println(string(out))
	// Output: {"a":2,"m":3,"z":1}
}
