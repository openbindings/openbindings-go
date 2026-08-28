package openapi

import openapiclient "github.com/openbindings/openapi-client/go"

// denotesTargetBase remains for the shared twin table; the RFC 3986/9110
// predicate is owned and executed by the standalone client.
func denotesTargetBase(value string) bool { return openapiclient.IsServerBaseURL(value) }
