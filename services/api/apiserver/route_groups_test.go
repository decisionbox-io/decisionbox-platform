package apiserver

import (
	"net/http"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
}

func TestRegisterRouteGroup_RejectsEmptyPrefix(t *testing.T) {
	t.Cleanup(ResetRouteGroupsForTest)
	ResetRouteGroupsForTest()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("RegisterRouteGroup with empty prefix must panic")
		}
	}()
	RegisterRouteGroup("", okHandler())
}

func TestRegisterRouteGroup_RejectsMissingLeadingSlash(t *testing.T) {
	t.Cleanup(ResetRouteGroupsForTest)
	ResetRouteGroupsForTest()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("RegisterRouteGroup must require a leading slash on the prefix")
		}
	}()
	RegisterRouteGroup("api/foo", okHandler())
}

func TestRegisterRouteGroup_RejectsTrailingSlash(t *testing.T) {
	t.Cleanup(ResetRouteGroupsForTest)
	ResetRouteGroupsForTest()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("RegisterRouteGroup must reject trailing-slash prefixes — the mux appends one internally")
		}
	}()
	RegisterRouteGroup("/api/foo/", okHandler())
}

func TestRegisterRouteGroup_RejectsNilHandler(t *testing.T) {
	t.Cleanup(ResetRouteGroupsForTest)
	ResetRouteGroupsForTest()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("RegisterRouteGroup(nil) must panic — nil handler is a programmer error")
		}
	}()
	RegisterRouteGroup("/api/foo", nil)
}

func TestRegisterRouteGroup_RejectsDuplicatePrefix(t *testing.T) {
	t.Cleanup(ResetRouteGroupsForTest)
	ResetRouteGroupsForTest()
	RegisterRouteGroup("/api/foo", okHandler())
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("registering the same prefix twice must panic to catch typos at boot")
		}
	}()
	RegisterRouteGroup("/api/foo", okHandler())
}

func TestRegisterRouteGroup_StoresInRegistrationOrder(t *testing.T) {
	t.Cleanup(ResetRouteGroupsForTest)
	ResetRouteGroupsForTest()
	RegisterRouteGroup("/api/a", okHandler())
	RegisterRouteGroup("/api/b", okHandler())
	RegisterRouteGroup("/api/c", okHandler())

	got := RegisteredRouteGroups()
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].Prefix != "/api/a" || got[1].Prefix != "/api/b" || got[2].Prefix != "/api/c" {
		t.Errorf("prefix order = %v, want [/api/a /api/b /api/c]", []string{got[0].Prefix, got[1].Prefix, got[2].Prefix})
	}
}

func TestRegisteredRouteGroups_ReturnsCopy(t *testing.T) {
	t.Cleanup(ResetRouteGroupsForTest)
	ResetRouteGroupsForTest()
	RegisterRouteGroup("/api/a", okHandler())
	got := RegisteredRouteGroups()
	got[0].Prefix = "/mutated"
	again := RegisteredRouteGroups()
	if again[0].Prefix != "/api/a" {
		t.Errorf("RegisteredRouteGroups returned a live alias — callers can mutate registry, got %q", again[0].Prefix)
	}
}

func TestResetRouteGroupsForTest_Clears(t *testing.T) {
	t.Cleanup(ResetRouteGroupsForTest)
	RegisterRouteGroup("/api/a", okHandler())
	ResetRouteGroupsForTest()
	if got := RegisteredRouteGroups(); len(got) != 0 {
		t.Errorf("ResetRouteGroupsForTest left %d groups behind", len(got))
	}
}
