package agentserver

import (
	"errors"
	"testing"

	llmlayer "github.com/decisionbox-io/decisionbox/libs/go-common/llmlayer"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// TestTableDescriptionLayerModelGuard covers the table-description layer's
// model guard: an empty model is rejected for a normal provider but accepted
// when the effective config carries an endpoint_id (a user-deployed endpoint
// identifies its own model).
//
// The fixtures are the ones written for resolveBlurbLLM, re-pointed at the
// layer resolver that replaced it — the three legacy blurb branches have to
// keep behaving identically, and these are the cases that pin them.
func TestTableDescriptionLayerModelGuard(t *testing.T) {
	tests := []struct {
		name         string
		project      *models.Project
		wantErr      string
		wantModel    string
		wantProvider string // defaults to project.LLM.Provider when empty
	}{
		{
			name: "endpoint_backed_empty_model_ok",
			project: &models.Project{
				LLM: models.LLMConfig{Provider: "vertex-ai", Model: "", Config: map[string]string{"endpoint_id": "mg-endpoint-abc"}},
			},
			wantModel: "",
		},
		{
			name: "empty_model_no_endpoint_errors",
			project: &models.Project{
				LLM: models.LLMConfig{Provider: "vertex-ai", Model: "", Config: map[string]string{}},
			},
			wantErr: "no model configured",
		},
		{
			name: "normal_model_passes_through",
			project: &models.Project{
				LLM: models.LLMConfig{Provider: "openai", Model: "gpt-4o"},
			},
			wantModel: "gpt-4o",
		},
		{
			// A separate blurb endpoint override with a blank model must
			// not inherit the analysis model — it stays empty.
			name: "blurb_endpoint_override_does_not_inherit_analysis_model",
			project: &models.Project{
				LLM:      models.LLMConfig{Provider: "openai", Model: "gpt-4o"},
				BlurbLLM: &models.BlurbLLMConfig{Provider: "vertex-ai", Model: "", Config: map[string]string{"endpoint_id": "mg-endpoint-blurb"}},
			},
			wantModel:    "",
			wantProvider: "vertex-ai",
		},
		{
			// An endpoint config with an explicit model (strict serving
			// container) keeps it — blurb must not blank it out.
			name: "endpoint_with_explicit_model_is_preserved",
			project: &models.Project{
				LLM: models.LLMConfig{Provider: "vertex-ai", Model: "served-name", Config: map[string]string{"endpoint_id": "mg-endpoint-strict"}},
			},
			wantModel: "served-name",
		},
		{
			// A blurb endpoint override with an explicit model keeps it too.
			name: "blurb_endpoint_override_explicit_model_is_preserved",
			project: &models.Project{
				LLM:      models.LLMConfig{Provider: "openai", Model: "gpt-4o"},
				BlurbLLM: &models.BlurbLLMConfig{Provider: "vertex-ai", Model: "served-name", Config: map[string]string{"endpoint_id": "mg-endpoint-strict"}},
			},
			wantModel:    "served-name",
			wantProvider: "vertex-ai",
		},
		{
			// A whitespace-only endpoint_id is not an endpoint, so the
			// non-endpoint branch applies and the analysis model is
			// inherited rather than the model staying blank.
			name: "whitespace_endpoint_id_is_not_an_endpoint",
			project: &models.Project{
				LLM:      models.LLMConfig{Provider: "openai", Model: "gpt-4o"},
				BlurbLLM: &models.BlurbLLMConfig{Provider: "vertex-ai", Model: "", Config: map[string]string{"endpoint_id": "   "}},
			},
			wantModel:    "gpt-4o",
			wantProvider: "vertex-ai",
		},
		{
			// A blurb override with no provider is ignored ENTIRELY — not
			// partially — so its model does not leak into the analysis pair.
			name: "blurb_override_without_a_provider_is_ignored",
			project: &models.Project{
				LLM:      models.LLMConfig{Provider: "openai", Model: "gpt-4o"},
				BlurbLLM: &models.BlurbLLMConfig{Provider: "", Model: "ignored"},
			},
			wantModel: "gpt-4o",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := tc.project.LayerSpecUnchecked(llmlayer.LayerTableDescriptions)
			if err != nil {
				t.Fatalf("resolve the table-description layer: %v", err)
			}
			verr := spec.Validate()
			if tc.wantErr != "" {
				if !errors.Is(verr, llmlayer.ErrModelMissing) {
					t.Fatalf("Validate() = %v, want ErrModelMissing", verr)
				}
				return
			}
			if verr != nil {
				t.Fatalf("unexpected error: %v", verr)
			}
			wantProvider := tc.wantProvider
			if wantProvider == "" {
				wantProvider = tc.project.LLM.Provider
			}
			if spec.Provider != wantProvider {
				t.Errorf("provider = %q, want %q", spec.Provider, wantProvider)
			}
			if spec.Model != tc.wantModel {
				t.Errorf("model = %q, want %q", spec.Model, tc.wantModel)
			}
		})
	}
}
