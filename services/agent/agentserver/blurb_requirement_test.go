package agentserver

import (
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// TestProjectNeedsBlurbs decides whether an index run resolves a blurb LLM at
// all. Getting it wrong in the permissive direction is the bug this fixes: a
// project made only of self-describing sources would fail its index run on a
// model it never calls. Getting it wrong the other way is worse — a project
// with tables would index them with no descriptions to embed.
func TestProjectNeedsBlurbs(t *testing.T) {
	tests := []struct {
		name       string
		warehouses []models.WarehouseConfig
		want       bool
	}{
		{
			name:       "catalog-only project needs none",
			warehouses: []models.WarehouseConfig{{ID: "ga", Provider: "test-cube-source", Datasets: []string{}}},
			want:       false,
		},
		{
			name:       "table source needs one",
			warehouses: []models.WarehouseConfig{{ID: "wh", Provider: "test-tabular-source", Datasets: []string{"d"}}},
			want:       true,
		},
		{
			name: "a mixed project still needs one for its tables",
			warehouses: []models.WarehouseConfig{
				{ID: "ga", Provider: "test-cube-source"},
				{ID: "wh", Provider: "test-tabular-source", Datasets: []string{"d"}},
			},
			want: true,
		},
		{
			// An unregistered slug is treated as table-shaped, which is what
			// every provider was before catalog sources existed. Guessing the
			// other way would silently skip blurbs for a real warehouse.
			name:       "unregistered provider needs one",
			warehouses: []models.WarehouseConfig{{ID: "x", Provider: "not-registered", Datasets: []string{"d"}}},
			want:       true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &models.Project{Warehouses: tt.warehouses}
			if got := projectNeedsBlurbs(p); got != tt.want {
				t.Errorf("projectNeedsBlurbs() = %v, want %v", got, tt.want)
			}
		})
	}
}
