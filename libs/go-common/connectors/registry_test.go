package connectors

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
)

// mockConnector implements Connector for testing.
type mockConnector struct {
	kind string
}

func (m *mockConnector) Kind() string { return m.kind }
func (m *mockConnector) Exchange(_ context.Context, _, _ string) (Token, error) {
	return Token{RefreshToken: "rt", Scope: "test"}, nil
}
func (m *mockConnector) Refresh(_ context.Context, _ Token) (Token, error) {
	return Token{AccessToken: "at", Scope: "test"}, nil
}
func (m *mockConnector) PickerToken(_ context.Context, _ Token) (BrowserToken, error) {
	return BrowserToken{AccessToken: "bt", Scope: "narrow"}, nil
}
func (m *mockConnector) Enumerate(_ context.Context, _ Token, sel []PickedItem) ([]Item, error) {
	return make([]Item, len(sel)), nil
}
func (m *mockConnector) Download(_ context.Context, _ Token, _ Item) (io.ReadCloser, string, error) {
	return io.NopCloser(strings.NewReader("data")), "text/plain", nil
}

// resetRegistry clears the global registry for test isolation.
func resetRegistry() {
	registryMu.Lock()
	defer registryMu.Unlock()
	factories = make(map[string]Factory)
	factoryMeta = make(map[string]ProviderMeta)
}

func TestRegister(t *testing.T) {
	resetRegistry()

	Register("test-connector", func(Config) (Connector, error) {
		return &mockConnector{kind: "test-connector"}, nil
	})

	names := RegisteredConnectors()
	if len(names) != 1 || names[0] != "test-connector" {
		t.Fatalf("expected [test-connector], got %v", names)
	}
}

func TestRegisterNilFactory(t *testing.T) {
	resetRegistry()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for nil factory")
		}
		if msg := fmt.Sprintf("%v", r); !strings.Contains(msg, "nil") {
			t.Fatalf("expected panic message about nil, got: %s", msg)
		}
	}()

	Register("nil-factory", nil)
}

func TestRegisterDuplicate(t *testing.T) {
	resetRegistry()

	factory := func(Config) (Connector, error) { return &mockConnector{}, nil }
	Register("dup", factory)

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for duplicate registration")
		}
		if msg := fmt.Sprintf("%v", r); !strings.Contains(msg, "twice") {
			t.Fatalf("expected panic message about twice, got: %s", msg)
		}
	}()

	Register("dup", factory)
}

func TestRegisterWithMeta(t *testing.T) {
	resetRegistry()

	meta := ProviderMeta{
		Name:        "Test Drive",
		Description: "A test connector",
		ConfigFields: []ConfigField{
			{Key: "client_id", Label: "Client ID", Required: true, Type: "string", BrowserSafe: true},
			{Key: "client_secret", Label: "Client Secret", Required: true, Type: "credential"},
		},
	}
	RegisterWithMeta("gdrive-test", func(Config) (Connector, error) {
		return &mockConnector{kind: "gdrive-test"}, nil
	}, meta)

	metas := RegisteredMeta()
	if len(metas) != 1 {
		t.Fatalf("expected 1 meta, got %d", len(metas))
	}
	if metas[0].ID != "gdrive-test" {
		t.Fatalf("expected ID gdrive-test (set from name), got %s", metas[0].ID)
	}
	if metas[0].Name != "Test Drive" {
		t.Fatalf("expected Name 'Test Drive', got %s", metas[0].Name)
	}

	got, ok := GetMeta("gdrive-test")
	if !ok {
		t.Fatal("expected meta to be found")
	}
	// Assert the browser-safe flag survives so a config endpoint can filter on it.
	var sawBrowserSafe, sawCredential bool
	for _, f := range got.ConfigFields {
		if f.Key == "client_id" && f.BrowserSafe {
			sawBrowserSafe = true
		}
		if f.Key == "client_secret" && !f.BrowserSafe {
			sawCredential = true
		}
	}
	if !sawBrowserSafe || !sawCredential {
		t.Fatalf("expected client_id browser-safe and client_secret not, got %+v", got.ConfigFields)
	}

	if _, ok := GetMeta("nonexistent"); ok {
		t.Fatal("expected meta not found for nonexistent")
	}
}

func TestNewConnector(t *testing.T) {
	resetRegistry()

	Register("test", func(cfg Config) (Connector, error) {
		if cfg["client_id"] == "" {
			return nil, fmt.Errorf("client_id is required")
		}
		return &mockConnector{kind: "test"}, nil
	})

	// Success case
	c, err := NewConnector("test", Config{"client_id": "abc"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Kind() != "test" {
		t.Fatalf("expected kind test, got %s", c.Kind())
	}

	// Missing config → factory error
	if _, err = NewConnector("test", Config{}); err == nil {
		t.Fatal("expected error for missing client_id")
	}

	// Unknown connector → clear error, no panic (platform-only build path)
	_, err = NewConnector("unknown", Config{})
	if err == nil {
		t.Fatal("expected error for unknown connector")
	}
	if !strings.Contains(err.Error(), "unknown connector") {
		t.Fatalf("expected 'unknown connector' in error, got: %s", err.Error())
	}
}

func TestRegisteredConnectors(t *testing.T) {
	resetRegistry()

	for _, n := range []string{"a", "b", "c"} {
		Register(n, func(Config) (Connector, error) { return &mockConnector{}, nil })
	}

	names := RegisteredConnectors()
	if len(names) != 3 {
		t.Fatalf("expected 3 connectors, got %d", len(names))
	}
	set := make(map[string]bool)
	for _, n := range names {
		set[n] = true
	}
	for _, want := range []string{"a", "b", "c"} {
		if !set[want] {
			t.Fatalf("expected connector %q in list", want)
		}
	}
}
