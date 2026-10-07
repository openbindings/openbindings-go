package schemaeval_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/schemaeval"
	"golang.org/x/sync/singleflight"
)

const tasksDocument = `{"openbindings":"0.2.0","operations":{
	"tasks.create":{"input":{"type":"object","required":["title"],"properties":{"title":{"type":"string"}}}},
	"tasks.list":{"output":{"type":"array","items":{"$ref":"#/schemas/Task"}}}},
	"schemas":{"Task":{"type":"object","required":["title"]}}}`

func mustDocument(document string) *openbindings.Document {
	doc, err := openbindings.ParseDocument([]byte(document))
	if err != nil {
		panic(err)
	}
	return doc
}

// A service over a small document can compile the value contracts it serves
// at startup and keep them: core keeps none. Contracts share no compiled
// work, so a service over a large document compiles on demand
// (Example_onDemand).
func Example_startup() {
	ctx := context.Background()
	compiler, err := openbindings.NewValueContractCompiler(schemaeval.New(schemaeval.Options{}))
	if err != nil {
		panic(err)
	}
	contracts, err := compiler.Resolve(ctx, mustDocument(tasksDocument))
	if err != nil {
		panic(err)
	}
	inputs := map[string]*openbindings.ValueContract{}
	for _, operation := range []string{"tasks.create", "tasks.list"} {
		contract, err := contracts.CompileInput(ctx, operation)
		if err != nil {
			panic(err)
		}
		inputs[operation] = contract
	}

	for _, body := range []string{`{"title":"write docs"}`, `{"title":7}`} {
		var mismatch *openbindings.MismatchError
		switch err := inputs["tasks.create"].ValidateJSON(ctx, []byte(body)); {
		case err == nil:
			fmt.Println(body, "is valid")
		case errors.As(err, &mismatch):
			fmt.Println(body, "fails at", mismatch.Problems[0].InstanceLocation)
		}
	}
	fmt.Println(errors.Is(inputs["tasks.list"].Err(), openbindings.ErrNoValueContract))
	// Output:
	// {"title":"write docs"} is valid
	// {"title":7} fails at /title
	// true
}

// onDemand compiles value contracts the first time a request needs one and
// keeps them. A compile in progress is shared through singleflight's DoChan,
// so each request can leave when its own ctx ends, and it runs under a ctx no
// single request owns (the service's lifetime), so one request leaving never
// fails the others. A ctx error is never kept.
type onDemand struct {
	lifetime  context.Context
	contracts *openbindings.ValueContracts
	flight    singleflight.Group
	mu        sync.Mutex
	inputs    map[string]*openbindings.ValueContract
}

func (d *onDemand) input(ctx context.Context, operation string) (*openbindings.ValueContract, error) {
	if kept := d.kept(operation); kept != nil {
		return kept, nil
	}
	results := d.flight.DoChan(operation, func() (any, error) {
		// Another request may have compiled it since the check above.
		if kept := d.kept(operation); kept != nil {
			return kept, nil
		}
		contract, err := d.contracts.CompileInput(d.lifetime, operation)
		if err != nil {
			return nil, err // ErrOperationNotFound, or the service shutting down
		}
		d.mu.Lock()
		d.inputs[operation] = contract
		d.mu.Unlock()
		return contract, nil
	})
	select {
	case result := <-results:
		if result.Err != nil {
			return nil, result.Err
		}
		return result.Val.(*openbindings.ValueContract), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (d *onDemand) kept(operation string) *openbindings.ValueContract {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.inputs[operation]
}

// A service compiling on demand serves a request whose ctx is live even
// when the request that started the compile has left.
func Example_onDemand() {
	compiler, _ := openbindings.NewValueContractCompiler(schemaeval.New(schemaeval.Options{}))
	contracts, _ := compiler.Resolve(context.Background(), mustDocument(tasksDocument))
	service := &onDemand{lifetime: context.Background(), contracts: contracts, inputs: map[string]*openbindings.ValueContract{}}

	contract, err := service.input(context.Background(), "tasks.create")
	if err != nil {
		panic(err)
	}
	fmt.Println(contract.ValidateJSON(context.Background(), []byte(`{"title":"t"}`)))
	// Output: <nil>
}

// gatedEvaluator blocks each Compile until released, so a test can cancel a
// request while its compile runs.
type gatedEvaluator struct {
	openbindings.SchemaEvaluator
	started chan struct{}
	release chan struct{}
}

func (g gatedEvaluator) Compile(ctx context.Context, bundle openbindings.SchemaBundle) (openbindings.CompiledSchema, error) {
	g.started <- struct{}{}
	<-g.release
	return g.SchemaEvaluator.Compile(ctx, bundle)
}

// The first request leaves mid-compile; the second, whose ctx is live, gets
// the contract the shared compile produced.
func TestOnDemand_FirstRequestLeaves(t *testing.T) {
	gate := gatedEvaluator{schemaeval.New(schemaeval.Options{}), make(chan struct{}, 1), make(chan struct{})}
	compiler, _ := openbindings.NewValueContractCompiler(gate)
	contracts, _ := compiler.Resolve(context.Background(), mustDocument(tasksDocument))
	service := &onDemand{lifetime: context.Background(), contracts: contracts, inputs: map[string]*openbindings.ValueContract{}}

	first, leave := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		_, err := service.input(first, "tasks.create")
		firstDone <- err
	}()
	<-gate.started
	secondDone := make(chan error, 1)
	go func() {
		contract, err := service.input(context.Background(), "tasks.create")
		if err == nil {
			err = contract.ValidateJSON(context.Background(), []byte(`{"title":"t"}`))
		}
		secondDone <- err
	}()
	leave()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("the first request: %v", err)
	}
	close(gate.release)
	if err := <-secondDone; err != nil {
		t.Fatalf("the second request: %v", err)
	}
}
