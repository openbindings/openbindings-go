package processorscenarios

import "testing"

func TestMatchAlternativesAndPresence(t *testing.T) {
	s := Scenario{ID: "X", Expected: []Expected{
		{Disposition: "complete", Phase: "completion", Assertions: []Assertion{{Path: "/choice", Equals: "a", equalsPresent: true}}},
		{Disposition: "complete", Phase: "completion", Assertions: []Assertion{{Path: "/choice", Equals: "b", equalsPresent: true}, {Path: "/missing", Absent: true}}},
	}}
	idx, err := Match(s, Observation{Disposition: "complete", Phase: "completion", Data: map[string]any{"choice": "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if idx != 1 {
		t.Fatalf("alternative = %d, want 1", idx)
	}
}

func TestMatchSetAndContains(t *testing.T) {
	s := Scenario{ID: "X", Expected: []Expected{{
		Disposition: "complete", Phase: "completion", Assertions: []Assertion{
			{Path: "/set", SetEquals: []any{"a", "b"}},
			{Path: "/text", Contains: "needle", containsPresent: true},
		},
	}}}
	_, err := Match(s, Observation{Disposition: "complete", Phase: "completion", Data: map[string]any{
		"set": []any{"b", "a"}, "text": "a needle here",
	}})
	if err != nil {
		t.Fatal(err)
	}
}
