package sources

import (
	"context"
	"errors"
	"testing"
)

type stubProvider struct {
	chunks []Chunk
	err    error
}

func (s *stubProvider) RetrieveContext(_ context.Context, _ string, _ string, _ RetrieveOpts) ([]Chunk, error) {
	return s.chunks, s.err
}

func TestGetProvider_DefaultIsNoOp(t *testing.T) {
	resetForTest()
	defer resetForTest()

	p := GetProvider()
	if p == nil {
		t.Fatal("GetProvider() returned nil")
	}

	chunks, err := p.RetrieveContext(context.Background(), "proj", "query", RetrieveOpts{})
	if err != nil {
		t.Fatalf("NoOp.RetrieveContext returned error: %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("NoOp returned %d chunks, want 0", len(chunks))
	}
}

func TestRegisterFactory_NilPanics(t *testing.T) {
	resetForTest()
	defer resetForTest()

	defer func() {
		if r := recover(); r == nil {
			t.Error("RegisterFactory(nil) should panic")
		}
	}()
	RegisterFactory(nil)
}

func TestRegisterFactory_TwicePanics(t *testing.T) {
	resetForTest()
	defer resetForTest()

	RegisterFactory(func(Dependencies) (Provider, error) { return &stubProvider{}, nil })

	defer func() {
		if r := recover(); r == nil {
			t.Error("Second RegisterFactory call should panic")
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

	// GetProvider must still return NoOp.
	p := GetProvider()
	chunks, _ := p.RetrieveContext(context.Background(), "proj", "q", RetrieveOpts{})
	if len(chunks) != 0 {
		t.Errorf("After no-op Configure, expected NoOp behavior; got %d chunks", len(chunks))
	}
}

func TestConfigure_ActivatesProvider(t *testing.T) {
	resetForTest()
	defer resetForTest()

	want := []Chunk{{SourceID: "s1", Text: "hello"}}
	RegisterFactory(func(Dependencies) (Provider, error) {
		return &stubProvider{chunks: want}, nil
	})

	if err := Configure(context.Background(), Dependencies{}); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}

	got, err := GetProvider().RetrieveContext(context.Background(), "proj", "q", RetrieveOpts{})
	if err != nil {
		t.Fatalf("RetrieveContext error = %v", err)
	}
	if len(got) != 1 || got[0].SourceID != "s1" {
		t.Errorf("got %#v, want chunk s1", got)
	}
}

func TestConfigure_FactoryError(t *testing.T) {
	resetForTest()
	defer resetForTest()

	wantErr := errors.New("boom")
	RegisterFactory(func(Dependencies) (Provider, error) {
		return nil, wantErr
	})

	err := Configure(context.Background(), Dependencies{})
	if err == nil || !errors.Is(err, wantErr) {
		t.Errorf("Configure error = %v, want wrap of %v", err, wantErr)
	}

	// Provider should remain NoOp after a failed Configure.
	chunks, _ := GetProvider().RetrieveContext(context.Background(), "proj", "q", RetrieveOpts{})
	if len(chunks) != 0 {
		t.Errorf("After failed Configure, expected NoOp; got %d chunks", len(chunks))
	}
}

func TestConfigure_ReplacesActiveProvider(t *testing.T) {
	resetForTest()
	defer resetForTest()

	first := []Chunk{{SourceID: "first"}}
	second := []Chunk{{SourceID: "second"}}

	calls := 0
	RegisterFactory(func(Dependencies) (Provider, error) {
		calls++
		if calls == 1 {
			return &stubProvider{chunks: first}, nil
		}
		return &stubProvider{chunks: second}, nil
	})

	if err := Configure(context.Background(), Dependencies{}); err != nil {
		t.Fatalf("first Configure error = %v", err)
	}
	if err := Configure(context.Background(), Dependencies{}); err != nil {
		t.Fatalf("second Configure error = %v", err)
	}

	got, _ := GetProvider().RetrieveContext(context.Background(), "proj", "q", RetrieveOpts{})
	if len(got) != 1 || got[0].SourceID != "second" {
		t.Errorf("after second Configure, got %#v, want chunk 'second'", got)
	}
}
