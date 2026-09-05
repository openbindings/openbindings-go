package openapi

import (
	"github.com/openbindings/openapi-client/go/provider"
	"github.com/openbindings/openbindings-go/invoke"
)

// BuiltinClassify is the Core hook adapter for the family-wide HTTP success
// convention. All declaration-aware response processing remains native.
func BuiltinClassify(_ invoke.InvokeSite, raw invoke.RawResult) (bool, error) {
	return raw.Status != nil && *raw.Status >= 200 && *raw.Status < 300, nil
}

// decodeByContentTypeFor adapts the native response decoder to the SDK hook
// shape. The selected binding identifier is retained in the signature because
// it is part of the hook contract; edition behavior itself lives in the native
// client analysis and invocation paths.
func decodeByContentTypeFor(contentType, bindingSpec string) invoke.OutputDecoder {
	return func(_ invoke.InvokeSite, raw invoke.RawResult) (any, error) {
		return decodeTextLaneFor(contentType, raw.Body, bindingSpec)
	}
}

func decodeTextLaneFor(contentType string, body []byte, _ string) (any, error) {
	value, err := provider.DecodeResponseBody(contentType, body)
	if err != nil {
		return nil, &invoke.InvocationError{Code: invoke.ErrCodeExecutionFailed}
	}
	return value, nil
}
