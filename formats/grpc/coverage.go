package grpc

import (
	"fmt"
	"sort"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/synthesize"
)

type coverageIdentity struct {
	operationKey string
	bindingRef   string
}

// synthesisCoverage inventories every protobuf RPC method observed by the
// synthesizer. Methods outside openbindings.grpc@1's accepted ProtoJSON schema
// range are explicit binding-spec exclusions; admitted methods must have a
// corresponding emitted binding. Projection warnings are durable coverage
// entries rather than transient diagnostics only.
func synthesisCoverage(
	disc *discovery,
	iface *openbindings.Interface,
	warnings []synthesize.SynthesizerWarning,
) []synthesize.SynthesisCoverageEntry {
	if disc == nil || iface == nil {
		return []synthesize.SynthesisCoverageEntry{}
	}

	byRef := make(map[string]coverageIdentity, len(iface.Bindings))
	byOperation := make(map[string]coverageIdentity, len(iface.Bindings))
	for _, binding := range iface.Bindings {
		id := coverageIdentity{operationKey: binding.Operation, bindingRef: binding.Ref}
		byRef[binding.Ref] = id
		byOperation[binding.Operation] = id
	}
	requirements := []string{}
	sourceLocation := ""
	for _, source := range iface.Sources {
		if source.BindingSpec == BindingSpec {
			sourceLocation = source.Location
			break
		}
	}
	if address, err := parseDialAddress(sourceLocation); err == nil && !address.explicit {
		requirements = []string{"configuration.transport"}
	}

	sort.Slice(disc.services, func(i, j int) bool {
		return string(disc.services[i].FullName()) < string(disc.services[j].FullName())
	})
	var entries []synthesize.SynthesisCoverageEntry
	for _, svc := range disc.services {
		for _, method := range serviceMethodsSorted(svc) {
			ref := string(svc.FullName()) + "/" + string(method.Name())
			if err := validateBoundClosure(method); err != nil {
				entries = append(entries, synthesize.SynthesisCoverageEntry{
					SourceIndex: 0,
					SourceRef:   ref,
					Scope:       synthesize.SynthesisCoverageTarget,
					Status:      synthesize.SynthesisExcluded,
					ReasonCode:  "grpc.schema_range",
					Rule:        "GRPC-P-03",
					Message:     err.Error(),
				})
				continue
			}
			id, ok := byRef[ref]
			if !ok {
				entries = append(entries, synthesize.SynthesisCoverageEntry{
					SourceIndex: 0,
					SourceRef:   ref,
					Scope:       synthesize.SynthesisCoverageTarget,
					Status:      synthesize.SynthesisImplementationUnsupported,
					ReasonCode:  "grpc.missing_emitted_binding",
					Message:     "the synthesizer returned without emitting this admitted protobuf RPC method",
				})
				continue
			}
			entries = append(entries, synthesize.SynthesisCoverageEntry{
				SourceIndex:  0,
				SourceRef:    ref,
				Scope:        synthesize.SynthesisCoverageTarget,
				Status:       synthesize.SynthesisRepresented,
				OperationKey: id.operationKey,
				BindingRef:   id.bindingRef,
				Requirements: append([]string{}, requirements...),
			})
		}
	}

	for index, warning := range warnings {
		id, ok := warningIdentity(warning.Path, byOperation)
		if !ok {
			continue
		}
		entries = append(entries, synthesize.SynthesisCoverageEntry{
			SourceIndex:  0,
			SourceRef:    fmt.Sprintf("%s::projection::%s::%s::%d", id.bindingRef, warning.Path, warning.Code, index),
			Scope:        synthesize.SynthesisCoverageProjection,
			Status:       synthesize.SynthesisLossy,
			OperationKey: id.operationKey,
			BindingRef:   id.bindingRef,
			ReasonCode:   warning.Code,
			Message:      warning.Message,
			Details:      warning.Details,
		})
	}
	return entries
}

func warningIdentity(path string, byOperation map[string]coverageIdentity) (coverageIdentity, bool) {
	for operationKey, id := range byOperation {
		if path == "operations."+operationKey+".input" || path == "operations."+operationKey+".output" {
			return id, true
		}
	}
	return coverageIdentity{}, false
}
