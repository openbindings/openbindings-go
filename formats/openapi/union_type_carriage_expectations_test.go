package openapi

// unionTypeCarriageExpectations is the frozen twin case table. The identical
// table is asserted by the TypeScript twin in
// openapi-client/typescript/src/union-type-carriage.test.ts; a cell that
// changes in one engine and not in the other fails there too.
//
// Reading a cell: `<edition>|<request media>|<type spelling>|<contentEncoding
// present?>` decides to `refused`, `missing-required-choice`, or `admitted`
// plus what one part carries for the probe value and what a supplied JSON
// null does.
//
// The rows this block moved, and why:
//   - Every 3.1 spelling with exactly one non-"null" member — string-null,
//     null-string, array-null, object-null, integer-null — now takes its
//     member's carriage and elides on null, because JSON Schema 2020-12
//     §6.1.1 makes the array a union and a one-non-null union asserts what
//     the collapsing anyOf spelling asserts. Sibling keywords keep applying,
//     each on its own terms: the contentEncoding column takes the
//     encoded-string row where the surviving member IS `string`, and
//     otherwise takes that member's own row. FIVE cells moved from refused to
//     admitted on 2026-08-17 for exactly that reason — 3.1.2 x
//     {integer-null, object-null} x both media, plus
//     3.1.2|urlencoded|array-null|contentEncoding — because
//     [JSON Schema 2020-12] §8.1 makes contentEncoding an annotation that
//     "does not function as a validation assertion" and §8.3 conditions it on
//     a string instance, while 3.1.1 and 3.1.2 hold `n/a` in the
//     contentEncoding column of the `number, integer, or boolean`, `object`
//     and `array` rows, defined by the table's own note as "the presence or
//     value of contentEncoding is irrelevant". Each now reads exactly as its
//     |plain twin, which is what that note means.
//   - Every spelling with two or more non-"null" members — string-object,
//     string-integer — refuses under both lines: value-dependent
//     alternatives leave no single faithful form carriage.
//   - Every multi-member spelling refuses under the 3.0 line whatever its
//     members, because all five 3.0 editions state "type - Value MUST be a
//     string. Multiple types via an array are not supported."
//   - A one-member or empty array raises no union question — it denotes
//     exactly the type it names, or none — and is read as that under both
//     lines. That tolerance is the engines' own, is identical in both, and
//     is why `string-array-1` is admitted at 3.0.4.
//   - Corrected 2026-08-26: a typeless 3.1 multipart part defaults to
//     application/octet-stream and uses the canonical-Base64 raw-octet lane.
//     The table's ordinary "x" probe is deliberately noncanonical and thus
//     reaches an invocation error after admission. A typeless 3.0 multipart
//     part remains represented but requires configuration.propertyMedia; this
//     harness supplies none and records missing-required-choice. Typeless
//     urlencoded cells still refuse because that lane has no octet boundary.
//     `empty-array` remains refused on the narrower ground that its explicit
//     empty type set admits no instance.
//
// R4 (ratified 2026-09-01): TWO cells moved from admitted to
// missing-required-choice -- 3.1.2|urlencoded|array-null on both spellings. A
// `type: ["array", "null"]` property collapses to `array`, and on the content
// lane the whole array rides one field. The editions derive an array's default
// from its ITEMS, here text/plain, under which no edition states an array's
// bytes; the prior application/json emission read the default off the
// container instead. The remedy is the section 9.3 `propertyMedia` choice,
// which this table supplies for no cell. The multipart sibling is unaffected:
// there the array expands into one part per item.
// Closed 2026-09-02: the one cell the two languages did not share,
// 3.1.2|multipart/form-data|boolean-true|plain, is gone with the row that held
// it. `anyOf: [{}, {not: {}}]` was carried as the "structural true spelling"
// because kin-openapi cannot hold a boolean Schema Object and the loader
// encodes a literal in that shape; the engines then read an AUTHORED copy of
// the shape as the literal too. Section 5.2 of both 3.x documents says
// otherwise: a choice skips only a branch "whose resolved declaration
// declares only `null`", `not` "never participate[s] in resolution", and the
// choice supplies a single resolved member "only when exactly one candidate
// remains" -- `{}` and `{not: {}}` are two candidates. The loader now marks
// its own encoding, an authored structure resolves to no single member, and
// the row is `ambiguous-choice`: refused in every cell, on both lines, in
// all three engines, exactly as `string-object` is.
var unionTypeCarriageExpectations = map[string]string{
	"3.0.4|application/x-www-form-urlencoded|absent-type|contentEncoding":      "refused",
	"3.0.4|application/x-www-form-urlencoded|absent-type|plain":                "refused",
	"3.0.4|application/x-www-form-urlencoded|array-null|contentEncoding":       "refused",
	"3.0.4|application/x-www-form-urlencoded|array-null|plain":                 "refused",
	"3.0.4|application/x-www-form-urlencoded|ambiguous-choice|contentEncoding": "refused",
	"3.0.4|application/x-www-form-urlencoded|ambiguous-choice|plain":           "refused",
	"3.0.4|application/x-www-form-urlencoded|empty-array|contentEncoding":      "refused",
	"3.0.4|application/x-www-form-urlencoded|empty-array|plain":                "refused",
	"3.0.4|application/x-www-form-urlencoded|integer-null|contentEncoding":     "refused",
	"3.0.4|application/x-www-form-urlencoded|integer-null|plain":               "refused",
	"3.0.4|application/x-www-form-urlencoded|memberless|contentEncoding":       "refused",
	"3.0.4|application/x-www-form-urlencoded|memberless|plain":                 "refused",
	"3.0.4|application/x-www-form-urlencoded|null-only|contentEncoding":        "refused",
	"3.0.4|application/x-www-form-urlencoded|null-only|plain":                  "refused",
	"3.0.4|application/x-www-form-urlencoded|null-string|contentEncoding":      "refused",
	"3.0.4|application/x-www-form-urlencoded|null-string|plain":                "refused",
	"3.0.4|application/x-www-form-urlencoded|object-null|contentEncoding":      "refused",
	"3.0.4|application/x-www-form-urlencoded|object-null|plain":                "refused",
	"3.0.4|application/x-www-form-urlencoded|string-array-1|contentEncoding":   "admitted;value=p=x;null=p=",
	"3.0.4|application/x-www-form-urlencoded|string-array-1|plain":             "admitted;value=p=x;null=p=",
	"3.0.4|application/x-www-form-urlencoded|string-integer|contentEncoding":   "refused",
	"3.0.4|application/x-www-form-urlencoded|string-integer|plain":             "refused",
	"3.0.4|application/x-www-form-urlencoded|string-null|contentEncoding":      "refused",
	"3.0.4|application/x-www-form-urlencoded|string-null|plain":                "refused",
	"3.0.4|application/x-www-form-urlencoded|string-object|contentEncoding":    "refused",
	"3.0.4|application/x-www-form-urlencoded|string-object|plain":              "refused",
	"3.0.4|application/x-www-form-urlencoded|string|contentEncoding":           "admitted;value=p=x;null=p=",
	"3.0.4|application/x-www-form-urlencoded|string|plain":                     "admitted;value=p=x;null=p=",
	"3.0.4|multipart/form-data|absent-type|contentEncoding":                    "missing-required-choice",
	"3.0.4|multipart/form-data|absent-type|plain":                              "missing-required-choice",
	"3.0.4|multipart/form-data|array-null|contentEncoding":                     "refused",
	"3.0.4|multipart/form-data|array-null|plain":                               "refused",
	"3.0.4|multipart/form-data|ambiguous-choice|contentEncoding":               "refused",
	"3.0.4|multipart/form-data|ambiguous-choice|plain":                         "refused",
	"3.0.4|multipart/form-data|empty-array|contentEncoding":                    "refused",
	"3.0.4|multipart/form-data|empty-array|plain":                              "refused",
	"3.0.4|multipart/form-data|integer-null|contentEncoding":                   "refused",
	"3.0.4|multipart/form-data|integer-null|plain":                             "refused",
	"3.0.4|multipart/form-data|memberless|contentEncoding":                     "missing-required-choice",
	"3.0.4|multipart/form-data|memberless|plain":                               "missing-required-choice",
	"3.0.4|multipart/form-data|null-only|contentEncoding":                      "refused",
	"3.0.4|multipart/form-data|null-only|plain":                                "refused",
	"3.0.4|multipart/form-data|null-string|contentEncoding":                    "refused",
	"3.0.4|multipart/form-data|null-string|plain":                              "refused",
	"3.0.4|multipart/form-data|object-null|contentEncoding":                    "refused",
	"3.0.4|multipart/form-data|object-null|plain":                              "refused",
	"3.0.4|multipart/form-data|string-array-1|contentEncoding":                 "admitted;value=text/plain:x;null=text/plain:",
	"3.0.4|multipart/form-data|string-array-1|plain":                           "admitted;value=text/plain:x;null=text/plain:",
	"3.0.4|multipart/form-data|string-integer|contentEncoding":                 "refused",
	"3.0.4|multipart/form-data|string-integer|plain":                           "refused",
	"3.0.4|multipart/form-data|string-null|contentEncoding":                    "refused",
	"3.0.4|multipart/form-data|string-null|plain":                              "refused",
	"3.0.4|multipart/form-data|string-object|contentEncoding":                  "refused",
	"3.0.4|multipart/form-data|string-object|plain":                            "refused",
	"3.0.4|multipart/form-data|string|contentEncoding":                         "admitted;value=text/plain:x;null=text/plain:",
	"3.0.4|multipart/form-data|string|plain":                                   "admitted;value=text/plain:x;null=text/plain:",
	"3.1.2|application/x-www-form-urlencoded|absent-type|contentEncoding":      "refused",
	"3.1.2|application/x-www-form-urlencoded|absent-type|plain":                "refused",
	"3.1.2|application/x-www-form-urlencoded|array-null|contentEncoding":       "missing-required-choice",
	"3.1.2|application/x-www-form-urlencoded|array-null|plain":                 "missing-required-choice",
	"3.1.2|application/x-www-form-urlencoded|ambiguous-choice|contentEncoding": "refused",
	"3.1.2|application/x-www-form-urlencoded|ambiguous-choice|plain":           "refused",
	"3.1.2|application/x-www-form-urlencoded|empty-array|contentEncoding":      "refused",
	"3.1.2|application/x-www-form-urlencoded|empty-array|plain":                "refused",
	"3.1.2|application/x-www-form-urlencoded|integer-null|contentEncoding":     "admitted;value=p=7;null=elided",
	"3.1.2|application/x-www-form-urlencoded|integer-null|plain":               "admitted;value=p=7;null=elided",
	"3.1.2|application/x-www-form-urlencoded|memberless|contentEncoding":       "refused",
	"3.1.2|application/x-www-form-urlencoded|memberless|plain":                 "refused",
	"3.1.2|application/x-www-form-urlencoded|null-only|contentEncoding":        "refused",
	"3.1.2|application/x-www-form-urlencoded|null-only|plain":                  "refused",
	"3.1.2|application/x-www-form-urlencoded|null-string|contentEncoding":      "admitted;value=p=x;null=elided",
	"3.1.2|application/x-www-form-urlencoded|null-string|plain":                "admitted;value=p=x;null=elided",
	"3.1.2|application/x-www-form-urlencoded|object-null|contentEncoding":      "admitted;value=p=%7B%22k%22%3A%22v%22%7D;null=elided",
	"3.1.2|application/x-www-form-urlencoded|object-null|plain":                "admitted;value=p=%7B%22k%22%3A%22v%22%7D;null=elided",
	"3.1.2|application/x-www-form-urlencoded|string-array-1|contentEncoding":   "admitted;value=p=x;null=error",
	"3.1.2|application/x-www-form-urlencoded|string-array-1|plain":             "admitted;value=p=x;null=p=",
	"3.1.2|application/x-www-form-urlencoded|string-integer|contentEncoding":   "refused",
	"3.1.2|application/x-www-form-urlencoded|string-integer|plain":             "refused",
	"3.1.2|application/x-www-form-urlencoded|string-null|contentEncoding":      "admitted;value=p=x;null=elided",
	"3.1.2|application/x-www-form-urlencoded|string-null|plain":                "admitted;value=p=x;null=elided",
	"3.1.2|application/x-www-form-urlencoded|string-object|contentEncoding":    "refused",
	"3.1.2|application/x-www-form-urlencoded|string-object|plain":              "refused",
	"3.1.2|application/x-www-form-urlencoded|string|contentEncoding":           "admitted;value=p=x;null=error",
	"3.1.2|application/x-www-form-urlencoded|string|plain":                     "admitted;value=p=x;null=p=",
	"3.1.2|multipart/form-data|absent-type|contentEncoding":                    "admitted;value=error;null=error",
	"3.1.2|multipart/form-data|absent-type|plain":                              "admitted;value=error;null=error",
	"3.1.2|multipart/form-data|array-null|contentEncoding":                     "admitted;value=text/plain:a;null=elided",
	"3.1.2|multipart/form-data|array-null|plain":                               "admitted;value=text/plain:a;null=elided",
	"3.1.2|multipart/form-data|ambiguous-choice|contentEncoding":               "refused",
	"3.1.2|multipart/form-data|ambiguous-choice|plain":                         "refused",
	"3.1.2|multipart/form-data|empty-array|contentEncoding":                    "refused",
	"3.1.2|multipart/form-data|empty-array|plain":                              "refused",
	"3.1.2|multipart/form-data|integer-null|contentEncoding":                   "admitted;value=text/plain:7;null=elided",
	"3.1.2|multipart/form-data|integer-null|plain":                             "admitted;value=text/plain:7;null=elided",
	"3.1.2|multipart/form-data|memberless|contentEncoding":                     "admitted;value=error;null=error",
	"3.1.2|multipart/form-data|memberless|plain":                               "admitted;value=error;null=error",
	"3.1.2|multipart/form-data|null-only|contentEncoding":                      "refused",
	"3.1.2|multipart/form-data|null-only|plain":                                "refused",
	"3.1.2|multipart/form-data|null-string|contentEncoding":                    "admitted;value=application/octet-stream:x;null=elided",
	"3.1.2|multipart/form-data|null-string|plain":                              "admitted;value=text/plain:x;null=elided",
	"3.1.2|multipart/form-data|object-null|contentEncoding":                    "admitted;value=application/json:{\"k\":\"v\"};null=elided",
	"3.1.2|multipart/form-data|object-null|plain":                              "admitted;value=application/json:{\"k\":\"v\"};null=elided",
	"3.1.2|multipart/form-data|string-array-1|contentEncoding":                 "admitted;value=application/octet-stream:x;null=error",
	"3.1.2|multipart/form-data|string-array-1|plain":                           "admitted;value=text/plain:x;null=text/plain:",
	"3.1.2|multipart/form-data|string-integer|contentEncoding":                 "refused",
	"3.1.2|multipart/form-data|string-integer|plain":                           "refused",
	"3.1.2|multipart/form-data|string-null|contentEncoding":                    "admitted;value=application/octet-stream:x;null=elided",
	"3.1.2|multipart/form-data|string-null|plain":                              "admitted;value=text/plain:x;null=elided",
	"3.1.2|multipart/form-data|string-object|contentEncoding":                  "refused",
	"3.1.2|multipart/form-data|string-object|plain":                            "refused",
	"3.1.2|multipart/form-data|string|contentEncoding":                         "admitted;value=application/octet-stream:x;null=error",
	"3.1.2|multipart/form-data|string|plain":                                   "admitted;value=text/plain:x;null=text/plain:",
}
