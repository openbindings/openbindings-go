package openbindings

import (
	"net/netip"
	"regexp"
	"strings"
)

// uriReference reports whether s is a URI-reference as RFC 3986 §4.1 defines
// it, and whether it is a URI, one with a scheme (§3), rather than a
// relative reference (§4.2).
func uriReference(s string) (wellFormed, hasScheme bool) {
	rest := s
	if i := strings.IndexByte(rest, '#'); i >= 0 {
		if !uriChars(rest[i+1:], "/?") {
			return false, false
		}
		rest = rest[:i]
	}
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		if !uriChars(rest[i+1:], "/?") {
			return false, false
		}
		rest = rest[:i]
	}
	if i := strings.IndexByte(rest, ':'); i >= 0 && !strings.ContainsAny(rest[:i], "/") {
		// A colon before any slash ends a scheme; a relative reference's first
		// segment cannot hold one (path-noscheme).
		if !isScheme(rest[:i]) {
			return false, false
		}
		return hierPart(rest[i+1:]), true
	}
	return hierPart(rest), false
}

// hierPart checks hier-part and relative-part: "//" authority path-abempty,
// or a path.
func hierPart(s string) bool {
	if strings.HasPrefix(s, "//") {
		s = s[2:]
		end := strings.IndexByte(s, '/')
		if end < 0 {
			end = len(s)
		}
		return authority(s[:end]) && uriChars(s[end:], "/")
	}
	return uriChars(s, "/")
}

// authority checks [ userinfo "@" ] host [ ":" port ].
func authority(s string) bool {
	if i := strings.IndexByte(s, '@'); i >= 0 {
		if !uriChars(s[:i], "") || strings.ContainsAny(s[:i], "@") {
			return false
		}
		s = s[i+1:]
	}
	host, port := s, ""
	if strings.HasPrefix(s, "[") {
		end := strings.IndexByte(s, ']')
		if end < 0 || !ipLiteral(s[1:end]) {
			return false
		}
		host, port = "", s[end+1:]
		if port != "" {
			if port[0] != ':' {
				return false
			}
			port = port[1:]
		}
	} else if i := strings.IndexByte(s, ':'); i >= 0 {
		host, port = s[:i], s[i+1:]
	}
	for _, c := range port {
		if c < '0' || c > '9' {
			return false
		}
	}
	// reg-name: unreserved, pct-encoded, and sub-delims. IPv4address is a
	// reg-name spelling.
	return uriChars(host, "") && !strings.ContainsAny(host, ":@")
}

// ipLiteral checks the inside of an IP-literal: an IPv6address or IPvFuture.
func ipLiteral(s string) bool {
	if len(s) > 1 && (s[0] == 'v' || s[0] == 'V') {
		dot := strings.IndexByte(s, '.')
		if dot < 2 || dot == len(s)-1 {
			return false
		}
		for i := 1; i < dot; i++ {
			if !isHex(s[i]) {
				return false
			}
		}
		return uriChars(s[dot+1:], "") && !strings.ContainsAny(s[dot+1:], "%@")
	}
	address, err := netip.ParseAddr(s)
	return err == nil && address.Is6() && address.Zone() == "" && strings.Contains(s, ":")
}

func isScheme(s string) bool {
	if s == "" || !isAlpha(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		if c := s[i]; !isAlpha(c) && !isDigit(c) && c != '+' && c != '-' && c != '.' {
			return false
		}
	}
	return true
}

// uriChars reports whether s holds only pchar (unreserved, pct-encoded,
// sub-delims, ":", "@") and the extra characters given.
func uriChars(s, extra string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '%':
			if i+2 >= len(s) || !isHex(s[i+1]) || !isHex(s[i+2]) {
				return false
			}
			i += 2
		case isAlpha(c) || isDigit(c) || strings.IndexByte("-._~!$&'()*+,;=:@", c) >= 0:
		case strings.IndexByte(extra, c) >= 0:
		default:
			return false
		}
	}
	return true
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func isAlpha(c byte) bool { return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') }

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// uriParts is a URI-reference split into its five components (RFC 3986
// Appendix B), each with whether it is defined, since an empty component and
// an undefined one differ.
type uriParts struct {
	scheme, authority, path, query, fragment       string
	hasScheme, hasAuthority, hasQuery, hasFragment bool
}

// uriSplit is the regular expression of RFC 3986 Appendix B.
var uriSplit = regexp.MustCompile(`^(([^:/?#]+):)?(//([^/?#]*))?([^?#]*)(\?([^#]*))?(#(.*))?$`)

// splitURI splits a URI-reference into its components (RFC 3986 Appendix B).
func splitURI(s string) uriParts {
	m := uriSplit.FindStringSubmatchIndex(s)
	part := func(group int) (string, bool) {
		if m[2*group] < 0 {
			return "", false
		}
		return s[m[2*group]:m[2*group+1]], true
	}
	var p uriParts
	p.scheme, p.hasScheme = part(2)
	p.authority, p.hasAuthority = part(4)
	p.path, _ = part(5)
	p.query, p.hasQuery = part(7)
	p.fragment, p.hasFragment = part(9)
	return p
}

// String recomposes a URI-reference from its components (RFC 3986 §5.3).
func (p uriParts) String() string {
	var b strings.Builder
	if p.hasScheme {
		b.WriteString(p.scheme + ":")
	}
	if p.hasAuthority {
		b.WriteString("//" + p.authority)
	}
	b.WriteString(p.path)
	if p.hasQuery {
		b.WriteString("?" + p.query)
	}
	if p.hasFragment {
		b.WriteString("#" + p.fragment)
	}
	return b.String()
}

// resolveURIReference transforms a reference into its target URI against a
// base, strictly as RFC 3986 §5.2.2 does, without normalizing anything but
// dot segments.
func resolveURIReference(base, ref uriParts) uriParts {
	var t uriParts
	switch {
	case ref.hasScheme:
		t = ref
		t.path = removeDotSegmentsStrict(ref.path)
	case ref.hasAuthority:
		t = ref
		t.path = removeDotSegmentsStrict(ref.path)
		t.scheme, t.hasScheme = base.scheme, base.hasScheme
	default:
		switch {
		case ref.path == "":
			t.path = base.path
			t.query, t.hasQuery = base.query, base.hasQuery
			if ref.hasQuery {
				t.query, t.hasQuery = ref.query, true
			}
		case strings.HasPrefix(ref.path, "/"):
			t.path = removeDotSegmentsStrict(ref.path)
			t.query, t.hasQuery = ref.query, ref.hasQuery
		default:
			t.path = removeDotSegmentsStrict(mergePaths(base, ref.path))
			t.query, t.hasQuery = ref.query, ref.hasQuery
		}
		t.authority, t.hasAuthority = base.authority, base.hasAuthority
		t.scheme, t.hasScheme = base.scheme, base.hasScheme
	}
	t.fragment, t.hasFragment = ref.fragment, ref.hasFragment
	return t
}

// mergePaths merges a relative-path reference with a base's path (RFC 3986
// §5.2.3).
func mergePaths(base uriParts, path string) string {
	if base.hasAuthority && base.path == "" {
		return "/" + path
	}
	return base.path[:strings.LastIndexByte(base.path, '/')+1] + path
}

// removeDotSegmentsStrict removes the "." and ".." segments of a path by the
// algorithm of RFC 3986 §5.2.4.
func removeDotSegmentsStrict(input string) string {
	var output strings.Builder
	for input != "" {
		switch {
		case strings.HasPrefix(input, "../"):
			input = input[3:]
		case strings.HasPrefix(input, "./"):
			input = input[2:]
		case strings.HasPrefix(input, "/./"):
			input = input[2:]
		case input == "/.":
			input = "/"
		case strings.HasPrefix(input, "/../") || input == "/..":
			if input == "/.." {
				input = "/"
			} else {
				input = input[3:]
			}
			out := output.String()
			output.Reset()
			if i := strings.LastIndexByte(out, '/'); i >= 0 {
				output.WriteString(out[:i])
			}
		case input == "." || input == "..":
			input = ""
		default:
			end := strings.IndexByte(input[1:], '/')
			if end < 0 {
				end = len(input)
			} else {
				end++
			}
			output.WriteString(input[:end])
			input = input[end:]
		}
	}
	return output.String()
}
