package agentserver

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/discovery"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

func ids(whs []models.WarehouseConfig) []string {
	out := make([]string, len(whs))
	for i, w := range whs {
		out[i] = w.ID
	}
	return out
}

// warehousesToIndex drives which datasources index-schema builds a catalog for.
// It must list the primary first, then every other datasource once, so
// ask-serve's search_tables / lookup_schema work across all of them — while a
// legacy single-warehouse project still yields exactly one warehouse.
func TestWarehousesToIndex(t *testing.T) {
	t.Run("legacy single warehouse yields the synthesised default", func(t *testing.T) {
		p := &models.Project{Warehouse: models.WarehouseConfig{Provider: "postgres", Datasets: []string{"public"}}}
		got := ids(warehousesToIndex(p))
		if len(got) != 1 || got[0] != models.DefaultWarehouseID {
			t.Fatalf("got %v, want [%s]", got, models.DefaultWarehouseID)
		}
	})

	t.Run("multi-warehouse: primary first, others deduped in order", func(t *testing.T) {
		p := &models.Project{
			PrimaryWarehouseID: "wh_b",
			Warehouses: []models.WarehouseConfig{
				{ID: "wh_a", Provider: "snowflake"},
				{ID: "wh_b", Provider: "redshift"},
				{ID: "wh_c", Provider: "bigquery"},
			},
		}
		got := ids(warehousesToIndex(p))
		want := []string{"wh_b", "wh_a", "wh_c"}
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("got %v, want %v", got, want)
			}
		}
	})

	t.Run("id-less default entry is not double-indexed", func(t *testing.T) {
		// Primary resolves (via normalization) to the id-less entry; it must not
		// then reappear as a second, separate warehouse.
		p := &models.Project{
			PrimaryWarehouseID: models.DefaultWarehouseID,
			Warehouses: []models.WarehouseConfig{
				{ID: "wh_a", Provider: "snowflake"},
				{ID: "", Provider: "postgres"}, // the default
			},
		}
		got := warehousesToIndex(p)
		if len(got) != 2 {
			t.Fatalf("got %d warehouses, want 2 (no double-index): %v", len(got), ids(got))
		}
		if got[0].Provider != "postgres" {
			t.Fatalf("primary should be the id-less default (postgres), got %q", got[0].Provider)
		}
	})

	t.Run("no warehouse configured yields a single zero entry (fails downstream, as before)", func(t *testing.T) {
		got := warehousesToIndex(&models.Project{})
		if len(got) != 1 || got[0].Provider != "" {
			t.Fatalf("empty project should yield one zero warehouse, got %v", ids(got))
		}
	})
}

// fakeRunRecorder captures recorded runs for assertions (got = last, all = every).
type fakeRunRecorder struct {
	got *models.SchemaIndexRun
	all []*models.SchemaIndexRun
	err error
}

func (f *fakeRunRecorder) Record(_ context.Context, run *models.SchemaIndexRun) error {
	f.got = run
	f.all = append(f.all, run)
	return f.err
}

// stampSchemaIndexRun turns a per-datasource BuildIndex outcome into the durable
// run record. The mapping must be faithful on both the success and failure
// paths, and must never be derailed by a failing audit write.
func TestStampSchemaIndexRun(t *testing.T) {
	ctx := context.Background()
	start := time.Now().Add(-30 * time.Second)

	t.Run("success maps stats to a ready record", func(t *testing.T) {
		rec := &fakeRunRecorder{}
		wh := models.WarehouseConfig{ID: "wh_a", Label: "Redshift — Sales", Provider: "redshift"}
		stats := &discovery.Stats{
			Tables:         42,
			Blurbs:         40,
			BlurbTokensIn:  1000,
			BlurbTokensOut: 2000,
			PhaseDurations: map[string]time.Duration{
				models.SchemaIndexPhaseSchemaDiscovery:  3 * time.Second,
				models.SchemaIndexPhaseDescribingTables: 12 * time.Second,
			},
		}
		stampSchemaIndexRun(ctx, rec, "proj-1", "run-1", wh, "wh_a", start, stats, nil)

		got := rec.got
		if got == nil {
			t.Fatal("no run recorded")
		}
		if got.ProjectID != "proj-1" || got.DatasourceID != "wh_a" || got.RunID != "run-1" {
			t.Errorf("identity wrong: %+v", got)
		}
		if got.DatasourceName != "Redshift — Sales" {
			t.Errorf("datasource_name = %q, want label", got.DatasourceName)
		}
		if got.Status != models.SchemaIndexStatusReady || got.Error != "" {
			t.Errorf("status = %q err = %q, want ready/empty", got.Status, got.Error)
		}
		if got.Kind != models.SchemaIndexRunKindTables {
			t.Errorf("kind = %q", got.Kind)
		}
		if got.ObjectsIndexed != 42 || got.BlurbsGenerated != 40 {
			t.Errorf("counts: objects=%d blurbs=%d", got.ObjectsIndexed, got.BlurbsGenerated)
		}
		if got.TokensIn != 1000 || got.TokensOut != 2000 {
			t.Errorf("tokens: in=%d out=%d", got.TokensIn, got.TokensOut)
		}
		if got.PhaseDurations[models.SchemaIndexPhaseSchemaDiscovery] != 3000 ||
			got.PhaseDurations[models.SchemaIndexPhaseDescribingTables] != 12000 {
			t.Errorf("phase_durations (ms): %+v", got.PhaseDurations)
		}
		if got.StartedAt.IsZero() || got.FinishedAt.IsZero() || got.FinishedAt.Before(got.StartedAt) {
			t.Errorf("timestamps: start=%v finish=%v", got.StartedAt, got.FinishedAt)
		}
	})

	t.Run("failure with nil stats records failed + error, zero counts", func(t *testing.T) {
		rec := &fakeRunRecorder{}
		wh := models.WarehouseConfig{ID: "wh_b", Provider: "snowflake"} // no Label → provider fallback
		stampSchemaIndexRun(ctx, rec, "proj-1", "run-2", wh, "wh_b", start, nil, errors.New("connect: timeout"))

		got := rec.got
		if got == nil {
			t.Fatal("no run recorded")
		}
		if got.Status != models.SchemaIndexStatusFailed {
			t.Errorf("status = %q, want failed", got.Status)
		}
		if got.Error != "connect: timeout" {
			t.Errorf("error = %q", got.Error)
		}
		if got.DatasourceName != "snowflake" {
			t.Errorf("datasource_name = %q, want provider fallback", got.DatasourceName)
		}
		if got.ObjectsIndexed != 0 || got.BlurbsGenerated != 0 || got.TokensIn != 0 || got.TokensOut != 0 {
			t.Errorf("failed run should have zero counts, got %+v", got)
		}
		if len(got.PhaseDurations) != 0 {
			t.Errorf("failed run (nil stats) should have no phase durations, got %+v", got.PhaseDurations)
		}
	})

	t.Run("datasource_name falls back to id when label + provider empty", func(t *testing.T) {
		rec := &fakeRunRecorder{}
		stampSchemaIndexRun(ctx, rec, "proj-1", "run-3", models.WarehouseConfig{}, "default", start, &discovery.Stats{}, nil)
		if rec.got.DatasourceName != "default" {
			t.Errorf("datasource_name = %q, want id fallback", rec.got.DatasourceName)
		}
	})

	t.Run("a failing recorder does not panic", func(t *testing.T) {
		rec := &fakeRunRecorder{err: errors.New("mongo down")}
		// Must not panic / must swallow the write error (best-effort audit).
		stampSchemaIndexRun(ctx, rec, "proj-1", "run-4", models.WarehouseConfig{Provider: "postgres"}, "default", start, &discovery.Stats{Tables: 1, Blurbs: 1}, nil)
	})
}

// stampSetupFailure must record a failed run for EVERY configured datasource
// when a run dies during project-wide setup (before per-datasource indexing),
// so the durable history reflects the failure instead of a stale prior success.
func TestStampSetupFailure(t *testing.T) {
	ctx := context.Background()
	start := time.Now().Add(-5 * time.Second)
	setupErr := errors.New("embedding pre-flight failed")

	t.Run("multi-warehouse stamps a failed record per datasource", func(t *testing.T) {
		rec := &fakeRunRecorder{}
		project := &models.Project{
			PrimaryWarehouseID: "wh_b",
			Warehouses: []models.WarehouseConfig{
				{ID: "wh_a", Provider: "snowflake"},
				{ID: "wh_b", Provider: "redshift"},
			},
		}
		stampSetupFailure(ctx, rec, "proj-1", "run-1", project, start, setupErr)

		if len(rec.all) != 2 {
			t.Fatalf("recorded %d runs, want one per datasource (2)", len(rec.all))
		}
		seen := map[string]bool{}
		for _, r := range rec.all {
			seen[r.DatasourceID] = true
			if r.Status != models.SchemaIndexStatusFailed || r.Error != setupErr.Error() {
				t.Errorf("record for %s: status=%q err=%q, want failed + setup error", r.DatasourceID, r.Status, r.Error)
			}
			if r.ObjectsIndexed != 0 {
				t.Errorf("setup failure should have zero objects, got %d", r.ObjectsIndexed)
			}
		}
		if !seen["wh_a"] || !seen["wh_b"] {
			t.Errorf("expected both datasources stamped, saw %v", seen)
		}
	})

	t.Run("legacy single warehouse stamps one default record", func(t *testing.T) {
		rec := &fakeRunRecorder{}
		project := &models.Project{Warehouse: models.WarehouseConfig{Provider: "postgres", Datasets: []string{"public"}}}
		stampSetupFailure(ctx, rec, "proj-1", "run-1", project, start, setupErr)
		if len(rec.all) != 1 || rec.all[0].DatasourceID != models.DefaultWarehouseID {
			t.Fatalf("want one record for the default datasource, got %+v", rec.all)
		}
		if rec.all[0].Status != models.SchemaIndexStatusFailed {
			t.Errorf("status = %q, want failed", rec.all[0].Status)
		}
	})
}

// stampSkippedDatasources records the datasources an aborted primary never got
// to — marked failed (with a skip reason), so the roll-up doesn't keep showing
// their prior success as current after a Re-index dropped the shared collection.
func TestStampSkippedDatasources(t *testing.T) {
	ctx := context.Background()
	start := time.Now().Add(-10 * time.Second)
	cause := errors.New("primary: embed failed")

	t.Run("stamps each skipped datasource failed with a skip reason", func(t *testing.T) {
		rec := &fakeRunRecorder{}
		skipped := []models.WarehouseConfig{{ID: "wh_b", Provider: "snowflake"}, {ID: "wh_c", Provider: "bigquery"}}
		stampSkippedDatasources(ctx, rec, "proj-1", "run-1", skipped, start, cause)

		if len(rec.all) != 2 {
			t.Fatalf("recorded %d runs, want one per skipped datasource (2)", len(rec.all))
		}
		for _, r := range rec.all {
			if r.Status != models.SchemaIndexStatusFailed {
				t.Errorf("%s: status = %q, want failed", r.DatasourceID, r.Status)
			}
			if !strings.Contains(r.Error, "skipped") || !strings.Contains(r.Error, "embed failed") {
				t.Errorf("%s: error = %q, want skip reason + cause", r.DatasourceID, r.Error)
			}
			if r.ObjectsIndexed != 0 {
				t.Errorf("%s: skipped run should have zero objects, got %d", r.DatasourceID, r.ObjectsIndexed)
			}
		}
	})

	t.Run("no-op for an empty skip list", func(t *testing.T) {
		rec := &fakeRunRecorder{}
		stampSkippedDatasources(ctx, rec, "proj-1", "run-1", nil, start, cause)
		if len(rec.all) != 0 {
			t.Fatalf("expected no records for empty skip list, got %d", len(rec.all))
		}
	})
}

// warehouseIDOrDefault maps an id-less (default) warehouse to the reserved
// "default" id so warehouse-scoped lookups (discovery search_tables, the schema
// cache) filter on a concrete id instead of reading empty as "all warehouses".
func TestWarehouseIDOrDefault(t *testing.T) {
	if got := warehouseIDOrDefault(models.WarehouseConfig{ID: ""}); got != models.DefaultWarehouseID {
		t.Errorf("empty id = %q, want %q", got, models.DefaultWarehouseID)
	}
	if got := warehouseIDOrDefault(models.WarehouseConfig{ID: "wh_b"}); got != "wh_b" {
		t.Errorf("explicit id = %q, want wh_b", got)
	}
}
