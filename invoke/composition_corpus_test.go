package invoke

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

type compositionCorpusProvider struct {
	Key                 string                  `json:"key"`
	Preference          float64                 `json:"preference"`
	RuntimeBindingSpecs []string                `json:"runtimeBindingSpecs"`
	Interface           *openbindings.Interface `json:"interface"`
}

type compositionCorpusCase struct {
	ID         string                      `json:"id"`
	Consumer   *openbindings.Interface     `json:"consumer"`
	Providers  []compositionCorpusProvider `json:"providers"`
	Dependency string                      `json:"dependency"`
	Invocation *struct {
		Input  any `json:"input"`
		Output any `json:"output"`
	} `json:"invocation"`
	Expected struct {
		Status          string   `json:"status"`
		ProviderKey     string   `json:"providerKey"`
		BindingKey      string   `json:"bindingKey"`
		AmbiguityStage  string   `json:"ambiguityStage"`
		AssessmentCodes []string `json:"assessmentCodes"`
	} `json:"expected"`
}

type compositionCorpus struct {
	FormatVersion string                  `json:"formatVersion"`
	PolicyID      string                  `json:"policyId"`
	Cases         []compositionCorpusCase `json:"cases"`
}

func TestPortableRuntimeCompositionCorpus(t *testing.T) {
	localPath := filepath.Join("testdata", "runtime-composition-v1.json")
	local, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatal(err)
	}
	authoritativePath := filepath.Join("..", "..", "interfaces", "conformance", "composition", "cases.json")
	if authoritative, err := os.ReadFile(authoritativePath); err == nil && !bytes.Equal(local, authoritative) {
		t.Fatal("vendored runtime composition corpus differs from interfaces source")
	}
	var corpus compositionCorpus
	if err := json.Unmarshal(local, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.FormatVersion != "1.0.0" || corpus.PolicyID != ReferenceCompositionPolicyID {
		t.Fatalf("corpus metadata = %q %q", corpus.FormatVersion, corpus.PolicyID)
	}

	for _, scenario := range corpus.Cases {
		scenario := scenario
		t.Run(scenario.ID, func(t *testing.T) {
			consumer, err := openbindings.PrepareInterface(scenario.Consumer)
			if err != nil {
				t.Fatal(err)
			}
			registrations := make([]ProviderRegistration, 0, len(scenario.Providers))
			for _, candidate := range scenario.Providers {
				prepared, err := openbindings.PrepareInterface(candidate.Interface)
				if err != nil {
					t.Fatal(err)
				}
				infos := make([]openbindings.BindingSpecInfo, 0, len(candidate.RuntimeBindingSpecs))
				for _, bindingSpec := range candidate.RuntimeBindingSpecs {
					infos = append(infos, openbindings.BindingSpecInfo{BindingSpec: bindingSpec})
				}
				runtime := &compositionTestRuntime{specs: infos}
				provider, err := PrepareProvider(PreparedProviderOptions{
					Key: candidate.Key, Interface: prepared, Runtime: runtime,
				})
				if err != nil {
					t.Fatal(err)
				}
				registrations = append(registrations, ProviderRegistration{
					Provider: provider, Preference: candidate.Preference,
				})
			}
			session, err := NewCompositionSession(CompositionSessionOptions{
				Consumer: consumer, Providers: registrations,
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := ResolveDependency(context.Background(), session, NewDynamicDependencySignature(scenario.Dependency))
			if err != nil {
				t.Fatal(err)
			}
			if string(result.Status) != scenario.Expected.Status {
				t.Fatalf("status = %s, want %s", result.Status, scenario.Expected.Status)
			}
			switch result.Status {
			case DependencyAvailable:
				if result.Route.ProviderKey != scenario.Expected.ProviderKey || result.Route.BindingKey != scenario.Expected.BindingKey {
					t.Fatalf("route = %s/%s", result.Route.ProviderKey, result.Route.BindingKey)
				}
				if scenario.Invocation != nil {
					call := result.Route.Invoke(context.Background())
					if err := call.Write(context.Background(), scenario.Invocation.Input); err != nil {
						t.Fatal(err)
					}
					output, err := Single(context.Background(), call.Outputs())
					if err != nil || !reflect.DeepEqual(output, scenario.Invocation.Output) {
						t.Fatalf("invocation output = %#v, want %#v (err=%v)", output, scenario.Invocation.Output, err)
					}
				}
			case DependencyAmbiguous:
				if result.Ambiguity.Stage != scenario.Expected.AmbiguityStage {
					t.Fatalf("ambiguity stage = %s", result.Ambiguity.Stage)
				}
			case DependencyUnavailable:
				codes := make([]string, 0, len(result.Assessments))
				for _, assessment := range result.Assessments {
					codes = append(codes, assessment.Code)
				}
				if !reflect.DeepEqual(codes, scenario.Expected.AssessmentCodes) {
					t.Fatalf("assessment codes = %#v, want %#v", codes, scenario.Expected.AssessmentCodes)
				}
			}
		})
	}
}
