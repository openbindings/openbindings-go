package connect

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/bufbuild/protocompile"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// This file is the connect family's embedded-schema lane. The family
// shares its protobuf schema layer with openbindings.grpc@1 by
// exact-identifier citation (openbindings.connect@1 §2, §3, §5): the two
// embedded schema carriages, their parse pins, and the accepted schema
// range are GRPC-D-01's, incorporated as CONN-D-01. The code transcribes
// formats/grpc/client.go's content lane rather than importing it — the
// modules stay independent — so the logic here must match grpc's.

type discovery struct {
	services []protoreflect.ServiceDescriptor
}

// discoverFromContent interprets an embedded content value per CONN-D-01's
// structural discrimination (incorporating GRPC-D-01, §3, §5): a string is
// single-file .proto source text; an object is a
// google.protobuf.FileDescriptorSet in canonical protobuf JSON. No other
// JSON type or shape is an accepted content value under this
// specification. ([]byte is the string carriage arriving from a Go caller.)
func discoverFromContent(ctx context.Context, content any) (*discovery, error) {
	switch c := content.(type) {
	case string:
		return compileProtoText(ctx, []byte(c))
	case []byte:
		return compileProtoText(ctx, c)
	case map[string]any:
		return discoverFromDescriptorSet(c)
	default:
		return nil, fmt.Errorf(
			"connect content must be single-file .proto source text (string) or a google.protobuf.FileDescriptorSet in canonical JSON (object), got %T (openbindings.connect@1 CONN-D-01)", content)
	}
}

// compileProtoText compiles embedded single-file .proto source text (the
// string carriage of openbindings.grpc@1 §3, via CONN-D-01). Its imports
// MUST be limited to the google/protobuf/* path prefix — the files the
// protobuf distribution bundles, resolved from this processor's own
// bundled copies; any other import is refused loudly at load.
func compileProtoText(ctx context.Context, raw []byte) (*discovery, error) {
	const fileName = "content.proto"
	compiler := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(&protocompile.SourceResolver{
			Accessor: func(path string) (io.ReadCloser, error) {
				if path == fileName {
					return io.NopCloser(bytes.NewReader(raw)), nil
				}
				// google/protobuf/* paths never reach here: the
				// WithStandardImports wrapper serves them from the
				// bundled copies when this accessor refuses.
				return nil, fmt.Errorf(
					"embedded .proto content may import only google/protobuf/* files; %q is refused (openbindings.grpc@1 §3, via openbindings.connect@1 CONN-D-01)", path)
			},
		}),
	}
	files, err := compiler.Compile(ctx, fileName)
	if err != nil {
		return nil, fmt.Errorf("parse proto: %w", err)
	}

	disc := &discovery{}
	for _, fd := range files {
		collectServices(disc, fd)
	}
	return disc, nil
}

// discoverFromDescriptorSet builds descriptors from a FileDescriptorSet
// carried in canonical protobuf JSON (the object carriage of
// openbindings.grpc@1 §3, via CONN-D-01): (i) unknown members in the
// content object are refused loudly, matching this family's input posture
// (protojson's own default); (ii) bracket-keyed extension members — the
// runtime convention for compiled custom options — are refused loudly:
// custom options never affect the JSON mapping or wire marshaling, so a
// conformant pin carries option-stripped descriptors. The set is the
// compiled, self-contained closure; a missing dependency is a loud load
// failure.
func discoverFromDescriptorSet(content map[string]any) (*discovery, error) {
	if err := refuseBracketKeys(content, "content"); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(content)
	if err != nil {
		return nil, fmt.Errorf("re-marshal descriptor-set content: %w", err)
	}
	var fds descriptorpb.FileDescriptorSet
	if err := protojson.Unmarshal(raw, &fds); err != nil {
		return nil, fmt.Errorf(
			"connect content is not a google.protobuf.FileDescriptorSet in canonical JSON: %v (openbindings.connect@1 CONN-D-01)", err)
	}
	files, err := protodesc.NewFiles(&fds)
	if err != nil {
		return nil, fmt.Errorf(
			"descriptor-set content does not form a self-contained closure: %v (openbindings.grpc@1 §3, via openbindings.connect@1 CONN-D-01)", err)
	}

	disc := &discovery{}
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		collectServices(disc, fd)
		return true
	})
	return disc, nil
}

// refuseBracketKeys walks a JSON tree and refuses any "[ext.name]" object
// member (object-carriage pin (ii) of openbindings.grpc@1 §3).
func refuseBracketKeys(v any, path string) error {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if strings.HasPrefix(k, "[") {
				return fmt.Errorf(
					"descriptor-set content carries bracket-keyed extension member %q at %s: custom options never affect the JSON mapping or wire marshaling, so a conformant pin carries option-stripped descriptors (openbindings.grpc@1 §3, via openbindings.connect@1 CONN-D-01)", k, path)
			}
			if err := refuseBracketKeys(val, path+"."+k); err != nil {
				return err
			}
		}
	case []any:
		for i, val := range t {
			if err := refuseBracketKeys(val, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func collectServices(disc *discovery, fd protoreflect.FileDescriptor) {
	services := fd.Services()
	for i := 0; i < services.Len(); i++ {
		disc.services = append(disc.services, services.Get(i))
	}
}

// discoverFromProto parses embedded content (per CONN-D-01) or a .proto
// file on disk (a synthesizer-lane convenience) and extracts service
// descriptors.
func discoverFromProto(ctx context.Context, location string, content any) (*discovery, error) {
	if content != nil {
		return discoverFromContent(ctx, content)
	}
	if location == "" {
		return nil, fmt.Errorf("proto source requires a location or content")
	}
	compiler := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(&protocompile.SourceResolver{}),
	}
	files, err := compiler.Compile(ctx, location)
	if err != nil {
		return nil, fmt.Errorf("parse proto: %w", err)
	}

	disc := &discovery{}
	for _, fd := range files {
		collectServices(disc, fd)
	}
	return disc, nil
}
