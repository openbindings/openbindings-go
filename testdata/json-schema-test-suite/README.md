These files come from the JSON Schema Test Suite
(https://github.com/json-schema-org/JSON-Schema-Test-Suite), at commit
5b0ee1613e45fcc2bddac00e07c19cd49b00d8a8, under its MIT license (LICENSE here):

- tests/draft2020-12/ref.json, anchor.json, defs.json, dynamicRef.json,
  unevaluatedProperties.json, unevaluatedItems.json, and
  infinite-loop-detection.json;
- tests/draft2020-12/optional/id.json, as id.json.

They are unmodified. json_schema_test_suite_test.go runs each case through
an OBI document, to test how the SDK hands schemas to its schema library.
