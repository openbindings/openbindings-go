package grpc

import (
	"fmt"
	"sort"

	openbindings "github.com/openbindings/openbindings-go"
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
	warnings []openbindings.SynthesizerWarning,
) []openbindings.SynthesisCoverageEntry {
	if disc == nil || iface == nil {
		return []openbindings.SynthesisCoverageEntry{}
	}

	byRef := make(map[string]coverageIdentity, len(iface.Bindings))
	byOperation := make(map[string]coverageIdentity, len(iface.Bindings))
	for _, binding := range iface.Bindings {
		id := coverageIdentity{operationKey: binding.Operation, bindingRef: binding.Ref}
		byRef[binding.Ref] = id
		byOperation[binding.Operation] = id
	}

	sort.Slice(disc.services, func(i, j int) bool {
		return string(disc.services[i].FullName()) < string(disc.services[j].FullName())
	})
	var entries []openbindings.SynthesisCoverageEntry
	for _, svc := range disc.services {
		for _, method := range serviceMethodsSorted(svc) {
			ref := string(svc.FullName()) + "/" + string(method.Name())
			if err := validateBoundClosure(method); err != nil {
				entries = append(entries, openbindings.SynthesisCoverageEntry{
					SourceIndex: 0,
					SourceRef:   ref,
					Scope:       openbindings.SynthesisCoverageTarget,
					Status:      openbindings.SynthesisExcluded,
					ReasonCode:  "grpc.schema_range",
					Rule:        "GRPC-P-03",
					Message:     err.Error(),
				})
				continue
			}
			id, ok := byRef[ref]
			if !ok {
				entries = append(entries, openbindings.SynthesisCoverageEntry{
					SourceIndex: 0,
					SourceRef:   ref,
					Scope:       openbindings.SynthesisCoverageTarget,
					Status:      openbindings.SynthesisImplementationUnsupported,
					ReasonCode:  "grpc.missing_emitted_binding",
					Message:     "the synthesizer returned without emitting this admitted protobuf RPC method",
				})
				continue
			}
			entries = append(entries, openbindings.SynthesisCoverageEntry{
				SourceIndex:  0,
				SourceRef:    ref,
				Scope:        openbindings.SynthesisCoverageTarget,
				Status:       openbindings.SynthesisRepresented,
				OperationKey: id.operationKey,
				BindingRef:   id.bindingRef,
			})
		}
	}

	for index, warning := range warnings {
		id, ok := warningIdentity(warning.Path, byOperation)
		if !ok {
			continue
		}
		entries = append(entries, openbindings.SynthesisCoverageEntry{
			SourceIndex:  0,
			SourceRef:    fmt.Sprintf("%s::projection::%s::%s::%d", id.bindingRef, warning.Path, warning.Code, index),
			Scope:        openbindings.SynthesisCoverageProjection,
			Status:       openbindings.SynthesisLossy,
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
