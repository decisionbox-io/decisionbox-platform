package warehouse

import (
	"context"
	"testing"
)

func TestProjectIDContext(t *testing.T) {
	ctx := context.Background()

	if got := ProjectIDFromContext(ctx); got != "" {
		t.Errorf("expected empty project ID from bare context, got %q", got)
	}

	ctx = WithProjectID(ctx, "proj-123")
	if got := ProjectIDFromContext(ctx); got != "proj-123" {
		t.Errorf("expected proj-123, got %q", got)
	}
}
