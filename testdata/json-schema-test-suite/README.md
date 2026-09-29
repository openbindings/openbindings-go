draft2020-12/ is the tests/draft2020-12 folder of the JSON Schema Test Suite
(https://github.com/json-schema-org/JSON-Schema-Test-Suite), at commit
5b0ee1613e45fcc2bddac00e07c19cd49b00d8a8, under its MIT license (LICENSE
here). It is unmodified; to update it, replace the folder with the one at a
newer commit and change the commit named here.

json_schema_test_suite_test.go runs each case through an OBI document, to
test the SDK's whole path from a document to a verdict: how it hands schemas
to its schema library, and its own ECMA-262 pattern engine and handling of
numbers. Every file runs except two, which test what OBI-T-08 rules out:

- optional/format/ asserts format, which OBI-T-08 makes an annotation
  (format.json tests that it is one);
- optional/dependencies-compatibility.json evaluates dependencies, which
  strict 2020-12 does not define.

A case may reach no verdict only when its schema references one of the
suite's remote schemas (http://localhost:1234/...), which no document here
embeds; holds a Unicode property escape, which the SDK does not evaluate; or
references a value under a keyword that holds no schema.
