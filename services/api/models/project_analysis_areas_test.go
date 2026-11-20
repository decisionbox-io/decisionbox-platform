package models

import "testing"

// A project's enabled analysis areas are the set a discovery run can execute:
// the agent builds its list from prompts.analysis_areas and drops every area
// switched off on the playbook. Both the trigger's refusal of an unrunnable
// selective run and the message naming what the project does offer read this
// one list (#573).

func TestEnabledAnalysisAreaIDs(t *testing.T) {
	tests := []struct {
		name    string
		prompts *ProjectPrompts
		want    []string
	}{
		{"nil prompts", nil, nil},
		{"no areas", &ProjectPrompts{}, []string{}},
		{
			"disabled areas are excluded",
			&ProjectPrompts{AnalysisAreas: map[string]AnalysisAreaConfig{
				"churn":  {Enabled: true},
				"levels": {Enabled: false},
			}},
			[]string{"churn"},
		},
		{
			// Sorted, because the ids come out of a map and Go randomises map
			// iteration — an error message that reorders itself between two
			// identical requests is not one anybody can act on.
			"sorted by id",
			&ProjectPrompts{AnalysisAreas: map[string]AnalysisAreaConfig{
				"monetization": {Enabled: true},
				"churn":        {Enabled: true},
				"engagement":   {Enabled: true},
			}},
			[]string{"churn", "engagement", "monetization"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Repeated, because a randomised order can agree with the
			// expected one by chance.
			for i := 0; i < 50; i++ {
				got := tc.prompts.EnabledAnalysisAreaIDs()
				if len(got) != len(tc.want) {
					t.Fatalf("EnabledAnalysisAreaIDs() = %v, want %v", got, tc.want)
				}
				for j := range got {
					if got[j] != tc.want[j] {
						t.Fatalf("EnabledAnalysisAreaIDs() = %v, want %v", got, tc.want)
					}
				}
			}
		})
	}
}
