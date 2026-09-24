package openbindings

import (
	"net/netip"
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
		for _, c := range s[1:dot] {
			if !isHex(byte(c)) {
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
