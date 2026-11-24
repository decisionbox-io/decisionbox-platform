package agentserver

import (
	"context"
	"fmt"
	"sort"
)

// This file holds the pure half of the agent's --list-tables mode, separate
// from list_tables.go so it is covered by unit tests while the Mongo /
// secret-provider / warehouse-provider wiring around it stays out of the
// coverage target (codecov.yml) — the same split validate_sql.go keeps with
// validate_sql_check.go.

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
