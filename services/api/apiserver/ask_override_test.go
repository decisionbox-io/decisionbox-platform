package apiserver

import (
	"net/http"
	"testing"
)

func TestAskOverride_RegisterAndGetRoundTrip(t *testing.T) {
	defer ResetAskOverrideForTest()
	ResetAskOverrideForTest()

	if _, ok := GetAskOverride(); ok {
		t.Fatal("expected no override before Register")
	}

	want := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	RegisterAskOverride(want)

	got, ok := GetAskOverride()
	if !ok {
		t.Fatal("expected ok=true after Register")
	}
	if got == nil {
		t.Fatal("handler unexpectedly nil after round-trip")
	}
	if _, ok := got.(http.HandlerFunc); !ok {
		t.Fatalf("got handler is %T, want http.HandlerFunc", got)
	}
}

func TestAskOverride_ResetClearsRegistration(t *testing.T) {
	defer ResetAskOverrideForTest()
	ResetAskOverrideForTest()

	RegisterAskOverride(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if _, ok := GetAskOverride(); !ok {
		t.Fatal("setup: expected override registered")
	}
	ResetAskOverrideForTest()
	if _, ok := GetAskOverride(); ok {
		t.Fatal("expected ResetAskOverrideForTest to clear the slot")
	}
}

func TestAskOverride_DoubleRegisterPanics(t *testing.T) {
	defer ResetAskOverrideForTest()
	ResetAskOverrideForTest()

	RegisterAskOverride(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on duplicate Register, got none")
		}
	}()
	RegisterAskOverride(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
}

func TestAskOverride_NilRegisterPanics(t *testing.T) {
	defer ResetAskOverrideForTest()
	ResetAskOverrideForTest()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil handler, got none")
		}
	}()
	RegisterAskOverride(nil)
}
