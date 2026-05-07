package openbindings

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"golang.org/x/net/idna"
)

// defaultPorts maps URI schemes to their default port strings, used by
// CanonicalizeLocation's scheme-based normalization.
var defaultPorts = map[string]string{
	"http":  "80",
	"https": "443",
	"ws":    "80",
	"wss":   "443",
	"ftp":   "21",
}

// CanonicalizeLocation produces the canonical form of a URI per spec §10
// (Location Equality). Two URIs refer to the same OBI iff their canonical
// forms are byte-equal.
//
// The function applies, in order:
//
//  1. Bare absolute filesystem paths are lifted to file:// URIs per RFC 8089.
//  2. Host labels are converted to A-label form per UTS #46.
//  3. Scheme and host are lowercased; percent-encoding of unreserved
//     characters is decoded; remaining percent escapes have their hex
//     digits uppercased; dot-segments are removed (RFC 3986 §6.2.2).
//  4. The default port for the scheme is removed; an empty path becomes
//     "/" when the URI has an authority (RFC 3986 §6.2.3).
//  5. The fragment component is stripped.
//
// Path and query case, query strings, userinfo, scheme (http vs https),
// and trailing slashes on non-empty paths remain significant per the
// spec. The URI used for equality is the declared URI (or caller-supplied
// base), regardless of any HTTP redirects encountered during fetching.
//
// Non-hierarchical URIs (e.g., mailto:, urn:) are returned with only the
// scheme lowercased; they are outside this spec's canonicalization rules.
func CanonicalizeLocation(uri string) (string, error) {
	if uri == "" {
		return "", errors.New("openbindings: cannot canonicalize empty URI")
	}

	if strings.HasPrefix(uri, "/") {
		uri = "file://" + uri
	}

	// Apply percent-encoding normalization on the raw input first, so that
	// reserved %XX sequences (e.g., %2F) are preserved through url.Parse.
	// url.Parse decodes the path into u.Path, which would otherwise lose
	// the encoded form on round-trip.
	uri = normalizePercentEncoding(uri)

	u, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("openbindings: parse URI %q: %w", uri, err)
	}
	if u.Scheme == "" {
		return "", fmt.Errorf("openbindings: %q has no scheme and is not an absolute path", uri)
	}

	scheme := strings.ToLower(u.Scheme)

	if u.Opaque != "" {
		// Non-hierarchical URI (e.g., mailto:foo). Out of scope for the
		// spec's canonicalization steps; lowercase the scheme and return.
		return scheme + ":" + u.Opaque, nil
	}

	var host string
	if u.Host != "" {
		hostName, port := splitHostPort(u.Host)
		ascii, err := idna.Lookup.ToASCII(hostName)
		if err != nil {
			return "", fmt.Errorf("openbindings: IDN conversion %q: %w", hostName, err)
		}
		ascii = strings.ToLower(ascii)
		if port != "" {
			if def, ok := defaultPorts[scheme]; ok && port == def {
				port = ""
			}
		}
		if port == "" {
			host = ascii
		} else {
			host = ascii + ":" + port
		}
	}

	// EscapedPath returns the percent-encoded form: u.RawPath if it is a
	// valid encoding of u.Path (preserving any non-default encoding the
	// caller used, such as %2F for a literal slash), otherwise the default
	// encoding of u.Path. Dot-segment patterns don't intersect with %XX,
	// so removal is correct on the encoded form.
	rawPath := u.EscapedPath()
	rawPath = removeDotSegments(rawPath)
	if host != "" && rawPath == "" {
		rawPath = "/"
	}

	// Reassemble manually so the encoded form survives. u.String() would
	// re-encode from the decoded u.Path and lose %2F-style reserved escapes.
	var b strings.Builder
	b.Grow(len(uri))
	b.WriteString(scheme)
	b.WriteString("://")
	if u.User != nil {
		b.WriteString(u.User.String())
		b.WriteByte('@')
	}
	b.WriteString(host)
	b.WriteString(rawPath)
	if u.RawQuery != "" {
		b.WriteByte('?')
		b.WriteString(u.RawQuery)
	}
	return b.String(), nil
}

// ResolveRef resolves a relative URI reference against a base URI per
// RFC 3986 §5 Reference Resolution. This is the spec §12 operation: it
// converts a roles[*] value, sources[*].location value, or schema $ref
// into a fully-qualified URI suitable for fetching or comparison.
//
// Resolution is directory-relative: the merge step strips everything
// after the last "/" in the base URI's path before appending the
// reference (RFC 3986 §5.2.3).
//
// An absolute reference is returned unchanged. A relative reference
// requires a non-empty absolute base; otherwise ResolveRef returns an
// error. Callers loading documents without a canonical retrieval URI
// (e.g., from stdin or memory) may pass any caller-supplied absolute
// base. JSON Pointer fragments (RFC 6901) are preserved by url.URL's
// reference-resolution semantics without further handling.
func ResolveRef(base, ref string) (string, error) {
	if ref == "" {
		return "", errors.New("openbindings: cannot resolve empty reference")
	}
	refURL, err := url.Parse(ref)
	if err != nil {
		return "", fmt.Errorf("openbindings: parse reference %q: %w", ref, err)
	}
	if refURL.IsAbs() {
		return ref, nil
	}
	if base == "" {
		return "", fmt.Errorf("openbindings: cannot resolve relative reference %q without a base URI", ref)
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("openbindings: parse base %q: %w", base, err)
	}
	if !baseURL.IsAbs() {
		return "", fmt.Errorf("openbindings: base %q is not absolute", base)
	}
	resolved := baseURL.ResolveReference(refURL)
	return resolved.String(), nil
}

// removeDotSegments implements RFC 3986 §5.2.4 to remove "." and ".."
// segments from a URI path component. Returns the cleaned path.
func removeDotSegments(input string) string {
	if input == "" {
		return ""
	}
	var output strings.Builder
	output.Grow(len(input))
	for len(input) > 0 {
		switch {
		case strings.HasPrefix(input, "../"):
			input = input[3:]
		case strings.HasPrefix(input, "./"):
			input = input[2:]
		case strings.HasPrefix(input, "/./"):
			input = "/" + input[3:]
		case input == "/.":
			input = "/"
		case strings.HasPrefix(input, "/../"):
			input = "/" + input[4:]
			truncateLastSegment(&output)
		case input == "/..":
			input = "/"
			truncateLastSegment(&output)
		case input == "." || input == "..":
			input = ""
		default:
			start := 0
			if input[0] == '/' {
				start = 1
			}
			end := strings.IndexByte(input[start:], '/')
			if end < 0 {
				output.WriteString(input)
				input = ""
			} else {
				output.WriteString(input[:start+end])
				input = input[start+end:]
			}
		}
	}
	return output.String()
}

// truncateLastSegment drops the last "/segment" from b's accumulated string.
// If b has no "/", b becomes empty.
func truncateLastSegment(b *strings.Builder) {
	s := b.String()
	idx := strings.LastIndexByte(s, '/')
	b.Reset()
	if idx >= 0 {
		b.WriteString(s[:idx])
	}
}

// splitHostPort splits a host:port pair. IPv6 hosts retain their brackets.
// If hostport has no port, port is "".
func splitHostPort(hostport string) (host, port string) {
	if strings.HasPrefix(hostport, "[") {
		end := strings.Index(hostport, "]")
		if end < 0 {
			return hostport, ""
		}
		host = hostport[:end+1]
		if end+1 < len(hostport) && hostport[end+1] == ':' {
			port = hostport[end+2:]
		}
		return host, port
	}
	if i := strings.LastIndex(hostport, ":"); i >= 0 {
		return hostport[:i], hostport[i+1:]
	}
	return hostport, ""
}

// normalizePercentEncoding decodes percent-encoded unreserved characters
// (RFC 3986 §2.3: ALPHA / DIGIT / "-" / "." / "_" / "~") and uppercases
// the hex digits in any remaining percent escapes. Part of RFC 3986 §6.2.2
// syntax-based normalization.
func normalizePercentEncoding(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '%' || i+2 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		hi, hiOK := hexNibble(s[i+1])
		lo, loOK := hexNibble(s[i+2])
		if !hiOK || !loOK {
			b.WriteByte(s[i])
			continue
		}
		c := byte(hi<<4 | lo)
		if isUnreserved(c) {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(hexUpper(s[i+1]))
			b.WriteByte(hexUpper(s[i+2]))
		}
		i += 2
	}
	return b.String()
}

func isUnreserved(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z':
		return true
	case c >= 'a' && c <= 'z':
		return true
	case c >= '0' && c <= '9':
		return true
	case c == '-' || c == '.' || c == '_' || c == '~':
		return true
	}
	return false
}

func hexNibble(c byte) (int, bool) {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0'), true
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10, true
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10, true
	}
	return 0, false
}

func hexUpper(c byte) byte {
	if c >= 'a' && c <= 'f' {
		return c - 'a' + 'A'
	}
	return c
}
