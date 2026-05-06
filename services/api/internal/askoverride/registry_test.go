package askoverride

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGet_DefaultIsUnset(t *testing.T) {
	resetForTest()
	defer resetForTest()

	if h, ok := Get(); ok || h != nil {
		t.Fatalf("expected unset; got (%v, %v)", h, ok)
	}
}

func TestRegister_NilPanics(t *testing.T) {
	resetForTest()
	defer resetForTest()

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Register(nil) must panic")
		}
	}()
	Register(nil)
}

func TestRegister_DoubleRegisterPanics(t *testing.T) {
	resetForTest()
	defer resetForTest()

	Register(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("re-Register must panic")
		}
	}()
	Register(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
}

// TestResetForTest_ExportedHelperClearsSlot covers the exported
// ResetForTest wrapper that callers outside this package use (the
// apiserver tests, in particular). The unexported resetForTest is
// already exercised throughout this file's setup.
func TestResetForTest_ExportedHelperClearsSlot(t *testing.T) {
	resetForTest()
	defer resetForTest()
	Register(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	if _, ok := Get(); !ok {
		t.Fatal("setup: expected Register to install handler")
	}
	ResetForTest()
	if _, ok := Get(); ok {
		t.Fatal("ResetForTest did not clear the slot")
	}
}

func TestGet_ReturnsRegisteredHandler(t *testing.T) {
	resetForTest()
	defer resetForTest()

	Register(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	h, ok := Get()
	if !ok || h == nil {
		t.Fatal("Get returned no handler after Register")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/projects/x/ask", nil))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("override handler not invoked; got status %d", rec.Code)
	}
}
