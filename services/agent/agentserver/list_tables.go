package agentserver

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/config"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
)

// runListTables lists the qualified table names of one warehouse and prints
// them as JSON to stdout, then exits. Invoked when the agent is launched with
// `--list-tables` (optionally `--warehouse-id <id>`; empty resolves to the
// project's primary datasource).
//
// This is the cheap pre-index enumeration that powers the discovery-scope
// table picker: it lists names only (the warehouse driver's ListTablesInDataset
// metadata query) and does NOT pull per-table schema, sample rows, generate
// blurbs, or embed — so an operator can choose a table scope on a 1000+ table
// warehouse before paying to index it. The names are qualified with the same
// RefQualifier the indexer/discovery use, so a scope saved from this list
// matches the schema_key form the index and the run-time filters key on.
//
// Output contract mirrors --test-connection: a single JSON object on stdout,
// parsed by the API's RunSync caller. Non-zero exit + an error object on
// failure.
func runListTables(cfg *config.Config, projectID, warehouseID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ctx = gowarehouse.WithProjectID(ctx, projectID)

	mongoClient, err := initMongoDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = mongoClient.Disconnect(ctx) }()

	db := database.New(mongoClient)
	projectRepo := database.NewProjectRepository(db)
	project, err := projectRepo.GetByID(ctx, projectID)
	if err != nil {
		return fmt.Errorf("failed to load project %s: %w", projectID, err)
	}

	secretProvider, err := initSecretProvider(mongoClient)
	if err != nil {
		return err
	}

	// Empty id resolves to the primary through the accessor (not the raw id) so
	// a stale/removed primary_warehouse_id falls back to the first configured
	// warehouse — same resolution --test-connection uses.
	whID := warehouseID
	if whID == "" {
		whID = warehouseIDOrDefault(project.PrimaryWarehouse())
	}
	wh, ok := project.WarehouseByID(whID)
	if !ok || wh.Provider == "" {
		return fmt.Errorf("no warehouse %q configured", whID)
	}

	whCtx := gowarehouse.WithWarehouseID(ctx, whID)
	provider, err := initWarehouseProvider(whCtx, project, whID, secretProvider, projectID)
	if err != nil {
		return err
	}
	defer provider.Close()

	// Qualify names the same way discovery does (RefQualifier when the provider
	// needs a component the agent can't derive, e.g. BigQuery's data project;
	// plain "dataset.table" otherwise), so the picker's names match the
	// schema_key the index and scope filters use.
	qualifier, hasQualifier := provider.(gowarehouse.RefQualifier)

	seen := make(map[string]struct{})
	tables := make([]string, 0)
	for _, dataset := range wh.GetDatasets() {
		names, err := provider.ListTablesInDataset(whCtx, dataset)
		if err != nil {
			// One bad dataset shouldn't blank the whole picker — skip it and
			// keep listing the others (mirrors discovery's per-dataset skip).
			continue
		}
		for _, name := range names {
			qualified := dataset + "." + name
			if hasQualifier {
				qualified = qualifier.QualifiedName(dataset, name)
			}
			if _, dup := seen[qualified]; dup {
				continue
			}
			seen[qualified] = struct{}{}
			tables = append(tables, qualified)
		}
	}
	sort.Strings(tables)

	out, err := json.Marshal(map[string]interface{}{
		"success":      true,
		"warehouse_id": whID,
		"tables":       tables,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal result: %w", err)
	}
	fmt.Println(string(out))
	return nil
}
