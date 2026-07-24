package graphql

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type corpusFixture struct {
	Rule        string       `json:"rule"`
	BindingSpec string       `json:"bindingSpec"`
	Tests       []corpusTest `json:"tests"`
}

type corpusTest struct {
	Description string          `json:"description"`
	Document    json.RawMessage `json:"document"`
	Valid       bool            `json:"valid"`
}

type corpusDocument struct {
	Sources  map[string]corpusSource  `json:"sources"`
	Bindings map[string]corpusBinding `json:"bindings"`
}

type corpusSource struct {
	BindingSpec string          `json:"bindingSpec"`
	Location    *string         `json:"location"`
	Content     json.RawMessage `json:"content"`
}

type corpusBinding struct {
	Source string  `json:"source"`
	Ref    *string `json:"ref"`
}

func TestBindingSpecCorpus(t *testing.T) {
	root := os.Getenv("OB_SPEC_CORPUS")
	if root == "" {
		root = filepath.Join("..", "..", "..", "spec", "conformance")
	}
	dir := filepath.Join(root, "binding-specs", "graphql")
	files, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) && os.Getenv("OB_CORPUS_REQUIRED") == "" {
			t.Skip(err)
		}
		t.Fatal(err)
	}
	ran := false
	for _, file := range files {
		if filepath.Ext(file.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var fixture corpusFixture
		if err := json.Unmarshal(raw, &fixture); err != nil {
			t.Fatal(err)
		}
		if fixture.BindingSpec != BindingSpec {
			t.Fatalf("%s bindingSpec = %q", file.Name(), fixture.BindingSpec)
		}
		ran = true
		for _, test := range fixture.Tests {
			test := test
			t.Run(fixture.Rule+"/"+test.Description, func(t *testing.T) {
				refusal := judgeCorpusDocument(test.Document)
				if test.Valid && refusal != nil {
					t.Errorf("valid fixture refused: %v", refusal)
				}
				if !test.Valid && refusal == nil {
					t.Error("invalid fixture accepted")
				}
			})
		}
	}
	if !ran {
		t.Fatal("GraphQL corpus contains no fixtures")
	}
}

func judgeCorpusDocument(raw json.RawMessage) error {
	var document corpusDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return err
	}
	for sourceName, source := range document.Sources {
		if source.BindingSpec != BindingSpec {
			continue
		}
		location := ""
		if source.Location != nil {
			location = *source.Location
		}
		if err := validateHTTPLocation(location); err != nil {
			return err
		}
		var schema *introspectionSchema
		if source.Content != nil {
			var err error
			schema, err = parseIntrospectionContent(source.Content)
			if err != nil {
				return err
			}
		}
		for _, binding := range document.Bindings {
			if binding.Source != sourceName {
				continue
			}
			ref := ""
			if binding.Ref != nil {
				ref = *binding.Ref
			}
			kind, fieldName, err := parseRef(ref)
			if err != nil {
				return err
			}
			if schema != nil {
				if _, err := resolveField(schema, kind, fieldName); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
