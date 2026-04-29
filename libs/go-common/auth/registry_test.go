package auth

import (
	"context"
	"testing"
)

func TestGetProvider_DefaultIsNoAuth(t *testing.T) {
	// Reset registry
	registryMu.Lock()
	registeredProvider = nil
	registryMu.Unlock()

	p := GetProvider()
	if p == nil {
		t.Fatal("GetProvider() returned nil")
	}

	user, err := p.ValidateToken(context.Background(), "")
	if err != nil {
		t.Fatalf("ValidateToken() error = %v", err)
	}
	if user.Sub != "anonymous" {
		t.Errorf("Sub = %q, want %q (NoAuth default)", user.Sub, "anonymous")
	}
}

func TestRegisterProvider_OverridesDefault(t *testing.T) {
	// Reset registry
	registryMu.Lock()
	registeredProvider = nil
	registryMu.Unlock()

	// Register a custom provider
	custom := &NoAuthProvider{}
	RegisterProvider(custom)

	p := GetProvider()
	if p != custom {
		t.Error("GetProvider() should return the registered provider")
	}

	// Cleanup
	registryMu.Lock()
	registeredProvider = nil
	registryMu.Unlock()
}

func TestRegisterProvider_NilFallsBackToNoAuth(t *testing.T) {
	// Reset registry
	registryMu.Lock()
	registeredProvider = nil
	registryMu.Unlock()

	p := GetProvider()
	if p == nil {
		t.Fatal("GetProvider() should never return nil")
	}
}

func TestPeekProvider_ReturnsNilWhenUnregistered(t *testing.T) {
	registryMu.Lock()
	registeredProvider = nil
	registryMu.Unlock()

	if p := PeekProvider(); p != nil {
		t.Errorf("PeekProvider() = %v, want nil before any registration", p)
	}
}

func TestPeekProvider_ReturnsRegisteredWithoutFallback(t *testing.T) {
	registryMu.Lock()
	registeredProvider = nil
	registryMu.Unlock()

	custom := &NoAuthProvider{}
	RegisterProvider(custom)
	t.Cleanup(func() {
		registryMu.Lock()
		registeredProvider = nil
		registryMu.Unlock()
	})

	if p := PeekProvider(); p != custom {
		t.Errorf("PeekProvider() = %v, want %v (the registered provider)", p, custom)
	}
}
