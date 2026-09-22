package agentserver

import (
	"context"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// initWarehouseProvider is where the dataset requirement actually bites: a
// source configured without one is refused before a provider is constructed.
// The shared helper can be correct and this call site still let a
// dataset-less warehouse through, or refuse a cube source that legitimately
// has none — which is the failure that made a cube source unconfigurable in
// the first place.
func TestInitWarehouseProvider_DatasetRequirementFollowsShape(t *testing.T) {
	project := func(provider string, datasets []string) *models.Project {
		return &models.Project{
			ID:         "p1",
			Warehouses: []models.WarehouseConfig{{ID: "wh_1", Provider: provider, Datasets: datasets}},
		}
	}

	t.Run("a table-shaped source without a dataset is refused", func(t *testing.T) {
		_, err := initWarehouseProvider(context.Background(), project("test-tabular-source", nil), "wh_1", &fakeSecretProvider{}, "p1")
		if err == nil || !strings.Contains(err.Error(), "no datasets configured") {
			t.Fatalf("err = %v, want a missing-dataset refusal", err)
		}
	})

	t.Run("a cube source without a dataset gets past the requirement", func(t *testing.T) {
		// Whatever construction then does, the assertion is that the dataset
		// check did not refuse it: a cube source has no datasets to give.
		_, err := initWarehouseProvider(context.Background(), project("test-cube-source", nil), "wh_1", &fakeSecretProvider{}, "p1")
		if err != nil && strings.Contains(err.Error(), "no datasets configured") {
			t.Fatalf("a source with no datasets was asked for one: %v", err)
		}
	})
}
