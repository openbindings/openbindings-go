package grpc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"

	"github.com/bufbuild/protocompile"
	"github.com/jhump/protoreflect/v2/grpcreflect"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	refv1 "google.golang.org/grpc/reflection/grpc_reflection_v1"
	refv1alpha "google.golang.org/grpc/reflection/grpc_reflection_v1alpha"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

type discovery struct {
	services []protoreflect.ServiceDescriptor
	address  string
}

// dialAddress is a parsed dial address per openbindings.grpc@1 §4
// (GRPC-D-02): the scheme-stripped host:port handed to the resolver, plus
// the address-form transport determination (the transport configuration
// point's default, GRPC-P-02).
type dialAddress struct {
	hostPort string
	tls      bool
}

// parseDialAddress validates a dial address against §4's three
// port-explicit forms (GRPC-D-02):
//
//	host:port          TLS iff the port is 443, plaintext otherwise
//	grpc://host:port   plaintext
//	grpcs://host:port  TLS
//
// host is a DNS name, an IPv4 literal, or a bracketed IPv6 literal. No
// other scheme spelling is defined (one spelling per meaning; the pre-spec
// http:// and https:// affordances are cut), no port is defaulted, and
// path, query, fragment, and userinfo components are not part of a dial
// address — a location carrying any of them is not conformant.
func parseDialAddress(address string) (dialAddress, error) {
	rest := address
	var schemeTLS, schemePlain bool
	switch {
	case strings.HasPrefix(address, "grpc://"):
		rest = strings.TrimPrefix(address, "grpc://")
		schemePlain = true
	case strings.HasPrefix(address, "grpcs://"):
		rest = strings.TrimPrefix(address, "grpcs://")
		schemeTLS = true
	case strings.Contains(address, "://"):
		return dialAddress{}, fmt.Errorf(
			"grpc dial address %q uses an undefined scheme: the only defined forms are host:port, grpc://host:port (plaintext), and grpcs://host:port (TLS) (openbindings.grpc@1 §4, GRPC-D-02)", address)
	}

	for _, bad := range []struct{ char, component string }{
		{"/", "path"}, {"?", "query"}, {"#", "fragment"}, {"@", "userinfo"},
	} {
		if strings.Contains(rest, bad.char) {
			return dialAddress{}, fmt.Errorf(
				"grpc dial address %q carries a %s component, which is not part of a dial address (openbindings.grpc@1 §4, GRPC-D-02)", address, bad.component)
		}
	}

	host, port, err := net.SplitHostPort(rest)
	if err != nil || host == "" || port == "" {
		return dialAddress{}, fmt.Errorf(
			"grpc dial address %q is not host:port with an explicit port — this specification defaults no ports (openbindings.grpc@1 §4, GRPC-D-02)", address)
	}
	for i := 0; i < len(port); i++ {
		if port[i] < '0' || port[i] > '9' {
			return dialAddress{}, fmt.Errorf(
				"grpc dial address %q has non-numeric port %q (openbindings.grpc@1 §4, GRPC-D-02)", address, port)
		}
	}

	return dialAddress{
		hostPort: rest,
		tls:      schemeTLS || (!schemePlain && port == "443"),
	}, nil
}

// defaultTransport returns the address-form transport determination
// (GRPC-P-02's default): TLS or plaintext per §4's table.
func defaultTransport(addr dialAddress) credentials.TransportCredentials {
	if addr.tls {
		return credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	}
	return insecure.NewCredentials()
}

// resolveTransport resolves the transport configuration point (§9.3,
// GRPC-P-02), consulted per-invocation/consumer configuration
// (context.configuration.transport — the operation invoker merges both
// tiers into one carriage) → invoker-level credentials
// (WithTransportCredentials) → the §4 address-form determination. An
// override replaces the address-form determination ENTIRELY: it wins even
// against an explicit grpc:// or grpcs:// scheme (an override is an
// override). The returned tag keys the connection cache: one channel per
// (target, transport identity) pair.
func resolveTransport(cfg map[string]any, invokerCreds credentials.TransportCredentials, addr dialAddress) (credentials.TransportCredentials, string, error) {
	if raw, ok := cfg["transport"]; ok && raw != nil {
		return transportFromConfig(raw)
	}
	if invokerCreds != nil {
		return invokerCreds, "invoker", nil
	}
	if addr.tls {
		return defaultTransport(addr), "tls", nil
	}
	return defaultTransport(addr), "plaintext", nil
}

// transportFromConfig interprets a configuration.transport value: the
// string elections "plaintext" and "tls", or an object carrying channel
// security material — {"ca": <PEM>, "clientCert": <PEM>, "clientKey":
// <PEM>, "serverName": <string>} — for a custom CA or a mutual-TLS
// identity. Unknown members are refused loudly, matching this family's
// input posture.
func transportFromConfig(raw any) (credentials.TransportCredentials, string, error) {
	switch v := raw.(type) {
	case string:
		switch v {
		case "plaintext":
			return insecure.NewCredentials(), "cfg:plaintext", nil
		case "tls":
			return credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12}), "cfg:tls", nil
		default:
			return nil, "", fmt.Errorf(
				"configuration.transport %q is not a defined election: use \"plaintext\", \"tls\", or an object with ca/clientCert/clientKey/serverName (openbindings.grpc@1 §9.3)", v)
		}
	case map[string]any:
		tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
		for key := range v {
			switch key {
			case "ca", "clientCert", "clientKey", "serverName":
			default:
				return nil, "", fmt.Errorf(
					"configuration.transport carries unknown member %q: the accepted members are ca, clientCert, clientKey, and serverName (openbindings.grpc@1 §9.3)", key)
			}
		}
		str := func(key string) (string, error) {
			raw, ok := v[key]
			if !ok || raw == nil {
				return "", nil
			}
			s, ok := raw.(string)
			if !ok {
				return "", fmt.Errorf("configuration.transport.%s must be a string, got %T", key, raw)
			}
			return s, nil
		}
		ca, err := str("ca")
		if err != nil {
			return nil, "", err
		}
		if ca != "" {
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM([]byte(ca)) {
				return nil, "", fmt.Errorf("configuration.transport.ca carries no parseable PEM certificate")
			}
			tlsCfg.RootCAs = pool
		}
		cert, err := str("clientCert")
		if err != nil {
			return nil, "", err
		}
		key, err := str("clientKey")
		if err != nil {
			return nil, "", err
		}
		if (cert == "") != (key == "") {
			return nil, "", fmt.Errorf("configuration.transport requires clientCert and clientKey together for a mutual-TLS identity")
		}
		if cert != "" {
			pair, err := tls.X509KeyPair([]byte(cert), []byte(key))
			if err != nil {
				return nil, "", fmt.Errorf("configuration.transport client identity does not parse: %v", err)
			}
			tlsCfg.Certificates = []tls.Certificate{pair}
		}
		serverName, err := str("serverName")
		if err != nil {
			return nil, "", err
		}
		tlsCfg.ServerName = serverName

		// Cache tag: a stable digest of the configured material, so
		// distinct identities never share a channel.
		digest, err := json.Marshal(v) // map keys marshal sorted: stable
		if err != nil {
			return nil, "", fmt.Errorf("configuration.transport does not marshal: %v", err)
		}
		return credentials.NewTLS(tlsCfg), fmt.Sprintf("cfg:%x", sha256Sum(digest)), nil
	default:
		return nil, "", fmt.Errorf(
			"configuration.transport must be \"plaintext\", \"tls\", or an object with channel security material, got %T (openbindings.grpc@1 §9.3)", raw)
	}
}

func sha256Sum(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}

// dial opens a (lazy) channel to a parsed dial address with the resolved
// transport credentials. The scheme was stripped during parsing (§4: the
// scheme expresses the transport choice and is stripped before dialing).
func dial(addr dialAddress, creds credentials.TransportCredentials, extra []grpc.DialOption) (*grpc.ClientConn, error) {
	opts := []grpc.DialOption{grpc.WithTransportCredentials(creds)}
	opts = append(opts, extra...)
	conn, err := grpc.NewClient("dns:///"+addr.hostPort, opts...)
	if err != nil {
		return nil, fmt.Errorf("dial %q: %w", addr.hostPort, err)
	}
	return conn, nil
}

// connKey builds the connection-cache key: one cached channel per
// (dial target, transport identity) pair, since the transport
// configuration point can vary per invocation (§9.3).
func connKey(hostPort, transportTag string) string {
	return hostPort + "|" + transportTag
}

// reflClient wraps grpcreflect with GRPC-P-01's version selection: the
// grpc.reflection.v1.ServerReflection service first, retrying against
// v1alpha iff the v1 info stream failed with status UNIMPLEMENTED. Any
// other v1 status is that source's failure, not a version signal.
// (grpcreflect.NewClientAuto is close but also falls back on UNAVAILABLE,
// which GRPC-P-01 forbids, so the selection lives here.)
type reflClient struct {
	ctx        context.Context
	conn       grpc.ClientConnInterface
	client     *grpcreflect.Client
	triedAlpha bool
}

func newReflClient(ctx context.Context, conn grpc.ClientConnInterface) *reflClient {
	return &reflClient{
		ctx:    ctx,
		conn:   conn,
		client: grpcreflect.NewClientV1(ctx, refv1.NewServerReflectionClient(conn)),
	}
}

// fallForward switches to v1alpha iff the v1 failure is UNIMPLEMENTED and
// v1alpha has not been tried. Reports whether the caller should retry.
func (r *reflClient) fallForward(err error) bool {
	if r.triedAlpha || status.Code(err) != codes.Unimplemented {
		return false
	}
	r.client.Reset()
	r.client = grpcreflect.NewClientV1Alpha(r.ctx, refv1alpha.NewServerReflectionClient(r.conn))
	r.triedAlpha = true
	return true
}

func (r *reflClient) ListServices() ([]protoreflect.FullName, error) {
	names, err := r.client.ListServices()
	if err != nil && r.fallForward(err) {
		names, err = r.client.ListServices()
	}
	return names, err
}

func (r *reflClient) FileContainingSymbol(name protoreflect.FullName) (protoreflect.FileDescriptor, error) {
	fd, err := r.client.FileContainingSymbol(name)
	if err != nil && r.fallForward(err) {
		fd, err = r.client.FileContainingSymbol(name)
	}
	return fd, err
}

func (r *reflClient) Reset() { r.client.Reset() }

// discover lists a live server's services via Server Reflection
// (GRPC-P-01). Used by the synthesizer lane; the invocation lane resolves
// one symbol via resolveService instead.
func discover(ctx context.Context, address string, cfg dialConfig) (*discovery, error) {
	// A local FILE lands here because only content (or a .proto location)
	// dispatches to the parse lanes. Dialing a file path yields an
	// unrelated resolver error; name the remedy instead.
	if fi, statErr := os.Stat(address); statErr == nil && fi.Mode().IsRegular() {
		return nil, fmt.Errorf(
			"grpc source %q is a local file, not a dial address; raw binary descriptor sets (.pb/.binpb) have no carriage under openbindings.grpc@1 — embed the schema as content (single-file .proto source text, or a FileDescriptorSet in canonical JSON) with the server's host:port as location, or use a live reflection address (§3, §4)", address)
	}
	addr, err := parseDialAddress(address)
	if err != nil {
		return nil, err
	}
	creds := cfg.creds
	if creds == nil {
		creds = defaultTransport(addr)
	}
	conn, err := dial(addr, creds, cfg.extra)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	refClient := newReflClient(ctx, conn)
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

// resolveService asks the reflection server for the file containing a
// service symbol and extracts the matching ServiceDescriptor.
func resolveService(client *reflClient, name protoreflect.FullName) (protoreflect.ServiceDescriptor, error) {
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
// The zero value means automatic behavior (the §4 address-form
// determination, no extra options).
type dialConfig struct {
	// creds, when set, is the invoker-level transport identity (mTLS
	// client certificates, a custom CA pool, forced plaintext). Per §9.3
	// it replaces the address-form determination entirely, and is itself
	// displaced by a per-invocation configuration.transport value.
	creds credentials.TransportCredentials
	// extra dial options append after the defaults; grpc-go's own
	// last-wins semantics apply where an option overlaps a default.
	extra []grpc.DialOption
}

func isInfraService(name string) bool {
	return strings.HasPrefix(name, "grpc.reflection.") ||
		strings.HasPrefix(name, "grpc.health.")
}

// discoverFromContent interprets an embedded content value per GRPC-D-01's
// structural discrimination (§3, §5): a string is single-file .proto
// source text; an object is a google.protobuf.FileDescriptorSet in
// canonical protobuf JSON. No other JSON type or shape is an accepted
// content value. ([]byte is the string carriage arriving from a Go caller.)
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
			"grpc content must be single-file .proto source text (string) or a google.protobuf.FileDescriptorSet in canonical JSON (object), got %T (openbindings.grpc@1 GRPC-D-01)", content)
	}
}

// compileProtoText compiles embedded single-file .proto source text (§3
// string carriage). Its imports MUST be limited to the google/protobuf/*
// path prefix, resolved from this processor's bundled copies (the standard
// imports the protobuf distribution bundles, descriptor.proto included);
// any other import is refused loudly at load.
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
					"embedded .proto content may import only google/protobuf/* files; %q is refused (openbindings.grpc@1 §3)", path)
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
// carried in canonical protobuf JSON (§3 object carriage, GRPC-D-01):
// (i) unknown members in the content object are refused loudly, matching
// this family's input posture (protojson's own default); (ii)
// bracket-keyed extension members — the runtime convention for compiled
// custom options — are refused loudly: custom options never affect the
// JSON mapping or wire marshaling, so a conformant pin carries
// option-stripped descriptors. The set is the compiled, self-contained
// closure; a missing dependency is a loud load failure.
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
			"grpc content is not a google.protobuf.FileDescriptorSet in canonical JSON: %v (openbindings.grpc@1 GRPC-D-01)", err)
	}
	files, err := protodesc.NewFiles(&fds)
	if err != nil {
		return nil, fmt.Errorf(
			"descriptor-set content does not form a self-contained closure: %v (openbindings.grpc@1 §3)", err)
	}

	disc := &discovery{}
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		collectServices(disc, fd)
		return true
	})
	return disc, nil
}

// refuseBracketKeys walks a JSON tree and refuses any "[ext.name]" object
// member (§3 object-carriage pin (ii)).
func refuseBracketKeys(v any, path string) error {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if strings.HasPrefix(k, "[") {
				return fmt.Errorf(
					"descriptor-set content carries bracket-keyed extension member %q at %s: custom options never affect the JSON mapping or wire marshaling, so a conformant pin carries option-stripped descriptors (openbindings.grpc@1 §3)", k, path)
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
		svc := services.Get(i)
		if !isInfraService(string(svc.FullName())) {
			disc.services = append(disc.services, svc)
		}
	}
}

// discoverFromProto parses embedded content (per GRPC-D-01) or a .proto
// file on disk (a synthesizer-lane convenience) and extracts service
// descriptors without connecting to a live server.
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

// isProtoFile checks if a source location looks like a .proto file path.
func isProtoFile(location string) bool {
	return strings.HasSuffix(location, ".proto")
}
