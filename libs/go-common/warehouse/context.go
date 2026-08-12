package warehouse

import "context"

type contextKey string

const projectIDKey contextKey = "warehouse.projectID"
const warehouseIDKey contextKey = "warehouse.warehouseID"

// WithProjectID returns a new context carrying the project ID.
// Warehouse middleware (e.g. governance) can retrieve it to load
// per-project policies.
func WithProjectID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, projectIDKey, id)
}

// ProjectIDFromContext extracts the project ID set by WithProjectID.
// Returns an empty string if no project ID was set.
func ProjectIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(projectIDKey).(string)
	return id
}

// WithWarehouseID returns a new context carrying the warehouse ID of the
// datasource a query targets. Warehouse middleware (e.g. governance) uses
// it — alongside the project ID — to scope per-warehouse policies once a
// project holds more than one warehouse. Empty means "the project's
// primary/only warehouse" and callers may leave it unset for the
// single-warehouse case.
func WithWarehouseID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, warehouseIDKey, id)
}

// WarehouseIDFromContext extracts the warehouse ID set by WithWarehouseID.
// Returns an empty string if none was set.
func WarehouseIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(warehouseIDKey).(string)
	return id
}
