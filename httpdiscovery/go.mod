module github.com/openbindings/openbindings-go/httpdiscovery

go 1.25.12

toolchain go1.25.13

require github.com/openbindings/openbindings-go v0.2.0

require (
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3 // indirect
	golang.org/x/text v0.39.0 // indirect
)

// Develop against the sibling core until the required release is tagged.
// RELEASING.md orders the tags and removal of this replacement.
replace github.com/openbindings/openbindings-go => ../
