package grpc

import (
	"context"
	"crypto/tls"
	"fmt"
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

func discover(ctx context.Context, address string) (*discovery, error) {
	conn, err := dial(ctx, address)
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

func dial(ctx context.Context, address string) (*grpc.ClientConn, error) {
	var opts []grpc.DialOption

	if needsTLS(address) {
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})))
	} else {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	target := address
	if !strings.Contains(address, "://") {
		target = "dns:///" + address
	}

	conn, err := grpc.NewClient(target, opts...)
	if err != nil {
		return nil, fmt.Errorf("dial %q: %w", address, err)
	}
	return conn, nil
}

func needsTLS(address string) bool {
	if strings.HasSuffix(address, ":443") {
		return true
	}
	if strings.HasPrefix(address, "https://") {
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
