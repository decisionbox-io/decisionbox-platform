package agentserver

import (
	"context"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// TestResolveBlurbLLM_EmptyModel covers the blurb-LLM model guard: an
// empty model is rejected for a normal provider but accepted when the
// effective config carries an endpoint_id (a user-deployed endpoint
// identifies its own model).
func TestResolveBlurbLLM_EmptyModel(t *testing.T) {
	sp := &fakeSecretProvider{}
	tests := []struct {
		name      string
		project   *models.Project
		wantErr   string
		wantModel string
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
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			provider, model, _, err := resolveBlurbLLM(context.Background(), nil, tc.project, sp, "p1")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if provider != tc.project.LLM.Provider {
				t.Errorf("provider = %q, want %q", provider, tc.project.LLM.Provider)
			}
			if model != tc.wantModel {
				t.Errorf("model = %q, want %q", model, tc.wantModel)
			}
		})
	}
}
