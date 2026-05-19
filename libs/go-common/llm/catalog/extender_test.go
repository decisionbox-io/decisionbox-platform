package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// stubExtender is a controllable Extender for the registry tests. Each
// field on the stub corresponds to one observable behaviour the
// registry must respect.
type stubExtender struct {
	name            string
	extendEntries   []gollm.ModelEntry
	extendErr       error
	resolveEntry    *gollm.ModelEntry
	resolveErr      error
	extendCalls     int
	resolveCalls    int
	lastProjectID   string
	lastModelID     string
}

func (s *stubExtender) Extend(_ context.Context, projectID string) ([]gollm.ModelEntry, error) {
	s.extendCalls++
	s.lastProjectID = projectID
	if s.extendErr != nil {
		return nil, s.extendErr
	}
	return s.extendEntries, nil
}

func (s *stubExtender) Resolve(_ context.Context, modelID string) (*gollm.ModelEntry, error) {
	s.resolveCalls++
	s.lastModelID = modelID
	if s.resolveErr != nil {
		return nil, s.resolveErr
	}
	return s.resolveEntry, nil
}

func TestRegisterExtender_NilPanics(t *testing.T) {
	t.Cleanup(ResetForTest)
	ResetForTest()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("RegisterExtender(nil) must panic — nil extender is a programmer error")
		}
	}()
	RegisterExtender(nil)
}

func TestRegisteredExtenders_ReturnsCopy(t *testing.T) {
	t.Cleanup(ResetForTest)
	ResetForTest()
	e := &stubExtender{name: "a"}
	RegisterExtender(e)
	got := RegisteredExtenders()
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	// Mutating the returned slice must not affect the registry.
	got[0] = nil
	again := RegisteredExtenders()
	if again[0] == nil {
		t.Error("registry exposed its internal slice — callers can mutate state")
	}
}

func TestExtend_EmptyProjectIDIsRejected(t *testing.T) {
	t.Cleanup(ResetForTest)
	ResetForTest()
	if _, err := Extend(context.Background(), ""); err == nil {
		t.Fatal("Extend with empty project ID must error — project scope is part of the contract")
	}
}

func TestExtend_NoExtendersReturnsNil(t *testing.T) {
	t.Cleanup(ResetForTest)
	ResetForTest()
	entries, err := Extend(context.Background(), "p1")
	if err != nil {
		t.Fatalf("Extend with no extenders: %v", err)
	}
	if entries != nil {
		t.Errorf("entries = %v, want nil (no extenders registered)", entries)
	}
}

func TestExtend_ConcatenatesInRegistrationOrder(t *testing.T) {
	t.Cleanup(ResetForTest)
	ResetForTest()
	a := &stubExtender{extendEntries: []gollm.ModelEntry{{ID: "a:1", MaxOutputTokens: 1}}}
	b := &stubExtender{extendEntries: []gollm.ModelEntry{{ID: "b:1", MaxOutputTokens: 1}, {ID: "b:2", MaxOutputTokens: 1}}}
	RegisterExtender(a)
	RegisterExtender(b)

	entries, err := Extend(context.Background(), "p1")
	if err != nil {
		t.Fatalf("Extend: %v", err)
	}
	gotIDs := make([]string, 0, len(entries))
	for _, e := range entries {
		gotIDs = append(gotIDs, e.ID)
	}
	wantIDs := []string{"a:1", "b:1", "b:2"}
	if strings.Join(gotIDs, ",") != strings.Join(wantIDs, ",") {
		t.Errorf("Extend order = %v, want %v", gotIDs, wantIDs)
	}
	if a.lastProjectID != "p1" || b.lastProjectID != "p1" {
		t.Errorf("project ID not forwarded: a=%q b=%q", a.lastProjectID, b.lastProjectID)
	}
}

func TestExtend_TransportErrorShortCircuits(t *testing.T) {
	t.Cleanup(ResetForTest)
	ResetForTest()
	a := &stubExtender{extendErr: errors.New("network blip")}
	b := &stubExtender{extendEntries: []gollm.ModelEntry{{ID: "b:1"}}}
	RegisterExtender(a)
	RegisterExtender(b)

	_, err := Extend(context.Background(), "p1")
	if err == nil || !strings.Contains(err.Error(), "network blip") {
		t.Fatalf("Extend must surface the first extender's error, got %v", err)
	}
	if b.extendCalls != 0 {
		t.Errorf("Extend should short-circuit on error; later extender saw %d calls", b.extendCalls)
	}
}

func TestResolve_EmptyModelIDRejected(t *testing.T) {
	t.Cleanup(ResetForTest)
	ResetForTest()
	if _, err := Resolve(context.Background(), ""); err == nil {
		t.Fatal("Resolve with empty model ID must error")
	}
}

func TestResolve_NotFoundWhenNoExtenders(t *testing.T) {
	t.Cleanup(ResetForTest)
	ResetForTest()
	_, err := Resolve(context.Background(), "ft:gpt-foo")
	if !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("Resolve with no extenders should return ErrModelNotFound, got %v", err)
	}
}

func TestResolve_FirstNonNotFoundWins(t *testing.T) {
	t.Cleanup(ResetForTest)
	ResetForTest()
	a := &stubExtender{resolveErr: ErrModelNotFound}
	want := gollm.ModelEntry{ID: "ft:abc"}
	b := &stubExtender{resolveEntry: &want}
	c := &stubExtender{resolveEntry: &gollm.ModelEntry{ID: "should-not-be-reached"}}
	RegisterExtender(a)
	RegisterExtender(b)
	RegisterExtender(c)

	got, err := Resolve(context.Background(), "ft:abc")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got == nil || got.ID != want.ID {
		t.Errorf("Resolve = %+v, want entry with ID %q", got, want.ID)
	}
	if c.resolveCalls != 0 {
		t.Errorf("Resolve should stop after first match; trailing extender saw %d calls", c.resolveCalls)
	}
}

func TestResolve_TransportErrorPropagates(t *testing.T) {
	t.Cleanup(ResetForTest)
	ResetForTest()
	a := &stubExtender{resolveErr: errors.New("timeout")}
	b := &stubExtender{resolveEntry: &gollm.ModelEntry{ID: "ft:abc"}}
	RegisterExtender(a)
	RegisterExtender(b)

	_, err := Resolve(context.Background(), "ft:abc")
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("Resolve must surface transport errors, got %v", err)
	}
	if b.resolveCalls != 0 {
		t.Error("Resolve must not consult later extenders after a transport error from an earlier one")
	}
}

func TestResolve_AllExtendersDisclaimOwnership(t *testing.T) {
	t.Cleanup(ResetForTest)
	ResetForTest()
	RegisterExtender(&stubExtender{resolveErr: ErrModelNotFound})
	RegisterExtender(&stubExtender{resolveErr: ErrModelNotFound})

	_, err := Resolve(context.Background(), "ft:nobody")
	if !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("Resolve must collapse to ErrModelNotFound when every extender disclaims, got %v", err)
	}
}

func TestResetForTest_ClearsRegistry(t *testing.T) {
	t.Cleanup(ResetForTest)
	RegisterExtender(&stubExtender{})
	ResetForTest()
	if got := RegisteredExtenders(); len(got) != 0 {
		t.Errorf("ResetForTest left %d extenders behind", len(got))
	}
}
