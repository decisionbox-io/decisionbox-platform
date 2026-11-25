package agentserver

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// noFilter is the production no-op: no filters registered, input unchanged.
func noFilter(_ context.Context, _, _ string, in []string) ([]string, error) { return in, nil }

// plainQualify is the non-RefQualifier path: "dataset.table".
func plainQualify(dataset, name string) string { return dataset + "." + name }

// listerFor returns a list func backed by a per-dataset map; a dataset absent
// from the map fails to list, which is what an unreadable schema looks like.
func listerFor(byDataset map[string][]string) func(context.Context, string) ([]string, error) {
	return func(_ context.Context, dataset string) ([]string, error) {
		names, ok := byDataset[dataset]
		if !ok {
			return nil, errors.New("permission denied for schema " + dataset)
		}
		return names, nil
	}
}

func TestEnumerateTables_QualifiesSortsAndDedupes(t *testing.T) {
	tables, unlisted, err := enumerateTables(context.Background(), "p1",
		[]string{"public", "staging"},
		listerFor(map[string][]string{
			"public":  {"orders", "customers"},
			"staging": {"orders"}, // same bare name, different dataset — not a dup
		}),
		noFilter, plainQualify)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"public.customers", "public.orders", "staging.orders"}
	if len(tables) != len(want) {
		t.Fatalf("tables = %v, want %v", tables, want)
	}
	for i, w := range want {
		if tables[i] != w {
			t.Errorf("tables[%d] = %q, want %q (sorted)", i, tables[i], w)
		}
	}
	if len(unlisted) != 0 {
		t.Errorf("unlisted = %v, want empty for a complete enumeration", unlisted)
	}
}

func TestEnumerateTables_DedupesWithinADataset(t *testing.T) {
	// A provider that reports the same table twice (or a qualifier that collapses
	// two names onto one) must not produce a duplicate picker row.
	tables, _, err := enumerateTables(context.Background(), "p1",
		[]string{"public"},
		listerFor(map[string][]string{"public": {"orders", "orders"}}),
		noFilter, plainQualify)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tables) != 1 || tables[0] != "public.orders" {
		t.Errorf("tables = %v, want [public.orders]", tables)
	}
}

func TestEnumerateTables_SkipsUnreadableDatasetAndNamesIt(t *testing.T) {
	// The whole point: a schema the credential can't read must not blank the
	// picker, and must not be omitted silently either — a caller choosing what
	// to index has to tell "no tables here" from "couldn't look".
	tables, unlisted, err := enumerateTables(context.Background(), "p1",
		[]string{"public", "staging", "archive"},
		listerFor(map[string][]string{"public": {"orders"}}),
		noFilter, plainQualify)
	if err != nil {
		t.Fatalf("unexpected error: %v — one readable dataset is still a success", err)
	}
	if len(tables) != 1 || tables[0] != "public.orders" {
		t.Errorf("tables = %v, want the readable dataset's tables", tables)
	}
	if len(unlisted) != 2 || unlisted[0] != "archive" || unlisted[1] != "staging" {
		t.Errorf("unlisted = %v, want [archive staging] (sorted)", unlisted)
	}
}

func TestEnumerateTables_AllDatasetsFail_IsAnError(t *testing.T) {
	// An unreachable warehouse or a bad credential must read as the failure it
	// is, not as a success with zero tables.
	_, _, err := enumerateTables(context.Background(), "p1",
		[]string{"public", "staging"},
		listerFor(nil),
		noFilter, plainQualify)
	if err == nil {
		t.Fatal("expected an error when no dataset could be listed")
	}
	if !strings.Contains(err.Error(), "all 2 dataset(s) failed") {
		t.Errorf("err = %v, want it to name how many datasets failed", err)
	}
	// The last underlying reason survives, so the operator sees why.
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("err = %v, want the underlying listing error", err)
	}
}

func TestEnumerateTables_NoDatasets_IsAnEmptySuccess(t *testing.T) {
	// A warehouse with nothing configured has nothing to enumerate; that is not
	// a failure, and the all-failed guard must not fire on it.
	tables, unlisted, err := enumerateTables(context.Background(), "p1",
		nil, listerFor(nil), noFilter, plainQualify)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tables) != 0 || len(unlisted) != 0 {
		t.Errorf("tables = %v, unlisted = %v, want both empty", tables, unlisted)
	}
}

func TestEnumerateTables_EmptyDatasetIsListedNotUnlisted(t *testing.T) {
	// A readable dataset that genuinely has no tables counts as LISTED — it is
	// the case the unlisted field exists to distinguish, so conflating the two
	// would defeat the point.
	tables, unlisted, err := enumerateTables(context.Background(), "p1",
		[]string{"public"},
		listerFor(map[string][]string{"public": {}}),
		noFilter, plainQualify)
	if err != nil {
		t.Fatalf("unexpected error: %v — an empty readable dataset is a success", err)
	}
	if len(tables) != 0 {
		t.Errorf("tables = %v, want empty", tables)
	}
	if len(unlisted) != 0 {
		t.Errorf("unlisted = %v, want empty — the dataset was read, it is just empty", unlisted)
	}
}

func TestEnumerateTables_UsesTheQualifier(t *testing.T) {
	// A provider needing a component the agent can't derive (BigQuery's data
	// project) qualifies further, and the picker's names must match the
	// schema_key the index and the filters key on.
	tables, _, err := enumerateTables(context.Background(), "p1",
		[]string{"analytics"},
		listerFor(map[string][]string{"analytics": {"events"}}),
		noFilter,
		func(dataset, name string) string { return "my-gcp-project." + dataset + "." + name },
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tables) != 1 || tables[0] != "my-gcp-project.analytics.events" {
		t.Errorf("tables = %v, want the fully qualified name", tables)
	}
}

func TestEnumerateTables_AppliesRegisteredFilters(t *testing.T) {
	// The deployment's own filters run here too, so an operator can't pick a
	// table the deployment forbids.
	dropForbidden := func(_ context.Context, _, _ string, in []string) ([]string, error) {
		kept := make([]string, 0, len(in))
		for _, t := range in {
			if t != "forbidden" {
				kept = append(kept, t)
			}
		}
		return kept, nil
	}
	tables, unlisted, err := enumerateTables(context.Background(), "p1",
		[]string{"public"},
		listerFor(map[string][]string{"public": {"orders", "forbidden"}}),
		dropForbidden, plainQualify)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tables) != 1 || tables[0] != "public.orders" {
		t.Errorf("tables = %v, want the filter applied", tables)
	}
	if len(unlisted) != 0 {
		t.Errorf("unlisted = %v, want empty — the dataset listed fine", unlisted)
	}
}

func TestEnumerateTables_FilterErrorSkipsTheDatasetAndNamesIt(t *testing.T) {
	// A filter that errors leaves the dataset's table set unknown, so it is
	// skipped like a failed listing — and named for the same reason.
	failOnStaging := func(_ context.Context, _, dataset string, in []string) ([]string, error) {
		if dataset == "staging" {
			return nil, errors.New("policy lookup failed")
		}
		return in, nil
	}
	tables, unlisted, err := enumerateTables(context.Background(), "p1",
		[]string{"public", "staging"},
		listerFor(map[string][]string{"public": {"orders"}, "staging": {"events"}}),
		failOnStaging, plainQualify)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tables) != 1 || tables[0] != "public.orders" {
		t.Errorf("tables = %v, want only the dataset whose filter succeeded", tables)
	}
	if len(unlisted) != 1 || unlisted[0] != "staging" {
		t.Errorf("unlisted = %v, want [staging]", unlisted)
	}
}

// --- listTablesJSON: the stdout contract the API parses ---

func TestListTablesJSON_CompleteListing(t *testing.T) {
	// The API keys off these exact field names (handler.listTablesResult). A
	// rename or omission here breaks the picker in the other repo, silently —
	// the field would simply decode as absent.
	got, err := listTablesJSON("wh_pg", []string{"public.orders"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `{"success":true,"tables":["public.orders"],"unlisted_datasets":[],"warehouse_id":"wh_pg"}`
	if string(got) != want {
		t.Errorf("listTablesJSON =\n %s\nwant\n %s", got, want)
	}
}

func TestListTablesJSON_PartialListing(t *testing.T) {
	got, err := listTablesJSON("wh_pg", []string{"public.orders"}, []string{"staging"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `{"success":true,"tables":["public.orders"],"unlisted_datasets":["staging"],"warehouse_id":"wh_pg"}`
	if string(got) != want {
		t.Errorf("listTablesJSON =\n %s\nwant\n %s", got, want)
	}
}

func TestListTablesJSON_EmptyListsAreArraysNotNull(t *testing.T) {
	// unlisted_datasets must be [] rather than absent or null: the API reads an
	// OMITTED field as "this agent predates the field", which is a different
	// claim from "nothing was skipped".
	got, err := listTablesJSON("wh_pg", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `{"success":true,"tables":[],"unlisted_datasets":[],"warehouse_id":"wh_pg"}`
	if string(got) != want {
		t.Errorf("listTablesJSON =\n %s\nwant\n %s", got, want)
	}
}
