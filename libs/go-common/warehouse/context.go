package warehouse

import "context"

type contextKey string

const projectIDKey contextKey = "warehouse.projectID"

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
