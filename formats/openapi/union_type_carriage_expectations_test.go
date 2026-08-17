package openapi

// unionTypeCarriageExpectations is the frozen twin case table. The identical
// table is asserted by the TypeScript twin in
// openapi-client/typescript/src/union-type-carriage.test.ts; a cell that
// changes in one engine and not in the other fails there too.
//
// Reading a cell: `<edition>|<request media>|<type spelling>|<contentEncoding
// present?>` decides to `refused`, or to `admitted` plus what one part
// carries for a canonical value of the declared type and what a supplied
// JSON null does.
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
//   - EIGHT cells moved from admitted to refused on 2026-08-17, all at
//     3.1.2, all in the |plain column: `absent-type`, `memberless` and
//     `boolean-true` (the structural true spelling) declare no `type` at
//     all, and every accepted 3.1 edition states that part's default
//     Content-Type as application/octet-stream — 3.1.1 and 3.1.2 as the
//     Encoding Object default table's `type`-absent first row, 3.1.0
//     through the total catch-all closing its prose enumeration — which
//     this revision defines no JSON-to-octet part boundary to cross.
//     `empty-array` refuses on a different and narrower ground: its `type`
//     is present, so no stated row reaches it at all, and JSON Schema
//     2020-12's meta-schema requires an array-valued `type` to carry at
//     least one member, so the declaration admits no instance. All eight
//     are admitted at 3.0.4, where no stated row reaches a declaration
//     carrying no `type` and §9.2's own convention answers.
var unionTypeCarriageExpectations = map[string]string{
	"3.0.4|application/x-www-form-urlencoded|absent-type|contentEncoding":    "admitted;value=p=x;null=error",
	"3.0.4|application/x-www-form-urlencoded|absent-type|plain":              "admitted;value=p=x;null=error",
	"3.0.4|application/x-www-form-urlencoded|array-null|contentEncoding":     "refused",
	"3.0.4|application/x-www-form-urlencoded|array-null|plain":               "refused",
	"3.0.4|application/x-www-form-urlencoded|boolean-true|contentEncoding":   "refused",
	"3.0.4|application/x-www-form-urlencoded|boolean-true|plain":             "admitted;value=p=x;null=error",
	"3.0.4|application/x-www-form-urlencoded|empty-array|contentEncoding":    "admitted;value=p=x;null=error",
	"3.0.4|application/x-www-form-urlencoded|empty-array|plain":              "admitted;value=p=x;null=error",
	"3.0.4|application/x-www-form-urlencoded|integer-null|contentEncoding":   "refused",
	"3.0.4|application/x-www-form-urlencoded|integer-null|plain":             "refused",
	"3.0.4|application/x-www-form-urlencoded|memberless|contentEncoding":     "admitted;value=p=x;null=error",
	"3.0.4|application/x-www-form-urlencoded|memberless|plain":               "admitted;value=p=x;null=error",
	"3.0.4|application/x-www-form-urlencoded|null-only|contentEncoding":      "refused",
	"3.0.4|application/x-www-form-urlencoded|null-only|plain":                "refused",
	"3.0.4|application/x-www-form-urlencoded|null-string|contentEncoding":    "refused",
	"3.0.4|application/x-www-form-urlencoded|null-string|plain":              "refused",
	"3.0.4|application/x-www-form-urlencoded|object-null|contentEncoding":    "refused",
	"3.0.4|application/x-www-form-urlencoded|object-null|plain":              "refused",
	"3.0.4|application/x-www-form-urlencoded|string-array-1|contentEncoding": "admitted;value=p=x;null=p=",
	"3.0.4|application/x-www-form-urlencoded|string-array-1|plain":           "admitted;value=p=x;null=p=",
	"3.0.4|application/x-www-form-urlencoded|string-integer|contentEncoding": "refused",
	"3.0.4|application/x-www-form-urlencoded|string-integer|plain":           "refused",
	"3.0.4|application/x-www-form-urlencoded|string-null|contentEncoding":    "refused",
	"3.0.4|application/x-www-form-urlencoded|string-null|plain":              "refused",
	"3.0.4|application/x-www-form-urlencoded|string-object|contentEncoding":  "refused",
	"3.0.4|application/x-www-form-urlencoded|string-object|plain":            "refused",
	"3.0.4|application/x-www-form-urlencoded|string|contentEncoding":         "admitted;value=p=x;null=p=",
	"3.0.4|application/x-www-form-urlencoded|string|plain":                   "admitted;value=p=x;null=p=",
	"3.0.4|multipart/form-data|absent-type|contentEncoding":                  "admitted;value=text/plain:x;null=error",
	"3.0.4|multipart/form-data|absent-type|plain":                            "admitted;value=text/plain:x;null=error",
	"3.0.4|multipart/form-data|array-null|contentEncoding":                   "refused",
	"3.0.4|multipart/form-data|array-null|plain":                             "refused",
	"3.0.4|multipart/form-data|boolean-true|contentEncoding":                 "refused",
	"3.0.4|multipart/form-data|boolean-true|plain":                           "admitted;value=text/plain:x;null=error",
	"3.0.4|multipart/form-data|empty-array|contentEncoding":                  "admitted;value=text/plain:x;null=error",
	"3.0.4|multipart/form-data|empty-array|plain":                            "admitted;value=text/plain:x;null=error",
	"3.0.4|multipart/form-data|integer-null|contentEncoding":                 "refused",
	"3.0.4|multipart/form-data|integer-null|plain":                           "refused",
	"3.0.4|multipart/form-data|memberless|contentEncoding":                   "admitted;value=text/plain:x;null=error",
	"3.0.4|multipart/form-data|memberless|plain":                             "admitted;value=text/plain:x;null=error",
	"3.0.4|multipart/form-data|null-only|contentEncoding":                    "refused",
	"3.0.4|multipart/form-data|null-only|plain":                              "refused",
	"3.0.4|multipart/form-data|null-string|contentEncoding":                  "refused",
	"3.0.4|multipart/form-data|null-string|plain":                            "refused",
	"3.0.4|multipart/form-data|object-null|contentEncoding":                  "refused",
	"3.0.4|multipart/form-data|object-null|plain":                            "refused",
	"3.0.4|multipart/form-data|string-array-1|contentEncoding":               "admitted;value=text/plain:x;null=text/plain:",
	"3.0.4|multipart/form-data|string-array-1|plain":                         "admitted;value=text/plain:x;null=text/plain:",
	"3.0.4|multipart/form-data|string-integer|contentEncoding":               "refused",
	"3.0.4|multipart/form-data|string-integer|plain":                         "refused",
	"3.0.4|multipart/form-data|string-null|contentEncoding":                  "refused",
	"3.0.4|multipart/form-data|string-null|plain":                            "refused",
	"3.0.4|multipart/form-data|string-object|contentEncoding":                "refused",
	"3.0.4|multipart/form-data|string-object|plain":                          "refused",
	"3.0.4|multipart/form-data|string|contentEncoding":                       "admitted;value=text/plain:x;null=text/plain:",
	"3.0.4|multipart/form-data|string|plain":                                 "admitted;value=text/plain:x;null=text/plain:",
	"3.1.2|application/x-www-form-urlencoded|absent-type|contentEncoding":    "refused",
	"3.1.2|application/x-www-form-urlencoded|absent-type|plain":              "refused",
	"3.1.2|application/x-www-form-urlencoded|array-null|contentEncoding":     "admitted;value=p=%5B%22a%22%5D;null=elided",
	"3.1.2|application/x-www-form-urlencoded|array-null|plain":               "admitted;value=p=%5B%22a%22%5D;null=elided",
	"3.1.2|application/x-www-form-urlencoded|boolean-true|contentEncoding":   "refused",
	"3.1.2|application/x-www-form-urlencoded|boolean-true|plain":             "refused",
	"3.1.2|application/x-www-form-urlencoded|empty-array|contentEncoding":    "refused",
	"3.1.2|application/x-www-form-urlencoded|empty-array|plain":              "refused",
	"3.1.2|application/x-www-form-urlencoded|integer-null|contentEncoding":   "admitted;value=p=7;null=elided",
	"3.1.2|application/x-www-form-urlencoded|integer-null|plain":             "admitted;value=p=7;null=elided",
	"3.1.2|application/x-www-form-urlencoded|memberless|contentEncoding":     "refused",
	"3.1.2|application/x-www-form-urlencoded|memberless|plain":               "refused",
	"3.1.2|application/x-www-form-urlencoded|null-only|contentEncoding":      "refused",
	"3.1.2|application/x-www-form-urlencoded|null-only|plain":                "refused",
	"3.1.2|application/x-www-form-urlencoded|null-string|contentEncoding":    "admitted;value=p=x;null=elided",
	"3.1.2|application/x-www-form-urlencoded|null-string|plain":              "admitted;value=p=x;null=elided",
	"3.1.2|application/x-www-form-urlencoded|object-null|contentEncoding":    "admitted;value=p=%7B%22k%22%3A%22v%22%7D;null=elided",
	"3.1.2|application/x-www-form-urlencoded|object-null|plain":              "admitted;value=p=%7B%22k%22%3A%22v%22%7D;null=elided",
	"3.1.2|application/x-www-form-urlencoded|string-array-1|contentEncoding": "admitted;value=p=x;null=error",
	"3.1.2|application/x-www-form-urlencoded|string-array-1|plain":           "admitted;value=p=x;null=p=",
	"3.1.2|application/x-www-form-urlencoded|string-integer|contentEncoding": "refused",
	"3.1.2|application/x-www-form-urlencoded|string-integer|plain":           "refused",
	"3.1.2|application/x-www-form-urlencoded|string-null|contentEncoding":    "admitted;value=p=x;null=elided",
	"3.1.2|application/x-www-form-urlencoded|string-null|plain":              "admitted;value=p=x;null=elided",
	"3.1.2|application/x-www-form-urlencoded|string-object|contentEncoding":  "refused",
	"3.1.2|application/x-www-form-urlencoded|string-object|plain":            "refused",
	"3.1.2|application/x-www-form-urlencoded|string|contentEncoding":         "admitted;value=p=x;null=error",
	"3.1.2|application/x-www-form-urlencoded|string|plain":                   "admitted;value=p=x;null=p=",
	"3.1.2|multipart/form-data|absent-type|contentEncoding":                  "refused",
	"3.1.2|multipart/form-data|absent-type|plain":                            "refused",
	"3.1.2|multipart/form-data|array-null|contentEncoding":                   "admitted;value=text/plain:a;null=elided",
	"3.1.2|multipart/form-data|array-null|plain":                             "admitted;value=text/plain:a;null=elided",
	"3.1.2|multipart/form-data|boolean-true|contentEncoding":                 "refused",
	"3.1.2|multipart/form-data|boolean-true|plain":                           "refused",
	"3.1.2|multipart/form-data|empty-array|contentEncoding":                  "refused",
	"3.1.2|multipart/form-data|empty-array|plain":                            "refused",
	"3.1.2|multipart/form-data|integer-null|contentEncoding":                 "admitted;value=text/plain:7;null=elided",
	"3.1.2|multipart/form-data|integer-null|plain":                           "admitted;value=text/plain:7;null=elided",
	"3.1.2|multipart/form-data|memberless|contentEncoding":                   "refused",
	"3.1.2|multipart/form-data|memberless|plain":                             "refused",
	"3.1.2|multipart/form-data|null-only|contentEncoding":                    "refused",
	"3.1.2|multipart/form-data|null-only|plain":                              "refused",
	"3.1.2|multipart/form-data|null-string|contentEncoding":                  "admitted;value=application/octet-stream:x;null=elided",
	"3.1.2|multipart/form-data|null-string|plain":                            "admitted;value=text/plain:x;null=elided",
	"3.1.2|multipart/form-data|object-null|contentEncoding":                  "admitted;value=application/json:{\"k\":\"v\"};null=elided",
	"3.1.2|multipart/form-data|object-null|plain":                            "admitted;value=application/json:{\"k\":\"v\"};null=elided",
	"3.1.2|multipart/form-data|string-array-1|contentEncoding":               "admitted;value=application/octet-stream:x;null=error",
	"3.1.2|multipart/form-data|string-array-1|plain":                         "admitted;value=text/plain:x;null=text/plain:",
	"3.1.2|multipart/form-data|string-integer|contentEncoding":               "refused",
	"3.1.2|multipart/form-data|string-integer|plain":                         "refused",
	"3.1.2|multipart/form-data|string-null|contentEncoding":                  "admitted;value=application/octet-stream:x;null=elided",
	"3.1.2|multipart/form-data|string-null|plain":                            "admitted;value=text/plain:x;null=elided",
	"3.1.2|multipart/form-data|string-object|contentEncoding":                "refused",
	"3.1.2|multipart/form-data|string-object|plain":                          "refused",
	"3.1.2|multipart/form-data|string|contentEncoding":                       "admitted;value=application/octet-stream:x;null=error",
	"3.1.2|multipart/form-data|string|plain":                                 "admitted;value=text/plain:x;null=text/plain:",
}
