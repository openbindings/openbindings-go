package openapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSourceContentBytes(t *testing.T) {
	tests := []struct {
		name    string
		content json.RawMessage
		want    string
		invalid bool
	}{
		{name: "object retains JSON image", content: json.RawMessage(`{ "openapi": "3.1.0" }`), want: `{ "openapi": "3.1.0" }`},
		{name: "string yields source text", content: json.RawMessage(`"openapi: 3.1.0\ninfo: {}"`), want: "openapi: 3.1.0\ninfo: {}"},
		{name: "null reaches loader", content: json.RawMessage(`null`), want: `null`},
		{name: "array reaches loader", content: json.RawMessage(`[]`), want: `[]`},
		{name: "absent is invalid", invalid: true},
		{name: "malformed object reaches loader", content: json.RawMessage(`{"openapi":`), want: `{"openapi":`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := sourceContentBytes(tt.content)
			if tt.invalid {
				if err == nil || !strings.Contains(err.Error(), "OpenAPI source content") {
					t.Fatalf("expected OpenAPI source error, got bytes %q, err %v", got, err)
				}
				return
			}
			if err != nil || string(got) != tt.want {
				t.Fatalf("bytes = %q, err = %v; want %q", got, err, tt.want)
			}
		})
	}
}
