package packgen

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type stubProvider struct {
	gen     *GenerateResult
	genErr  error
	regen   *RegenerateSectionResult
	regErr  error
	lastReq GenerateRequest
}

func (s *stubProvider) Generate(_ context.Context, req GenerateRequest) (*GenerateResult, error) {
	s.lastReq = req
	return s.gen, s.genErr
}

func (s *stubProvider) RegenerateSection(_ context.Context, _ RegenerateSectionRequest) (*RegenerateSectionResult, error) {
	return s.regen, s.regErr
}

func TestGetProvider_DefaultIsNoOp(t *testing.T) {
	resetForTest()
	defer resetForTest()

	p := GetProvider()
	if p == nil {
		t.Fatal("GetProvider() returned nil")
	}
	if _, ok := p.(noopProvider); !ok {
		t.Fatalf("GetProvider() default = %T, want noopProvider", p)
	}

	if _, err := p.Generate(context.Background(), GenerateRequest{}); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("noop.Generate err = %v, want ErrNotConfigured", err)
	}
	if _, err := p.RegenerateSection(context.Background(), RegenerateSectionRequest{}); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("noop.RegenerateSection err = %v, want ErrNotConfigured", err)
	}
}

func TestIsAvailable_DefaultFalse(t *testing.T) {
	resetForTest()
	defer resetForTest()

	if IsAvailable() {
		t.Error("IsAvailable() = true on a fresh registry, want false")
	}
}

func TestRegisterFactory_NilPanics(t *testing.T) {
	resetForTest()
	defer resetForTest()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("RegisterFactory(nil) should panic")
		}
		msg, ok := r.(string)
		if !ok || msg != "packgen: RegisterFactory called with nil factory" {
			t.Errorf("panic message = %v, want nil-factory message", r)
		}
	}()
	RegisterFactory(nil)
}

func TestRegisterFactory_TwicePanics(t *testing.T) {
	resetForTest()
	defer resetForTest()

	RegisterFactory(func(Dependencies) (Provider, error) { return &stubProvider{}, nil })

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("Second RegisterFactory call should panic")
		}
		msg, ok := r.(string)
		if !ok || msg != "packgen: RegisterFactory called twice" {
			t.Errorf("panic message = %v, want twice-call message", r)
		}
	}()
	RegisterFactory(func(Dependencies) (Provider, error) { return &stubProvider{}, nil })
}

func TestConfigure_NoFactoryIsNoOp(t *testing.T) {
	resetForTest()
	defer resetForTest()

	if err := Configure(context.Background(), Dependencies{}); err != nil {
		t.Fatalf("Configure with no factory should be a no-op, got: %v", err)
	}

	if IsAvailable() {
		t.Error("IsAvailable() = true after Configure with no factory, want false")
	}

	p := GetProvider()
	if _, err := p.Generate(context.Background(), GenerateRequest{}); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Generate after no-factory Configure: err = %v, want ErrNotConfigured", err)
	}
}

func TestConfigure_ActivatesProvider(t *testing.T) {
	resetForTest()
	defer resetForTest()

	want := &GenerateResult{RunID: "run-1", PackSlug: "acme-gaming", Attempts: 1}
	stub := &stubProvider{gen: want}
	RegisterFactory(func(Dependencies) (Provider, error) { return stub, nil })

	if err := Configure(context.Background(), Dependencies{}); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}

	if !IsAvailable() {
		t.Error("IsAvailable() = false after successful Configure, want true")
	}

	got, err := GetProvider().Generate(context.Background(), GenerateRequest{ProjectID: "p1", PackSlug: "acme-gaming"})
	if err != nil {
		t.Fatalf("Generate error = %v", err)
	}
	if got.PackSlug != "acme-gaming" || got.Attempts != 1 {
		t.Errorf("got %#v, want %#v", got, want)
	}
	if stub.lastReq.ProjectID != "p1" {
		t.Errorf("stub did not receive request: lastReq = %#v", stub.lastReq)
	}
}

func TestConfigure_FactoryError(t *testing.T) {
	resetForTest()
	defer resetForTest()

	wantErr := errors.New("boom")
	RegisterFactory(func(Dependencies) (Provider, error) { return nil, wantErr })

	err := Configure(context.Background(), Dependencies{})
	if err == nil || !errors.Is(err, wantErr) {
		t.Errorf("Configure error = %v, want wrap of %v", err, wantErr)
	}

	if IsAvailable() {
		t.Error("IsAvailable() = true after factory error, want false")
	}

	if _, err := GetProvider().Generate(context.Background(), GenerateRequest{}); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Generate after failed Configure: err = %v, want ErrNotConfigured", err)
	}
}

func TestConfigure_ReplacesActiveProvider(t *testing.T) {
	resetForTest()
	defer resetForTest()

	first := &stubProvider{gen: &GenerateResult{PackSlug: "first"}}
	second := &stubProvider{gen: &GenerateResult{PackSlug: "second"}}

	calls := 0
	RegisterFactory(func(Dependencies) (Provider, error) {
		calls++
		if calls == 1 {
			return first, nil
		}
		return second, nil
	})

	if err := Configure(context.Background(), Dependencies{}); err != nil {
		t.Fatalf("first Configure error = %v", err)
	}
	if err := Configure(context.Background(), Dependencies{}); err != nil {
		t.Fatalf("second Configure error = %v", err)
	}

	got, _ := GetProvider().Generate(context.Background(), GenerateRequest{})
	if got.PackSlug != "second" {
		t.Errorf("after second Configure, got pack %q, want %q", got.PackSlug, "second")
	}
}

func TestSetProviderForTest_BypassesFactory(t *testing.T) {
	resetForTest()
	defer resetForTest()

	stub := &stubProvider{regen: &RegenerateSectionResult{Section: "categories", PackSlug: "acme"}}
	SetProviderForTest(stub)

	if !IsAvailable() {
		t.Error("IsAvailable() = false after SetProviderForTest, want true")
	}

	got, err := GetProvider().RegenerateSection(context.Background(), RegenerateSectionRequest{Section: "categories"})
	if err != nil {
		t.Fatalf("RegenerateSection error = %v", err)
	}
	if got.Section != "categories" {
		t.Errorf("got section %q, want %q", got.Section, "categories")
	}
}

func TestResetForTest_ClearsState(t *testing.T) {
	resetForTest()
	RegisterFactory(func(Dependencies) (Provider, error) { return &stubProvider{}, nil })
	if err := Configure(context.Background(), Dependencies{}); err != nil {
		t.Fatalf("Configure error = %v", err)
	}
	if !IsAvailable() {
		t.Fatal("IsAvailable() should be true after Configure")
	}

	ResetForTest()

	if IsAvailable() {
		t.Error("IsAvailable() = true after ResetForTest, want false")
	}
	if _, err := GetProvider().Generate(context.Background(), GenerateRequest{}); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Generate after ResetForTest: err = %v, want ErrNotConfigured", err)
	}
}

// readOnlyProvider is a non-mutating stub used by the concurrency test.
// Using stubProvider there would race on its lastReq field, but that
// race is in the test fixture, not in production code under test.
type readOnlyProvider struct{ result *GenerateResult }

func (r readOnlyProvider) Generate(_ context.Context, _ GenerateRequest) (*GenerateResult, error) {
	return r.result, nil
}

func (r readOnlyProvider) RegenerateSection(_ context.Context, _ RegenerateSectionRequest) (*RegenerateSectionResult, error) {
	return nil, nil
}

// TestGetProvider_ConcurrentCalls exercises the read path under contention to
// catch any locking regressions. The race detector (go test -race) is the
// real test here; we just need many concurrent callers to exercise the lock.
func TestGetProvider_ConcurrentCalls(t *testing.T) {
	resetForTest()
	defer resetForTest()

	SetProviderForTest(readOnlyProvider{result: &GenerateResult{PackSlug: "x"}})

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := GetProvider()
			_, _ = p.Generate(context.Background(), GenerateRequest{})
		}()
	}
	wg.Wait()
}

// TestGenerateResult_AsyncShape documents the contract that callers rely on
// when distinguishing sync from async generation results. Locking this in
// with a test means a future refactor can't quietly drop the discriminator.
func TestGenerateResult_AsyncShape(t *testing.T) {
	syncResult := GenerateResult{RunID: "r1", PackSlug: "pack-1", Attempts: 2}
	if syncResult.Async {
		t.Error("zero-value Async should be false (synchronous)")
	}

	asyncResult := GenerateResult{RunID: "r2", Async: true}
	if asyncResult.PackSlug != "" {
		t.Error("async result should not advertise a PackSlug yet")
	}
	if asyncResult.Attempts != 0 {
		t.Error("async result should not advertise Attempts yet")
	}
}
