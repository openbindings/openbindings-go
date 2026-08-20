package invoke

import (
	"context"
	"fmt"

	openbindings "github.com/openbindings/openbindings-go"
)

// CombineInvokers returns a single BindingInvoker that routes to the
// appropriate inner invoker by the source's binding-specification
// identifier. Identifiers are exact and opaque (core §6): matching is
// string equality, never version-range interpretation. First registration
// wins for a given identifier; order matters.
func CombineInvokers(invokers ...BindingInvoker) BindingInvoker {
	c := &combinedInvoker{}
	for _, iv := range invokers {
		c.add(iv)
	}
	return c
}

type combinedInvoker struct {
	bySpec map[string]BindingInvoker // exact identifier -> invoker
	specs  []openbindings.BindingSpecInfo
}

func (c *combinedInvoker) add(iv BindingInvoker) {
	for _, info := range iv.BindingSpecs() {
		if _, taken := c.bySpec[info.BindingSpec]; taken {
			continue // first registration wins
		}
		if c.bySpec == nil {
			c.bySpec = map[string]BindingInvoker{}
		}
		c.bySpec[info.BindingSpec] = iv
		c.specs = append(c.specs, info)
	}
}

func (c *combinedInvoker) BindingSpecs() []openbindings.BindingSpecInfo {
	cp := make([]openbindings.BindingSpecInfo, len(c.specs))
	copy(cp, c.specs)
	return cp
}

func (c *combinedInvoker) InvokeBinding(ctx context.Context, args *BindingInvocationArgs) Invocation[any, any] {
	invoker := c.findInvoker(args.Source.BindingSpec)
	if invoker == nil {
		return NewErroredInvocation[any, any](&InvocationError{
			Code: ErrCodeBindingNotFound,
		})
	}
	return invoker.InvokeBinding(ctx, args)
}

// prepareBinding routes the side-effect-free preflight to the matching inner
// invoker. An invoker without BindingPreparer simply reports no requirement.
func (c *combinedInvoker) prepareBinding(ctx context.Context, args *BindingInvocationArgs) (*ContextRequiredDetails, error) {
	invoker := c.findInvoker(args.Source.BindingSpec)
	if invoker == nil {
		return nil, fmt.Errorf("%w: %s", ErrNoInvoker, args.Source.BindingSpec)
	}
	if p, ok := invoker.(BindingPreparer); ok {
		return p.PrepareBinding(ctx, args)
	}
	return nil, nil
}

func (c *combinedInvoker) findInvoker(bindingSpec string) BindingInvoker {
	return c.bySpec[bindingSpec]
}
