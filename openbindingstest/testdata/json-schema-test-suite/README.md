draft2020-12/ is the tests/draft2020-12 folder of the JSON Schema Test Suite
(https://github.com/json-schema-org/JSON-Schema-Test-Suite), and remotes/ its
remotes folder, at commit 5b0ee1613e45fcc2bddac00e07c19cd49b00d8a8, under its
MIT license (LICENSE here). Both are unmodified; to update them, replace the
folders with the ones at a newer commit and change the commit named here.

The conformance kit (openbindingstest) runs each suite schema as an OBI
document's input contract, with the remote fixtures supplied as resources
under their http://localhost:1234/ URIs, through core and on the evaluator
directly. Every file runs except two, which test what OBI-T-08 rules out:

- optional/format/ asserts format, which OBI-T-08 makes an annotation
  (format.json tests that it is one);
- optional/dependencies-compatibility.json evaluates dependencies, which
  strict 2020-12 does not define.

A group core refuses is pinned in suite.go, with its reason; an evaluator
names in its Options what it cannot decide or locate.
