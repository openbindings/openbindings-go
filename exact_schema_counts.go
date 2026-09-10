package openbindings

import (
	"strconv"
	"strings"

	"github.com/openbindings/openbindings-go/internal/thirdparty/jsonschema"
	"github.com/openbindings/openbindings-go/jsonvalue"
	"golang.org/x/text/message"
)

// exactCountCompiler uses the backend's compile extension, after its built-in
// keywords have been compiled, to correct int overflow in v6.0.3. No instance
// traversal, reference resolution, contains matching or coverage is duplicated.
func exactCountCompiler() *jsonschema.Compiler {
	c := jsonschema.NewCompiler()
	c.UseRegexpEngine(schemaRegexpEngine)
	c.RegisterVocabulary(&jsonschema.Vocabulary{
		URL: "urn:openbindings:implementation:exact-counts",
		Compile: func(ctx *jsonschema.CompilerContext, obj map[string]any) (jsonschema.SchemaExt, error) {
			s := ctx.Enqueue(nil)
			if s.DraftVersion < 2019 && s.Ref != nil {
				return nil, nil // older drafts ignore every $ref sibling
			}
			var ext exactLargeCounts
			for _, entry := range []struct {
				key   string
				field **int
			}{
				{"minItems", &s.MinItems}, {"maxItems", &s.MaxItems},
				{"minProperties", &s.MinProperties}, {"maxProperties", &s.MaxProperties},
				{"minLength", &s.MinLength}, {"maxLength", &s.MaxLength},
				{"minContains", &s.MinContains}, {"maxContains", &s.MaxContains},
			} {
				if strings.HasSuffix(entry.key, "Contains") && (s.DraftVersion < 2019 || s.Contains == nil) {
					continue
				}
				token, numeric, err := jsonvalue.NumberToken(obj[entry.key])
				if err != nil {
					return nil, err
				}
				if !numeric {
					continue
				}
				order, err := jsonvalue.CompareNumbers(obj[entry.key], int(^uint(0)>>1))
				if err != nil {
					return nil, err
				}
				if order <= 0 {
					continue
				}
				// Every representable Go array, object and string has a count
				// at most MaxInt. An oversized maximum imposes no restriction;
				// an oversized minimum is impossible for its instance type.
				*entry.field = nil
				if strings.HasPrefix(entry.key, "min") {
					ext = append(ext, exactLargeCount{entry.key, token})
					if entry.key == "minContains" {
						zero := 0
						s.MinContains = &zero // backend still owns matching/coverage
					}
				}
			}
			if len(ext) == 0 {
				return nil, nil
			}
			return ext, nil
		},
	})
	c.AssertVocabs()
	return c
}

type exactLargeCount struct{ keyword, token string }
type exactLargeCounts []exactLargeCount

func (counts exactLargeCounts) Validate(ctx *jsonschema.ValidatorContext, value any) {
	for _, count := range counts {
		applicable := false
		switch value.(type) {
		case []any:
			applicable = count.keyword == "minItems" || count.keyword == "minContains"
		case map[string]any:
			applicable = count.keyword == "minProperties"
		case string:
			applicable = count.keyword == "minLength"
		}
		if applicable {
			ctx.AddError(&count)
		}
	}
}

func (c *exactLargeCount) KeywordPath() []string { return []string{c.keyword} }
func (c *exactLargeCount) LocalizedString(p *message.Printer) string {
	return p.Sprintf("%s requires a count of at least %s, greater than any representable collection length (%s)", c.keyword, c.token, strconv.Itoa(int(^uint(0)>>1)))
}
