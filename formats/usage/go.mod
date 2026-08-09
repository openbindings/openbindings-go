module github.com/openbindings/openbindings-go/formats/usage

go 1.25.12

require (
	github.com/calico32/kdl-go v0.15.0
	github.com/openbindings/openbindings-go v0.2.0
)

require (
	github.com/dlclark/regexp2 v1.12.0 // indirect
	github.com/recolabs/gnata v0.2.2 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	github.com/tidwall/gjson v1.18.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/text v0.38.0 // indirect
)

// v0.1.0 predates the invocation-configuration rework: it claimed bare
// usage@ tokens while decoding stdout with a deleted payload-sniffing
// heuristic, so the same document behaves differently under it than under
// every later version. Retracted so consumers resolve to the reworked line.
retract v0.1.0
