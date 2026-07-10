package grpc

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"strings"

	"github.com/bufbuild/protocompile"
	"github.com/jhump/protoreflect/v2/grpcreflect"
	openbindings "github.com/openbindings/openbindings-go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type discovery struct {
	services []protoreflect.ServiceDescriptor
	address  string
}

func discover(ctx context.Context, address string, cfg dialConfig) (*discovery, error) {
	// A local FILE that isn't a .proto (a compiled FileDescriptorSet, say)
	// lands here because only .proto dispatches to the parse lane. Dialing a
	// file path yields an unrelated resolver error; name the gap instead.
	if fi, statErr := os.Stat(address); statErr == nil && fi.Mode().IsRegular() {
		return nil, fmt.Errorf(
			"grpc source %q is a local file, not a reflection address; compiled descriptor sets (.pb/.binpb) are not yet a supported source — use the .proto source file or a live host:port reflection address", address)
	}
	conn, err := dial(ctx, address, cfg)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	refClient := grpcreflect.NewClientAuto(ctx, conn)
	defer refClient.Reset()

	serviceNames, err := refClient.ListServices()
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}

	disc := &discovery{address: address}
	for _, name := range serviceNames {
		if isInfraService(string(name)) {
			continue
		}
		svcDesc, err := resolveService(refClient, name)
		if err != nil {
			return nil, fmt.Errorf("resolve service %q: %w", name, err)
		}
		disc.services = append(disc.services, svcDesc)
	}

	return disc, nil
}

// resolveService asks the reflection server for the file containing a service
// symbol and extracts the matching ServiceDescriptor. v2's grpcreflect.Client
// (unlike the v1 wrapper) returns the FileDescriptor; we walk it to find the
// named service.
func resolveService(client *grpcreflect.Client, name protoreflect.FullName) (protoreflect.ServiceDescriptor, error) {
	file, err := client.FileContainingSymbol(name)
	if err != nil {
		return nil, err
	}
	if svc := findServiceInFile(file, name); svc != nil {
		return svc, nil
	}
	return nil, fmt.Errorf("service %q not found in file %q", name, file.Path())
}

func findServiceInFile(file protoreflect.FileDescriptor, name protoreflect.FullName) protoreflect.ServiceDescriptor {
	services := file.Services()
	for i := 0; i < services.Len(); i++ {
		svc := services.Get(i)
		if svc.FullName() == name {
			return svc
		}
	}
	return nil
}

// dialConfig carries caller-supplied transport configuration into dial.
// The zero value means automatic behavior (TLS auto-detection, no extras).
type dialConfig struct {
	// creds, when set, is the caller-owned transport identity (mTLS client
	// certificates, a custom CA pool, forced plaintext). It replaces the
	// automatic TLS detection entirely: a caller who states the transport
	// identity owns it.
	creds credentials.TransportCredentials
	// extra dial options append after the defaults; grpc-go's own last-wins
	// semantics apply where an option overlaps a default.
	extra []grpc.DialOption
}

func dial(ctx context.Context, address string, cfg dialConfig) (*grpc.ClientConn, error) {
	var opts []grpc.DialOption

	switch {
	case cfg.creds != nil:
		opts = append(opts, grpc.WithTransportCredentials(cfg.creds))
	case needsTLS(address):
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})))
	default:
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	opts = append(opts, cfg.extra...)

	// An https:// (or grpcs://) prefix is the documented way to force TLS
	// off port 443; it is OUR affordance, not gRPC target syntax, so the
	// scheme is stripped before the address reaches the resolver
	// (grpc.NewClient reads "https://host:port" as scheme https plus a
	// malformed authority: "too many colons in address").
	target := stripTLSScheme(address)
	if !strings.Contains(target, "://") {
		target = "dns:///" + target
	}

	conn, err := grpc.NewClient(target, opts...)
	if err != nil {
		return nil, fmt.Errorf("dial %q: %w", address, err)
	}
	return conn, nil
}

func stripTLSScheme(address string) string {
	for _, scheme := range []string{"https://", "grpcs://"} {
		if strings.HasPrefix(address, scheme) {
			return strings.TrimPrefix(address, scheme)
		}
	}
	return address
}

func needsTLS(address string) bool {
	if strings.HasSuffix(address, ":443") {
		return true
	}
	if strings.HasPrefix(address, "https://") || strings.HasPrefix(address, "grpcs://") {
		return true
	}
	return false
}

func isInfraService(name string) bool {
	return strings.HasPrefix(name, "grpc.reflection.") ||
		strings.HasPrefix(name, "grpc.health.")
}

// discoverFromProto parses a .proto file (or inline content) and extracts
// service descriptors without connecting to a live server. Uses protocompile
// (the v2-native successor to jhump's protoparse, maintained by Buf).
func discoverFromProto(ctx context.Context, location string, content any) (*discovery, error) {
	var compiler protocompile.Compiler
	var fileName string

	if content != nil {
		raw, convErr := openbindings.ContentToBytes(content)
		if convErr != nil {
			return nil, fmt.Errorf("convert proto content: %w", convErr)
		}
		fileName = "inline.proto"
		compiler = protocompile.Compiler{
			Resolver: &protocompile.SourceResolver{
				Accessor: protocompile.SourceAccessorFromMap(map[string]string{
					fileName: string(raw),
				}),
			},
		}
	} else if location != "" {
		fileName = location
		compiler = protocompile.Compiler{
			Resolver: &protocompile.SourceResolver{},
		}
	} else {
		return nil, fmt.Errorf("proto source requires a location or content")
	}

	files, err := compiler.Compile(ctx, fileName)
	if err != nil {
		return nil, fmt.Errorf("parse proto: %w", err)
	}

	disc := &discovery{}
	for _, fd := range files {
		services := fd.Services()
		for i := 0; i < services.Len(); i++ {
			svc := services.Get(i)
			if !isInfraService(string(svc.FullName())) {
				disc.services = append(disc.services, svc)
			}
		}
	}
	return disc, nil
}

// isProtoFile checks if a source location looks like a .proto file path.
func isProtoFile(location string) bool {
	return strings.HasSuffix(location, ".proto")
}
