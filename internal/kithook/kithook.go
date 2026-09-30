// Package kithook lets the conformance kit (openbindingstest) reach what core
// does not export: the bundle core writes for a value contract, under each of
// the spellings the kit varies. Package openbindings sets it at init.
package kithook

import "encoding/json"

// Bundle returns the bundle core writes for an operation's input or output
// contract in contracts (a *openbindings.ValueContracts), spelled as spelling
// (0 or 1) says, or the refusal core gives instead.
var Bundle func(contracts any, operation, direction string, spelling int) (json.RawMessage, error)
