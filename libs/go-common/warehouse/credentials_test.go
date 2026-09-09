package warehouse

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

// cloudSafeKey is the alphabet the strictest backend in play (Azure Key Vault)
// accepts for a secret name. A key outside it fails to store at all.
var cloudSafeKey = regexp.MustCompile(`^[a-zA-Z0-9-]{1,127}$`)

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

// The id is composed INTO a key rather than being one. That is what stops a
// datasource config — writable by anyone who can edit the project — from naming
// another secret the project holds.
func TestSharedCredentialKey_CannotNameAnotherProjectSecret(t *testing.T) {
	for _, id := range []string{
		LegacyCredentialsKey,
		CredentialsKey("wh_1"),
		"llm-credentials",
		"embedding-credentials",
		"slack-bot-token",
	} {
		key, ok := SharedCredentialKey(id)
		if !ok {
			continue
		}
		if key == id {
			t.Errorf("SharedCredentialKey(%q) returned the id itself", id)
		}
		if !strings.HasPrefix(key, sharedCredentialPrefix+"-") {
			t.Errorf("SharedCredentialKey(%q) = %q, which is outside the shared namespace", id, key)
		}
	}
}

// The namespace must not overlap the derived per-datasource keys either, or a
// shared credential could shadow one datasource's own.
func TestSharedCredentialKey_DoesNotCollideWithADerivedKey(t *testing.T) {
	derived := map[string]bool{}
	for _, id := range []string{"", DefaultWarehouseID, "wh_1", "wh_2", "wh_b"} {
		derived[CredentialsKey(id)] = true
	}
	for _, id := range []string{"conn-1", "wh1", DefaultWarehouseID, "a"} {
		key, ok := SharedCredentialKey(id)
		if !ok {
			t.Fatalf("SharedCredentialKey(%q) was refused", id)
		}
		if derived[key] {
			t.Errorf("SharedCredentialKey(%q) = %q, which is a derived per-datasource key", id, key)
		}
	}
}

// Distinct ids must not share a slot, or one connection's grant would answer for
// another's.
func TestSharedCredentialKey_IsDistinctPerID(t *testing.T) {
	seen := map[string]string{}
	for _, id := range []string{"conn-1", "conn-2", "conn1", "CONN-1"} {
		key, ok := SharedCredentialKey(id)
		if !ok {
			t.Fatalf("SharedCredentialKey(%q) was refused", id)
		}
		if prev, clash := seen[key]; clash {
			t.Fatalf("%q collides with %q on key %q", id, prev, key)
		}
		seen[key] = id
	}
}

// The composed key has to be storable by every backend, whatever the id was.
func TestSharedCredentialKey_StaysInTheStrictestAlphabet(t *testing.T) {
	for _, id := range []string{"conn-1", "Odd.ID:v2", "with/slash", "../other", "a b", "CONN-1"} {
		key, ok := SharedCredentialKey(id)
		if !ok {
			continue
		}
		if !cloudSafeKey.MatchString(key) {
			t.Errorf("SharedCredentialKey(%q) = %q, which a cloud backend would reject", id, key)
		}
	}
}

// An id no key can be formed from is refused rather than silently producing one.
// An empty id would name the namespace's own root; an over-long one, or one
// outside the alphabet, a name Azure Key Vault rejects at write time, where
// nobody can see it.
func TestSharedCredentialKey_RefusesWhatItCannotName(t *testing.T) {
	for _, id := range []string{
		"", "   ", strings.Repeat("a", maxSharedCredentialID+1),
		"under_score", "with:colon", "with/slash", "with.dot", "../other",
	} {
		if _, ok := SharedCredentialKey(id); ok {
			t.Errorf("SharedCredentialKey(%q) was accepted", id)
		}
	}
}

// The limit Azure enforces is on the name it composes, not on this key alone.
// A connection id is a UUID, and the composed name for a typical deployment has
// to stay inside 127 characters or the credential cannot be stored at all.
func TestSharedCredentialKey_FitsAzuresComposedSecretName(t *testing.T) {
	const uuid = "2f1c0b1e-9a2d-4c3b-8f7e-6d5a4b3c2d1e"
	key, ok := SharedCredentialKey(uuid)
	if !ok {
		t.Fatal("a UUID id was refused")
	}
	// namespace + "-" + a 24-character Mongo ObjectID + "-" + key.
	composed := len("decisionbox") + 1 + 24 + 1 + len(key)
	if composed > 127 {
		t.Fatalf("composed Azure secret name is %d characters, over the 127 limit", composed)
	}
}
