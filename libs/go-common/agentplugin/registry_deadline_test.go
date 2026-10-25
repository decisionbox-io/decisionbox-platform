package agentplugin

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The caller bounds the whole render with one deadline. A provider that
// spends all of it must not hand the next provider an already-expired
// context: that suppresses a healthy section exactly as an error would
// (seen with a slow knowledge-source search starving the ledger section on
// every discovery run).
func TestRenderSections_SlowProviderDoesNotStarveTheNext(t *testing.T) {
	resetForTest()
	defer resetForTest()

	RegisterContextProvider(ContextProviderFunc{ProviderName: "slow", Fn: func(ctx context.Context, _, _ string, _ ContextProviderOpts) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}})
	RegisterContextProvider(ContextProviderFunc{ProviderName: "fast", Fn: func(ctx context.Context, _, _ string, _ ContextProviderOpts) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err // what a store client does with an expired context
		}
		return "## Fast\nbody", nil
	}})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	var failed []string
	got := RenderSections(ctx, "p1", "q", ContextProviderOpts{}, func(name string, _ error) { failed = append(failed, name) })

	if !strings.Contains(got, "## Fast") {
		t.Errorf("fast provider's section missing: %q", got)
	}
	if len(failed) != 1 || failed[0] != "slow" {
		t.Errorf("failed providers = %q, want [slow]", failed)
	}
}

// Providers finish in any order; sections and error reports still follow
// registration order.
func TestRenderSections_OrderIndependentOfFinishTime(t *testing.T) {
	resetForTest()
	defer resetForTest()

	delayed := func(name string, d time.Duration, fail bool) ContextProviderFunc {
		return ContextProviderFunc{ProviderName: name, Fn: func(context.Context, string, string, ContextProviderOpts) (string, error) {
			time.Sleep(d)
			if fail {
				return "", context.DeadlineExceeded
			}
			return "## " + name, nil
		}}
	}
	RegisterContextProvider(delayed("a", 30*time.Millisecond, false))
	RegisterContextProvider(delayed("b", 20*time.Millisecond, true))
	RegisterContextProvider(delayed("c", 0, false))
	RegisterContextProvider(delayed("d", 10*time.Millisecond, true))

	var failed []string
	got := RenderSections(context.Background(), "p1", "q", ContextProviderOpts{}, func(name string, _ error) { failed = append(failed, name) })
	if got != "## a\n\n## c\n" {
		t.Errorf("sections = %q, want a then c", got)
	}
	if strings.Join(failed, ",") != "b,d" {
		t.Errorf("error reports = %q, want b then d", failed)
	}
}
