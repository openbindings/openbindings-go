package grpc

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"

	"github.com/jhump/protoreflect/desc"            //nolint:staticcheck // no v2 equivalent yet
	"github.com/jhump/protoreflect/desc/protoparse"  //nolint:staticcheck // no v2 equivalent yet
	"github.com/jhump/protoreflect/grpcreflect"      //nolint:staticcheck // depends on protoreflect/desc
	openbindings "github.com/openbindings/openbindings-go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

type discovery struct {
	services []*desc.ServiceDescriptor
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
		if isInfraService(name) {
			continue
		}
		svcDesc, err := refClient.ResolveService(name)
		if err != nil {
			return nil, fmt.Errorf("resolve service %q: %w", name, err)
		}
		disc.services = append(disc.services, svcDesc)
	}

	return disc, nil
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
// service descriptors without connecting to a live server.
func discoverFromProto(location string, content any) (*discovery, error) {
	var fds []*desc.FileDescriptor
	var err error

	if content != nil {
		// Parse inline proto content.
		raw, convErr := openbindings.ContentToBytes(content)
		if convErr != nil {
			return nil, fmt.Errorf("convert proto content: %w", convErr)
		}
		p := protoparse.Parser{
			Accessor: protoparse.FileContentsFromMap(map[string]string{
				"inline.proto": string(raw),
			}),
		}
		fds, err = p.ParseFiles("inline.proto")
	} else if location != "" {
		p := protoparse.Parser{}
		fds, err = p.ParseFiles(location)
	} else {
		return nil, fmt.Errorf("proto source requires a location or content")
	}

	if err != nil {
		return nil, fmt.Errorf("parse proto: %w", err)
	}

	disc := &discovery{}
	for _, fd := range fds {
		for _, svc := range fd.GetServices() {
			if !isInfraService(svc.GetFullyQualifiedName()) {
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
