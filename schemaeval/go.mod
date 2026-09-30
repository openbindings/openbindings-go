module github.com/openbindings/openbindings-go/schemaeval

go 1.25.12

toolchain go1.25.13

require (
	github.com/dlclark/regexp2/v2 v2.7.1
	github.com/openbindings/openbindings-go v0.2.0
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3
	golang.org/x/text v0.39.0
)

require golang.org/x/sync v0.21.0

// Until the core module is tagged with the API this module implements,
// develop against the core beside it. RELEASING.md orders the tags.
replace github.com/openbindings/openbindings-go => ../
