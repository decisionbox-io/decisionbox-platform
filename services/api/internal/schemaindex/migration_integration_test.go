//go:build integration

package schemaindex

import (
	"context"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/api/database"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

func TestInteg_Migration_FlipsLegacyProjectsToPending(t *testing.T) {
	ctx := context.Background()
	repo := database.NewProjectRepository(testDB)

	// Project 1: warehouse configured, never indexed. Should migrate.
	p1 := &models.Project{
		Name:     "legacy-with-warehouse",
		Domain:   "gaming",
		Category: "match3",
		Warehouse: models.WarehouseConfig{
			Provider: "bigquery",
			Datasets: []string{"d1"},
		},
	}
	if err := repo.Create(ctx, p1); err != nil {
		t.Fatalf("create p1: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, p1.ID) })

	// Project 2: no warehouse. Should be left alone.
	p2 := &models.Project{
		Name:     "legacy-no-warehouse",
		Domain:   "gaming",
		Category: "match3",
	}
	if err := repo.Create(ctx, p2); err != nil {
		t.Fatalf("create p2: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, p2.ID) })

	// Project 3: already ready. Idempotent path.
	p3 := &models.Project{
		Name:     "already-ready",
		Domain:   "gaming",
		Category: "match3",
		Warehouse: models.WarehouseConfig{
			Provider: "bigquery",
			Datasets: []string{"d1"},
		},
	}
	if err := repo.Create(ctx, p3); err != nil {
		t.Fatalf("create p3: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, p3.ID) })
	if err := repo.SetSchemaIndexStatus(ctx, p3.ID, models.SchemaIndexStatusReady, ""); err != nil {
		t.Fatalf("pre-set p3 ready: %v", err)
	}

	n, err := MigratePreExistingProjects(ctx, repo)
	if err != nil {
		t.Fatalf("MigratePreExistingProjects: %v", err)
	}
	if n != 1 {
		t.Errorf("migrated = %d, want 1 (p1 only)", n)
	}

	got1, _ := repo.GetByID(ctx, p1.ID)
	if got1.SchemaIndexStatus != models.SchemaIndexStatusPendingIndexing {
		t.Errorf("p1 status = %q, want pending_indexing", got1.SchemaIndexStatus)
	}
	got2, _ := repo.GetByID(ctx, p2.ID)
	if got2.SchemaIndexStatus != "" {
		t.Errorf("p2 status = %q, want empty (no warehouse)", got2.SchemaIndexStatus)
	}
	got3, _ := repo.GetByID(ctx, p3.ID)
	if got3.SchemaIndexStatus != models.SchemaIndexStatusReady {
		t.Errorf("p3 status = %q, want ready (unchanged)", got3.SchemaIndexStatus)
	}
}

func TestInteg_Migration_Idempotent(t *testing.T) {
	ctx := context.Background()
	repo := database.NewProjectRepository(testDB)

	p := &models.Project{
		Name:     "idemp",
		Domain:   "gaming",
		Category: "match3",
		Warehouse: models.WarehouseConfig{
			Provider: "bigquery",
			Datasets: []string{"d1"},
		},
	}
	if err := repo.Create(ctx, p); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, p.ID) })

	n1, _ := MigratePreExistingProjects(ctx, repo)
	n2, _ := MigratePreExistingProjects(ctx, repo)
	if n1 != 1 {
		t.Errorf("first run migrated %d, want 1", n1)
	}
	if n2 != 0 {
		t.Errorf("second run migrated %d, want 0 (idempotent)", n2)
	}
}

func TestInteg_Migration_NilRepoErrors(t *testing.T) {
	_, err := MigratePreExistingProjects(context.Background(), nil)
	if err == nil {
		t.Error("nil repo should error")
	}
}
