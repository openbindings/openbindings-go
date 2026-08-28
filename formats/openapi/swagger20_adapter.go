package openapi

import (
	"context"

	openapiclient "github.com/openbindings/openapi-client/go"
	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
)

// runSwagger20Pass1 is the edition-specific adapter lane while the 2.0 token
// remains registered but unwarranted. It maps the OBI envelope into the
// native Swagger 2.0 loader and selector surface; successful preparation
// still refuses as unsupported until the later construction passes supply
// the full execution contract and warrant the token.
func (e *Runtime) runSwagger20Pass1(ctx context.Context, args *invoke.BindingInvocationArgs) error {
	var content []byte
	if args.Source.Content != nil {
		var err error
		content, err = openbindings.ContentToBytes(args.Source.Content)
		if err != nil {
			return &invoke.InvocationError{Code: invoke.ErrCodeSourceLoadFailed}
		}
	}
	_, err := e.engine.PrepareSwagger20(ctx, openapiclient.Swagger20PrepareOptions{
		Source: openapiclient.Swagger20Source{
			Location: args.Source.Location,
			Content:  content,
		},
		Ref:        args.Selector,
		Context:    args.Context,
		HTTPClient: e.client,
	})
	if err != nil {
		return bridgeExecutionError(err)
	}
	return unsupportedBindingSpecError(BindingSpecOpenAPI20)
}
