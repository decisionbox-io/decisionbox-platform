package policy

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func resetRegistry() {
	registryMu.Lock()
	registeredChecker = nil
	registryMu.Unlock()
}

func TestGetChecker_DefaultIsNoop(t *testing.T) {
	resetRegistry()
	c := GetChecker()
	if c == nil {
		t.Fatal("GetChecker returned nil")
	}
	if _, ok := c.(NoopChecker); !ok {
		t.Errorf("default checker = %T, want NoopChecker", c)
	}
}

func TestRegisterChecker_OverridesDefault(t *testing.T) {
	resetRegistry()
	t.Cleanup(resetRegistry)

	custom := &fakeChecker{}
	RegisterChecker(custom)

	c := GetChecker()
	if c != custom {
		t.Errorf("GetChecker() = %v, want registered custom", c)
	}
}

func TestNoopChecker_AllowsEverything(t *testing.T) {
	resetRegistry()
	c := GetChecker()
	ctx := context.Background()

	tests := []struct {
		name string
		run  func() error
	}{
		{"CheckCreateProject", func() error {
			_, err := c.CheckCreateProject(ctx, "dep1", ProjectIntent{ProjectID: "p1"})
			return err
		}},
		{"CheckStartDiscoveryRun", func() error {
			_, err := c.CheckStartDiscoveryRun(ctx, "dep1", "p1", "r1")
			return err
		}},
		{"ConfirmDiscoveryRunEnded", func() error {
			return c.ConfirmDiscoveryRunEnded(ctx, "res1", RunOutcome{Status: "success"})
		}},
		{"CheckAddDataSource", func() error {
			_, err := c.CheckAddDataSource(ctx, "dep1")
			return err
		}},
		{"CheckLLMProviderAllowed", func() error {
			return c.CheckLLMProviderAllowed(ctx, "dep1", "anything")
		}},
		{"CheckRegisterUser", func() error {
			return c.CheckRegisterUser(ctx, "dep1", UserIdentity{PrincipalSub: "s"})
		}},
		{"Release", func() error {
			return c.Release(ctx, "res1")
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); err != nil {
				t.Errorf("noop %s returned %v, want nil", tc.name, err)
			}
		})
	}
}

func TestNoopChecker_FeatureEnabled_True(t *testing.T) {
	resetRegistry()
	c := GetChecker()
	ok, err := c.FeatureEnabled(context.Background(), "dep1", FeatureAudit)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !ok {
		t.Error("NoopChecker.FeatureEnabled should return true")
	}
}

func TestNoopChecker_SyncCounters_IsNoOp(t *testing.T) {
	resetRegistry()
	c := GetChecker()
	// Must not panic; must return immediately; must not block.
	c.SyncCounters(context.Background(), "dep1", CounterSnapshot{ProjectsCurrent: 5, DataSourcesCurrent: 3})
}

func TestNoopChecker_ObserveLLMTokens_IsNoOp(t *testing.T) {
	resetRegistry()
	c := GetChecker()
	// Must not panic, must not block, must return immediately.
	done := make(chan struct{})
	go func() {
		c.ObserveLLMTokens(context.Background(), "dep1", LLMUsageEvent{
			Provider:   "claude",
			Model:      "claude-opus-4-7",
			InputTokens: 100,
			OutputTokens: 50,
			OccurredAt: time.Now(),
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("ObserveLLMTokens did not return quickly")
	}
}

func TestNoopChecker_CreateReservationShape(t *testing.T) {
	resetRegistry()
	c := GetChecker()

	res, err := c.CheckCreateProject(context.Background(), "dep42", ProjectIntent{ProjectID: "proj7"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if res == nil {
		t.Fatal("nil reservation")
	}
	if res.DeploymentID != "dep42" {
		t.Errorf("DeploymentID = %q, want dep42", res.DeploymentID)
	}
	if res.Kind != KindProjectCreate {
		t.Errorf("Kind = %q, want %q", res.Kind, KindProjectCreate)
	}
	if res.Subject != "proj7" {
		t.Errorf("Subject = %q, want proj7", res.Subject)
	}
}

func TestPolicyError_LimitMessage(t *testing.T) {
	e := &PolicyError{Kind: "limit", Limit: "projects_per_deployment", Current: 1, Max: 1, PlanID: "starter_t1"}
	msg := e.Error()
	if !strings.Contains(msg, "starter_t1") {
		t.Errorf("error missing plan id: %q", msg)
	}
	if !strings.Contains(msg, "projects_per_deployment") {
		t.Errorf("error missing limit name: %q", msg)
	}
	if !strings.Contains(msg, "1/1") {
		t.Errorf("error missing counter/max: %q", msg)
	}
	if !e.IsLimit() {
		t.Error("IsLimit() = false, want true")
	}
	if e.IsFeature() {
		t.Error("IsFeature() = true, want false")
	}
}

func TestPolicyError_FeatureMessage(t *testing.T) {
	e := &PolicyError{Kind: "feature", Feature: "audit_enabled", PlanID: "starter_t1"}
	msg := e.Error()
	if !strings.Contains(msg, "audit_enabled") {
		t.Errorf("error missing feature name: %q", msg)
	}
	if !e.IsFeature() {
		t.Error("IsFeature() = false, want true")
	}
}

func TestPolicyError_ProviderAllowedMessage(t *testing.T) {
	e := &PolicyError{Kind: "feature", Feature: "llm_provider", PlanID: "starter_t1", Allowed: []string{"claude", "openai"}}
	msg := e.Error()
	if !strings.Contains(msg, "claude") {
		t.Errorf("error missing allowed list: %q", msg)
	}
}

func TestPolicyError_ExplicitMessageWins(t *testing.T) {
	e := &PolicyError{Kind: "limit", Message: "custom reason"}
	if e.Error() != "custom reason" {
		t.Errorf("Error() = %q, want %q", e.Error(), "custom reason")
	}
}

func TestPolicyError_AsUnwrap(t *testing.T) {
	var e error = &PolicyError{Kind: "limit", Limit: "projects_per_deployment", Current: 1, Max: 1}
	var pe *PolicyError
	if !errors.As(e, &pe) {
		t.Fatal("errors.As should succeed on *PolicyError")
	}
	if pe.Limit != "projects_per_deployment" {
		t.Errorf("Limit = %q", pe.Limit)
	}
}

func TestRegisterChecker_ConcurrentSafe(t *testing.T) {
	resetRegistry()
	t.Cleanup(resetRegistry)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			RegisterChecker(&fakeChecker{})
		}()
		go func() {
			defer wg.Done()
			_ = GetChecker()
		}()
	}
	wg.Wait()

	// No assertion beyond "doesn't race / doesn't deadlock".
}

// --- helpers ---

type fakeChecker struct{ NoopChecker }
