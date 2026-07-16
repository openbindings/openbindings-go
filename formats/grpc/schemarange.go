package grpc

import (
	"fmt"

	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// This file enforces openbindings.grpc@1 §3's accepted schema range
// (referenced by GRPC-P-03): acceptance is evaluated per BOUND METHOD's
// transitive message closure — the request and response types and every
// message, enum, and field type reachable from them. Files in a carried
// set outside every bound closure (a proto2 descriptor.proto riding along
// for options, for instance) are inert carriage and never grounds for
// refusal. Within a bound closure, the following are refused loudly at
// descriptor load:
//
//   - proto2 groups
//   - fields with required presence (proto2 required, editions
//     field_presence = LEGACY_REQUIRED)
//   - fields with message_encoding = DELIMITED
//   - editions files whose resolved json_format feature is not ALLOW
//
// Proto3 files always satisfy the range. In short: every bound method's
// closure must sit fully inside the canonical JSON mapping.

// validateBoundClosure walks the bound method's transitive message closure
// and refuses the constructs above.
func validateBoundClosure(method protoreflect.MethodDescriptor) error {
	w := &closureWalker{
		seenMessages: map[protoreflect.FullName]bool{},
		seenFiles:    map[string]bool{},
	}
	for _, msg := range []protoreflect.MessageDescriptor{method.Input(), method.Output()} {
		if msg == nil {
			continue
		}
		if err := w.checkMessage(msg); err != nil {
			return fmt.Errorf(
				"gRPC method %q is outside the accepted schema range (openbindings.grpc@1 §3): %w", method.FullName(), err)
		}
	}
	return nil
}

type closureWalker struct {
	seenMessages map[protoreflect.FullName]bool
	seenFiles    map[string]bool
}

func (w *closureWalker) checkMessage(msg protoreflect.MessageDescriptor) error {
	if w.seenMessages[msg.FullName()] {
		return nil
	}
	w.seenMessages[msg.FullName()] = true

	if err := w.checkFile(msg.ParentFile()); err != nil {
		return err
	}

	fields := msg.Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if fd.Cardinality() == protoreflect.Required {
			return fmt.Errorf(
				"field %q has required presence (proto2 required / editions field_presence = LEGACY_REQUIRED)", fd.FullName())
		}
		if fd.Kind() == protoreflect.GroupKind {
			// protoreflect reports both proto2 groups and editions
			// message_encoding = DELIMITED as GroupKind; name whichever
			// the file's syntax makes it.
			if pf := fd.ParentFile(); pf != nil && pf.Syntax() == protoreflect.Editions {
				return fmt.Errorf("field %q uses message_encoding = DELIMITED", fd.FullName())
			}
			return fmt.Errorf("field %q is a proto2 group", fd.FullName())
		}
		switch {
		case fd.IsMap():
			if val := fd.MapValue(); val.Message() != nil {
				if err := w.checkMessage(val.Message()); err != nil {
					return err
				}
			}
		case fd.Message() != nil:
			if err := w.checkMessage(fd.Message()); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkFile refuses an editions file in the closure whose resolved
// json_format feature is not ALLOW. Editions 2023+ default json_format to
// ALLOW, so at file scope the only non-ALLOW resolution is an explicit
// features.json_format = LEGACY_BEST_EFFORT. Proto2/proto3 files are
// governed by the field-level checks alone (§3: proto3 always satisfies
// the range; proto2 is refused only for its groups and required fields).
func (w *closureWalker) checkFile(file protoreflect.FileDescriptor) error {
	if file == nil || file.Syntax() != protoreflect.Editions {
		return nil
	}
	if w.seenFiles[file.Path()] {
		return nil
	}
	w.seenFiles[file.Path()] = true

	fdp := protodesc.ToFileDescriptorProto(file)
	if fdp.GetOptions().GetFeatures().GetJsonFormat() == descriptorpb.FeatureSet_LEGACY_BEST_EFFORT {
		return fmt.Errorf(
			"editions file %q resolves json_format = LEGACY_BEST_EFFORT; the canonical JSON mapping requires ALLOW", file.Path())
	}
	return nil
}
