package agentserver

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/config"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
)

// runListTables lists the qualified table names of one warehouse and prints
// them as JSON to stdout, then exits. Invoked when the agent is launched with
// `--list-tables` (optionally `--warehouse-id <id>`; empty resolves to the
// project's primary datasource).
//
// This is the cheap enumeration that powers a table picker: it lists names only
// (the warehouse driver's ListTablesInDataset metadata query) and does NOT pull
// per-table schema, sample rows, generate blurbs, or embed — so an operator can
// choose a table scope on a 1000+ table warehouse before paying to index it.
//
// A dataset that cannot be read is skipped rather than failing the whole
// listing, but it is NAMED in unlisted_datasets so the caller knows the
// catalogue is incomplete — see enumerateTables. The names are qualified with the same
// RefQualifier the indexer/discovery use, so a scope saved from this list
// matches the schema_key form the index and the run-time filters key on.
//
// Output contract mirrors --test-connection: a single JSON object on stdout,
// parsed by the API's RunSync caller. Non-zero exit + an error object on
// failure.
func runListTables(cfg *config.Config, projectID, warehouseID string) error {
	// Configurable deadline: listing names on a large warehouse can be slow —
	// BigQuery in particular makes a metadata call per table — so a fixed short
	// cap would return an empty picker on exactly the 1000+ table warehouses this
	// targets. Default 120s; operators raise LIST_TABLES_TIMEOUT_SECONDS for
	// very large catalogs. The API's RunSync deadline is derived from the same
	// env so the two match.
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(envIntDefault("LIST_TABLES_TIMEOUT_SECONDS", 120))*time.Second)
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

	qualify := func(dataset, name string) string {
		if hasQualifier {
			return qualifier.QualifiedName(dataset, name)
		}
		return dataset + "." + name
	}

	tables, unlisted, err := enumerateTables(whCtx, projectID, wh.GetDatasets(),
		provider.ListTablesInDataset, agentplugin.ApplyListTablesFilters, qualify)
	if err != nil {
		return err
	}

	// unlisted_datasets is always present (empty when every dataset listed), so a
	// caller can tell a complete enumeration from one this agent is too old to
	// report on.
	out, err := json.Marshal(map[string]interface{}{
		"success":           true,
		"warehouse_id":      whID,
		"tables":            tables,
		"unlisted_datasets": unlisted,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal result: %w", err)
	}
	fmt.Println(string(out))
	return nil
}

// enumerateTables lists each dataset through list, applies the registered
// ListTables filters, and returns the qualified names sorted and deduped,
// alongside the datasets that could not be read.
//
// A dataset that fails to list — or whose filter errors — is SKIPPED rather
// than failing the whole enumeration: one unreadable schema shouldn't blank a
// picker the other schemas can fill. But it is named in unlisted, because a
// caller choosing which tables to index has to be able to tell "this dataset
// has no tables" from "we could not read this dataset"; a silent omission
// offers an incomplete catalogue as a complete one.
//
// Only an all-datasets failure is an error, so an unreachable warehouse or a
// bad credential reads as the failure it is rather than a success with zero
// tables. A warehouse with no datasets configured is an empty success.
//
// filter is agentplugin.ApplyListTablesFilters in production — passed in rather
// than called directly so this is testable without mutating a process-global
// registry that has no un-register.
//
// Those filters are the same hook the index pass uses (see schema_discovery.go),
// so a governance/policy allow-deny list is honoured here too — the operator
// can't pick a table the deployment forbids. The user-editable table-scope
// filter is a no-op in this mode (its repo isn't wired when the agent runs
// --list-tables, so it passes through), deliberately: the picker has to show
// the full set to choose a scope FROM.
func enumerateTables(
	ctx context.Context,
	projectID string,
	datasets []string,
	list func(ctx context.Context, dataset string) ([]string, error),
	filter func(ctx context.Context, projectID, dataset string, in []string) ([]string, error),
	qualify func(dataset, name string) string,
) (tables, unlisted []string, err error) {
	seen := make(map[string]struct{})
	tables = make([]string, 0)
	unlisted = make([]string, 0)
	listed := 0
	var lastErr error
	for _, dataset := range datasets {
		names, lerr := list(ctx, dataset)
		if lerr == nil {
			names, lerr = filter(ctx, projectID, dataset, names)
		}
		if lerr != nil {
			lastErr = lerr
			unlisted = append(unlisted, dataset)
			continue
		}
		listed++
		for _, name := range names {
			qualified := qualify(dataset, name)
			if _, dup := seen[qualified]; dup {
				continue
			}
			seen[qualified] = struct{}{}
			tables = append(tables, qualified)
		}
	}
	if listed == 0 && len(datasets) > 0 {
		return nil, nil, fmt.Errorf("list tables: all %d dataset(s) failed to list; last error: %w", len(datasets), lastErr)
	}
	sort.Strings(tables)
	sort.Strings(unlisted)
	return tables, unlisted, nil
}
