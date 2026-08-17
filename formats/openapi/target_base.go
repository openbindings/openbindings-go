package openapi

import "strings"

// This file answers one question for §9.3 of openbindings.openapi@1: does a
// server URL, after Server Variable substitution, denote a target address?
//
// It exists because the answer used to come from the HOST LANGUAGE's URL
// parser — Go's net/url here, the WHATWG URL parser in the TypeScript twin —
// and neither is an authority this specification incorporates. The two
// disagree, and the corpus caught both halves in opposite directions: net/url
// rejects a server URL carrying an unsubstituted template variable and accepts
// one whose http authority has no host, while the WHATWG parser does the
// reverse. Each engine was right about one shape by accident.
//
// The predicate below is derived from the two authorities instead. A server
// URL denotes a target address iff
//
//  1. it matches RFC 3986's URI production — `scheme ":" hier-part [ "?" query ]
//     [ "#" fragment ]`, Appendix A, Collected ABNF — so it carries a scheme and
//     every character is drawn from the productions that ABNF defines; and
//  2. when its scheme is http or https (compared ASCII case-insensitively), its
//     authority carries a NON-EMPTY host, per RFC 9110 §4.2.1 and §4.2.2: "A
//     sender MUST NOT generate an 'http' URI with an empty host identifier. A
//     recipient that processes such a URI reference MUST reject it as invalid."
//
// Both are incorporated: §2 of the binding specification incorporates RFC 9110
// "for plain HTTP semantics", and §9.3 and §11 name RFC 3986 for server-URL
// resolution. This is a restatement of an incorporated consequence, not a
// convention of this implementation's own.
//
// Deliberately RFC 3986's `URI` and not its `absolute-URI`: the two differ only
// in the optional fragment, and refusing a fragment-bearing server URL is a
// separate per-edition question that only OAS 3.1.2 speaks to ("Query and
// fragment MUST NOT be part of this URL", §4.8.5.1, absent from the other seven
// accepted editions). Whether a URL carries an address, and whether the artifact
// was allowed to write it, are different questions.
//
// Equally deliberately, nothing here repairs the artifact's value: a space is
// not percent-encoded, a non-ASCII host is not IDNA-mapped, and an unsubstituted
// `{name}` is not guessed at. A server URL that is not a URI leaves the target
// undetermined, and §9.3 sends that to the `server` configuration point.
//
// Twinned byte-for-byte with the TypeScript implementation in
// openapi-client/typescript/src/servers.ts and pinned by the shared case table
// testdata/server-target-base-cases.json.

// denotesTargetBase reports whether s is a URI under RFC 3986 that also
// satisfies RFC 9110's host requirement for the http and https schemes.
func denotesTargetBase(s string) bool {
	colon := strings.IndexByte(s, ':')
	if colon <= 0 {
		return false
	}
	if !isURIScheme(s[:colon]) {
		return false
	}
	scheme := strings.ToLower(s[:colon])
	rest := s[colon+1:]

	// URI = scheme ":" hier-part [ "?" query ] [ "#" fragment ]. A "#" cannot
	// appear inside hier-part or query, and a "?" cannot appear inside
	// hier-part, so the first occurrence of each delimits the component.
	if hash := strings.IndexByte(rest, '#'); hash >= 0 {
		// fragment = *( pchar / "/" / "?" )
		if !isCharRun(rest[hash+1:], pcharPlusSlashQuestion) {
			return false
		}
		rest = rest[:hash]
	}
	if question := strings.IndexByte(rest, '?'); question >= 0 {
		// query = *( pchar / "/" / "?" )
		if !isCharRun(rest[question+1:], pcharPlusSlashQuestion) {
			return false
		}
		rest = rest[:question]
	}

	host := ""
	hasAuthority := false
	if strings.HasPrefix(rest, "//") {
		// hier-part = "//" authority path-abempty
		after := rest[2:]
		authority := after
		path := ""
		if slash := strings.IndexByte(after, '/'); slash >= 0 {
			authority, path = after[:slash], after[slash:]
		}
		// path-abempty = *( "/" segment ); empty, or begins with "/".
		if path != "" && !isCharRun(path, pcharPlusSlash) {
			return false
		}
		parsedHost, ok := uriAuthorityHost(authority)
		if !ok {
			return false
		}
		host, hasAuthority = parsedHost, true
	} else {
		// hier-part = path-absolute / path-rootless / path-empty. All three are
		// pchar-and-"/" runs; path-absolute additionally forbids a leading "//",
		// which is unreachable here because that prefix took the branch above.
		if rest != "" && !isCharRun(rest, pcharPlusSlash) {
			return false
		}
	}

	if scheme == "http" || scheme == "https" {
		// RFC 9110 §4.2.1 / §4.2.2.
		if !hasAuthority || host == "" {
			return false
		}
	}
	return true
}

// uriAuthorityHost splits an RFC 3986 authority into its host and validates
// every component: authority = [ userinfo "@" ] host [ ":" port ].
func uriAuthorityHost(authority string) (string, bool) {
	// userinfo admits ":" but not "@", so the LAST "@" ends it.
	if at := strings.LastIndexByte(authority, '@'); at >= 0 {
		// userinfo = *( unreserved / pct-encoded / sub-delims / ":" )
		if !isCharRun(authority[:at], userinfoChar) {
			return "", false
		}
		authority = authority[at+1:]
	}
	if strings.HasPrefix(authority, "[") {
		// IP-literal = "[" ( IPv6address / IPvFuture ) "]"
		closing := strings.IndexByte(authority, ']')
		if closing < 0 || !isIPLiteralInner(authority[1:closing]) {
			return "", false
		}
		if !isURIPort(authority[closing+1:]) {
			return "", false
		}
		return authority[:closing+1], true
	}
	// Neither reg-name nor IPv4address admits ":", so the LAST ":" starts the
	// port. reg-name subsumes IPv4address character-wise, so one check covers
	// both alternatives of `host`.
	host := authority
	remainder := ""
	if colon := strings.LastIndexByte(authority, ':'); colon >= 0 {
		host, remainder = authority[:colon], authority[colon:]
	}
	if !isCharRun(host, regNameChar) || !isURIPort(remainder) {
		return "", false
	}
	return host, true
}

// isURIPort accepts an empty remainder or ":" *DIGIT.
func isURIPort(remainder string) bool {
	if remainder == "" {
		return true
	}
	if remainder[0] != ':' {
		return false
	}
	for i := 1; i < len(remainder); i++ {
		if remainder[i] < '0' || remainder[i] > '9' {
			return false
		}
	}
	return true
}

// isIPLiteralInner accepts IPv6address or IPvFuture, the two alternatives
// RFC 3986 admits between the square brackets.
func isIPLiteralInner(inner string) bool {
	if strings.HasPrefix(inner, "v") || strings.HasPrefix(inner, "V") {
		// IPvFuture = "v" 1*HEXDIG "." 1*( unreserved / sub-delims / ":" )
		dot := strings.IndexByte(inner, '.')
		if dot < 2 || dot+1 >= len(inner) {
			return false
		}
		for i := 1; i < dot; i++ {
			if !isHexDigit(inner[i]) {
				return false
			}
		}
		return isCharRun(inner[dot+1:], ipvFutureTailChar)
	}
	return isIPv6Address(inner)
}

// isIPv6Address checks RFC 3986's IPv6address alternatives: eight h16 groups,
// with at most one "::" eliding one or more of them, and an optional trailing
// IPv4address occupying the last two.
func isIPv6Address(s string) bool {
	if s == "" {
		return false
	}
	head, tail := s, ""
	elided := false
	if i := strings.Index(s, "::"); i >= 0 {
		if strings.Contains(s[i+2:], "::") {
			return false
		}
		head, tail, elided = s[:i], s[i+2:], true
	}
	headGroups, ok := ipv6Groups(head)
	if !ok {
		return false
	}
	tailGroups, ok := ipv6Groups(tail)
	if !ok {
		return false
	}
	if elided {
		return headGroups+tailGroups <= 7
	}
	return headGroups == 8
}

// ipv6Groups counts the h16 groups in one colon-separated run, allowing the
// last one to be an IPv4address (which occupies two groups). An empty run is
// zero groups.
func ipv6Groups(run string) (int, bool) {
	if run == "" {
		return 0, true
	}
	parts := strings.Split(run, ":")
	count := 0
	for i, part := range parts {
		if i == len(parts)-1 && strings.Contains(part, ".") {
			if !isIPv4Address(part) {
				return 0, false
			}
			count += 2
			continue
		}
		if len(part) < 1 || len(part) > 4 {
			return 0, false
		}
		for j := 0; j < len(part); j++ {
			if !isHexDigit(part[j]) {
				return 0, false
			}
		}
		count++
	}
	return count, true
}

// isIPv4Address checks RFC 3986's dec-octet "." dec-octet "." dec-octet "."
// dec-octet, which forbids a leading zero on a multi-digit octet.
func isIPv4Address(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	for _, part := range parts {
		if len(part) < 1 || len(part) > 3 {
			return false
		}
		if len(part) > 1 && part[0] == '0' {
			return false
		}
		value := 0
		for i := 0; i < len(part); i++ {
			if part[i] < '0' || part[i] > '9' {
				return false
			}
			value = value*10 + int(part[i]-'0')
		}
		if value > 255 {
			return false
		}
	}
	return true
}

// isURIScheme checks scheme = ALPHA *( ALPHA / DIGIT / "+" / "-" / "." ).
func isURIScheme(s string) bool {
	if s == "" || !isAlpha(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if !isAlpha(c) && !isDigit(c) && c != '+' && c != '-' && c != '.' {
			return false
		}
	}
	return true
}

// The character classes below are RFC 3986 Appendix A's, named for the
// production each one serves.
func unreservedChar(c byte) bool {
	return isAlpha(c) || isDigit(c) || c == '-' || c == '.' || c == '_' || c == '~'
}

func subDelimChar(c byte) bool {
	switch c {
	case '!', '$', '&', '\'', '(', ')', '*', '+', ',', ';', '=':
		return true
	}
	return false
}

func regNameChar(c byte) bool { return unreservedChar(c) || subDelimChar(c) }

func userinfoChar(c byte) bool { return regNameChar(c) || c == ':' }

func ipvFutureTailChar(c byte) bool { return unreservedChar(c) || subDelimChar(c) || c == ':' }

func pcharPlusSlash(c byte) bool { return userinfoChar(c) || c == '@' || c == '/' }

func pcharPlusSlashQuestion(c byte) bool { return pcharPlusSlash(c) || c == '?' }

// isCharRun reports whether every byte of s is admitted by allow, treating a
// "%" as the start of a pct-encoded triplet. Percent is in no character class,
// so this is the one place the grammar is not a pure per-byte test.
func isCharRun(s string, allow func(byte) bool) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '%' {
			if i+2 >= len(s) || !isHexDigit(s[i+1]) || !isHexDigit(s[i+2]) {
				return false
			}
			i += 2
			continue
		}
		if !allow(s[i]) {
			return false
		}
	}
	return true
}

func isAlpha(c byte) bool { return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') }

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isHexDigit(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// hasURIScheme reports whether s begins with an RFC 3986 scheme followed by
// ":". A string that does is never a relative reference — path-noscheme forbids
// a colon in a relative reference's first segment — so resolving it against the
// artifact's base URI cannot rescue it. It names an address, and the address
// does not exist.
func hasURIScheme(s string) bool {
	colon := strings.IndexByte(s, ':')
	return colon > 0 && isURIScheme(s[:colon])
}
