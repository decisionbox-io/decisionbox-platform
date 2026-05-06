package agentplugin

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// panickingNameProvider has a Name() that panics conditionally —
// the failure mode the round-2 review surfaced. The panic is gated
// behind a toggle so the provider can be registered cleanly (Register
// reads Name() to dedup), then flipped before invocation. Mirrors a
// real-world bug class where Name() works at startup and breaks later
// (config reload swapping a misbehaving impl into the slot, etc.).
type panickingNameProvider struct {
	registeredName string
	panicNow       *bool
	sectionFn      func() (string, error)
}

func (p panickingNameProvider) Name() string {
	if p.panicNow != nil && *p.panicNow {
		panic("Name() blew up")
	}
	return p.registeredName
}

func (p panickingNameProvider) Section(context.Context, string, string, ContextProviderOpts) (string, error) {
	if p.sectionFn != nil {
		return p.sectionFn()
	}
	return "", nil
}

func TestRenderSections_NamePanic_DoesNotEscape(t *testing.T) {
	defer ResetForTest()
	ResetForTest()

	// First provider is fine and emits a section. Second has a
	// Name() that panics AND a Section() that returns an error so the
	// onError path is exercised. RenderSections must finish, return
	// the first provider's section, and call onError without
	// panicking.
	RegisterContextProvider(stubProviderWithSection{name: "fine", section: "alpha"})
	panicSwitch := false
	RegisterContextProvider(panickingNameProvider{
		registeredName: "panic-later",
		panicNow:       &panicSwitch,
		sectionFn:      func() (string, error) { return "", errors.New("boom") },
	})
	panicSwitch = true // Flip the switch — Name() now panics.

	var onErrorName string
	var onErrorErr error
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("RenderSections must contain Name() panic; got panic=%v", r)
		}
	}()
	got := RenderSections(context.Background(), "p", "q", ContextProviderOpts{Limit: 1}, func(name string, err error) {
		onErrorName = name
		onErrorErr = err
	})

	if !strings.Contains(got, "alpha") {
		t.Fatalf("expected first provider's section in output; got %q", got)
	}
	if onErrorErr == nil {
		t.Fatal("expected onError to fire for the second provider")
	}
	// The panicking-Name provider should report through onError with
	// the placeholder name rather than escape.
	if !strings.Contains(onErrorName, "unknown") {
		t.Fatalf("onError name = %q, want a placeholder containing \"unknown\"", onErrorName)
	}
}

func TestRenderSections_SectionPanic_NameAlsoPanics_DoesNotEscape(t *testing.T) {
	defer ResetForTest()
	ResetForTest()

	// Combined failure: Name() panics AND Section() panics. The
	// recovery path used to call p.Name() to format the error
	// message — that re-panicked and escaped. With the safeName
	// capture, the recovered error carries the placeholder name and
	// RenderSections finishes cleanly.
	panicSwitch := false
	RegisterContextProvider(panickingNameProvider{
		registeredName: "panic-pair",
		panicNow:       &panicSwitch,
		sectionFn:      func() (string, error) { panic("Section blew up too") },
	})
	panicSwitch = true

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("RenderSections must contain combined Name+Section panic; got panic=%v", r)
		}
	}()
	var seenErr error
	_ = RenderSections(context.Background(), "p", "q", ContextProviderOpts{Limit: 1}, func(_ string, err error) {
		seenErr = err
	})
	if seenErr == nil {
		t.Fatal("expected onError to fire when Section panicked")
	}
	if !strings.Contains(seenErr.Error(), "unknown") {
		t.Fatalf("error = %q, want it to use the placeholder name", seenErr.Error())
	}
}

// stubProviderWithSection is the test stub with a configurable section
// body so the first-provider-OK case has visible output to assert.
type stubProviderWithSection struct {
	name    string
	section string
}

func (s stubProviderWithSection) Name() string { return s.name }
func (s stubProviderWithSection) Section(context.Context, string, string, ContextProviderOpts) (string, error) {
	return s.section, nil
}
