package agentserver

import (
	"context"
	"errors"
	"fmt"
	"testing"

	gosecrets "github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
)

// fakeSecretProvider implements gosecrets.Provider with a hand-set map
// and an optional injected error for Get. Only Get is exercised by
// resolveCredential; Set and List satisfy the interface.
type fakeSecretProvider struct {
	store  map[string]string // key: "projectID/key"
	getErr error             // when non-nil, Get returns this error instead of looking up
}

func (f *fakeSecretProvider) Get(_ context.Context, projectID, key string) (string, error) {
	if f.getErr != nil {
		return "", f.getErr
	}
	v, ok := f.store[projectID+"/"+key]
	if !ok {
		return "", gosecrets.ErrNotFound
	}
	return v, nil
}

func (f *fakeSecretProvider) Set(_ context.Context, projectID, key, value string) error {
	if f.store == nil {
		f.store = map[string]string{}
	}
	f.store[projectID+"/"+key] = value
	return nil
}

func (f *fakeSecretProvider) List(_ context.Context, _ string) ([]gosecrets.SecretEntry, error) {
	return nil, nil
}

func TestResolveCredential_DashboardWinsOverEnv(t *testing.T) {
	t.Setenv("FAKE_ENV", "env-value")
	sp := &fakeSecretProvider{store: map[string]string{"p1/test-key": "dashboard-value"}}
	v, src := resolveCredential(context.Background(), sp, "p1", "test-key", "FAKE_ENV")
	if v != "dashboard-value" {
		t.Errorf("value = %q, want dashboard-value", v)
	}
	if src != "dashboard" {
		t.Errorf("source = %q, want dashboard", src)
	}
}

func TestResolveCredential_DashboardOnlyEnvUnset(t *testing.T) {
	sp := &fakeSecretProvider{store: map[string]string{"p1/test-key": "dashboard-value"}}
	v, src := resolveCredential(context.Background(), sp, "p1", "test-key", "FAKE_ENV_NEVER_SET")
	if v != "dashboard-value" {
		t.Errorf("value = %q, want dashboard-value", v)
	}
	if src != "dashboard" {
		t.Errorf("source = %q, want dashboard", src)
	}
}

func TestResolveCredential_EnvFallback(t *testing.T) {
	t.Setenv("FAKE_ENV", "env-value")
	sp := &fakeSecretProvider{store: map[string]string{}}
	v, src := resolveCredential(context.Background(), sp, "p1", "missing-key", "FAKE_ENV")
	if v != "env-value" {
		t.Errorf("value = %q, want env-value", v)
	}
	if src != "env" {
		t.Errorf("source = %q, want env", src)
	}
}

func TestResolveCredential_BothMissingReturnsNone(t *testing.T) {
	sp := &fakeSecretProvider{store: map[string]string{}}
	v, src := resolveCredential(context.Background(), sp, "p1", "missing-key", "FAKE_ENV_NEVER_SET")
	if v != "" {
		t.Errorf("value = %q, want empty", v)
	}
	if src != "none" {
		t.Errorf("source = %q, want none", src)
	}
}

func TestResolveCredential_SecretProviderTransportError(t *testing.T) {
	t.Setenv("FAKE_ENV", "env-value")
	sp := &fakeSecretProvider{getErr: errors.New("upstream secret backend timeout")}
	// Transport errors must not block env fallback — operators who run with
	// env-only configuration (no secret backend wired up) should continue
	// to work when the agent can't talk to a secret backend.
	v, src := resolveCredential(context.Background(), sp, "p1", "test-key", "FAKE_ENV")
	if v != "env-value" {
		t.Errorf("value = %q, want env-value (env fallback after secret transport error)", v)
	}
	if src != "env" {
		t.Errorf("source = %q, want env", src)
	}
}

func TestResolveCredential_NotFoundFallsThroughSilently(t *testing.T) {
	t.Setenv("FAKE_ENV", "env-value")
	sp := &fakeSecretProvider{} // no store seeded → ErrNotFound on every Get
	v, src := resolveCredential(context.Background(), sp, "p1", "any-key", "FAKE_ENV")
	if v != "env-value" {
		t.Errorf("value = %q, want env-value", v)
	}
	if src != "env" {
		t.Errorf("source = %q, want env", src)
	}
}

func TestResolveCredential_WrappedErrNotFoundStillSilent(t *testing.T) {
	// Cloud secret providers (gcp/aws/azure) wrap backend errors with
	// %w. A wrapped ErrNotFound must still hit the env-fallback path
	// silently — no "Failed to read credential" warning. This pins the
	// errors.Is fix flagged by Copilot review.
	t.Setenv("FAKE_ENV", "env-value")
	sp := &fakeSecretProvider{getErr: fmt.Errorf("backend wrapped: %w", gosecrets.ErrNotFound)}
	v, src := resolveCredential(context.Background(), sp, "p1", "test-key", "FAKE_ENV")
	if v != "env-value" {
		t.Errorf("value = %q, want env-value (env fallback after wrapped ErrNotFound)", v)
	}
	if src != "env" {
		t.Errorf("source = %q, want env", src)
	}
}

func TestResolveCredential_EmptyDashboardValueFallsBackToEnv(t *testing.T) {
	// Edge case: dashboard secret exists but is the empty string.
	// resolveCredential treats empty as "no credential set" and falls
	// through to the env fallback — this matches operator intent (an
	// empty saved field is the same as "not configured").
	t.Setenv("FAKE_ENV", "env-value")
	sp := &fakeSecretProvider{store: map[string]string{"p1/test-key": ""}}
	v, src := resolveCredential(context.Background(), sp, "p1", "test-key", "FAKE_ENV")
	if v != "env-value" {
		t.Errorf("value = %q, want env-value", v)
	}
	if src != "env" {
		t.Errorf("source = %q, want env", src)
	}
}
