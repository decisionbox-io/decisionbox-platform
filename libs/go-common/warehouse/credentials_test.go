package warehouse

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

func TestCredentialsKey(t *testing.T) {
	// Unset / "default" resolve to the unmigrated primary key.
	for _, id := range []string{"", DefaultWarehouseID} {
		if got := CredentialsKey(id); got != LegacyCredentialsKey {
			t.Errorf("CredentialsKey(%q) = %q, want %q", id, got, LegacyCredentialsKey)
		}
	}

	// Additional warehouses must produce a key a cloud secret backend accepts as
	// a secret name: GCP allows [A-Za-z0-9_-], Azure only [A-Za-z0-9-]. The key
	// must therefore contain only alphanumerics and hyphens — no ':' or '_' — and
	// stay distinct + deterministic across ids (incl. ids that differ only by a
	// separator, which a naive sanitise would collide).
	safe := regexp.MustCompile(`^[A-Za-z0-9-]+$`)
	seen := map[string]string{}
	for _, id := range []string{"wh_b", "wh-b", "wh_snowflake", "WH:weird/id"} {
		got := CredentialsKey(id)
		if !safe.MatchString(got) {
			t.Errorf("CredentialsKey(%q) = %q is not secret-backend-safe ([A-Za-z0-9-])", id, got)
		}
		if got == LegacyCredentialsKey {
			t.Errorf("CredentialsKey(%q) = %q collides with the legacy/primary key", id, got)
		}
		if prev, ok := seen[got]; ok {
			t.Errorf("CredentialsKey(%q) collides with CredentialsKey(%q) = %q", id, prev, got)
		}
		seen[got] = id
		if again := CredentialsKey(id); again != got {
			t.Errorf("CredentialsKey(%q) not deterministic: %q vs %q", id, got, again)
		}
	}
}

func TestWarehouseIDContext(t *testing.T) {
	ctx := context.Background()
	if got := WarehouseIDFromContext(ctx); got != "" {
		t.Errorf("bare context should carry no warehouse id, got %q", got)
	}
	ctx = WithWarehouseID(ctx, "wh_a")
	if got := WarehouseIDFromContext(ctx); got != "wh_a" {
		t.Errorf("want wh_a, got %q", got)
	}
	// Warehouse id and project id are independent carriers.
	ctx = WithProjectID(ctx, "p1")
	if WarehouseIDFromContext(ctx) != "wh_a" || ProjectIDFromContext(ctx) != "p1" {
		t.Errorf("project and warehouse ids must not clobber each other")
	}
}

// The alphabet is the strictest backend's (Azure Key Vault). A ref outside it
// cannot name a key this system wrote, and reading it would fail at the cloud
// provider rather than here.
func TestValidCredentialRef(t *testing.T) {
	valid := []string{
		LegacyCredentialsKey,
		CredentialsKey("wh_1"),
		"connector-refresh-token-6f3a1b2c",
		"a",
	}
	for _, ref := range valid {
		if !ValidCredentialRef(ref) {
			t.Errorf("ValidCredentialRef(%q) = false, want true", ref)
		}
	}

	invalid := []string{
		"", "   ", "../other", "Has-Capitals", "under_score", "with:colon",
		"with/slash", "with.dot", strings.Repeat("a", 128),
	}
	for _, ref := range invalid {
		if ValidCredentialRef(ref) {
			t.Errorf("ValidCredentialRef(%q) = true, want false", ref)
		}
	}
}

// Every key this system produces has to pass its own check, or a datasource
// pointed at a real slot would be refused.
func TestValidCredentialRef_AcceptsEveryKeyCredentialsKeyProduces(t *testing.T) {
	for _, id := range []string{"", DefaultWarehouseID, "wh_1", "wh_b", "Odd.ID:v2"} {
		if key := CredentialsKey(id); !ValidCredentialRef(key) {
			t.Errorf("CredentialsKey(%q) = %q, which ValidCredentialRef rejects", id, key)
		}
	}
}
