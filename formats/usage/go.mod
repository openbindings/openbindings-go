module github.com/openbindings/openbindings-go/formats/usage

go 1.25.6

require (
	github.com/google/shlex v0.0.0-20191202100458-e7afc7fbc510
	github.com/openbindings/openbindings-go v0.2.0
	github.com/sblinch/kdl-go v0.0.0-20260120205643-17a91a33fe63
)

// v0.1.0 predates the invocation-configuration rework: it claimed bare
// usage@ tokens while decoding stdout with a deleted payload-sniffing
// heuristic, so the same document behaves differently under it than under
// every later version. Retracted so consumers resolve to the reworked line.
retract v0.1.0
