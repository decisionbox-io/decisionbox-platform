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

// A credential obtained for one consumer must not be addressable by another.
// Some providers make that an exfiltration rather than a failure: Postgres reads
// credentials_json as its password and host from config, so a datasource naming
// an analytics connection's slot and an attacker's host would send that
// connection's refresh token to it.
func TestSharedCredentialKey_BindsTheCredentialToItsConsumer(t *testing.T) {
	const id = "2f1c0b1e-9a2d-4c3b-8f7e-6d5a4b3c2d1e"
	forGA4, ok := SharedCredentialKey("ga4", id)
	if !ok {
		t.Fatal("SharedCredentialKey refused a plain consumer and id")
	}
	forPostgres, ok := SharedCredentialKey("postgres", id)
	if !ok {
		t.Fatal("SharedCredentialKey refused a plain consumer and id")
	}
	if forGA4 == forPostgres {
		t.Fatal("two providers share a slot for the same id")
	}

	// And several datasources of the SAME provider do share one — that is the
	// whole point of a shared credential.
	again, _ := SharedCredentialKey("ga4", id)
	if again != forGA4 {
		t.Fatal("the same consumer and id produced two different slots")
	}
}

// The parts are joined with a separator that cannot appear in a key, so no pair
// of values can be rearranged into another pair's slot — which a hyphen-joined
// key could be when one provider slug is a prefix of another.
func TestSharedCredentialKey_PartsCannotBeRearranged(t *testing.T) {
	a, _ := SharedCredentialKey("ga4", "beta-conn-1")
	b, _ := SharedCredentialKey("ga4-beta", "conn-1")
	if a == b {
		t.Fatal("a crafted id reached another provider's slot")
	}
}

// The key must not be the id, or a datasource config — writable by anyone who
// can edit the project — could name another secret the project holds.
func TestSharedCredentialKey_CannotNameAnotherProjectSecret(t *testing.T) {
	for _, id := range []string{
		LegacyCredentialsKey,
		CredentialsKey("wh_1"),
		"llm-credentials",
		"embedding-credentials",
		"slack-bot-token",
	} {
		key, ok := SharedCredentialKey("ga4", id)
		if !ok {
			t.Fatalf("SharedCredentialKey refused %q", id)
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
	for _, id := range []string{"conn-1", "wh_1", DefaultWarehouseID, "a"} {
		key, ok := SharedCredentialKey("ga4", id)
		if !ok {
			t.Fatalf("SharedCredentialKey(%q) was refused", id)
		}
		if derived[key] {
			t.Errorf("SharedCredentialKey(%q) = %q, which is a derived per-datasource key", id, key)
		}
	}
}

// Distinct ids must not share a slot, or one connection's grant would answer for
// another's — including ids that differ only in case, which Azure Key Vault
// would otherwise fold together.
func TestSharedCredentialKey_IsDistinctPerID(t *testing.T) {
	seen := map[string]string{}
	for _, id := range []string{"conn-1", "conn-2", "conn_1", "CONN-1"} {
		key, ok := SharedCredentialKey("ga4", id)
		if !ok {
			t.Fatalf("SharedCredentialKey(%q) was refused", id)
		}
		if prev, clash := seen[key]; clash {
			t.Fatalf("%q collides with %q on key %q", id, prev, key)
		}
		seen[key] = id
	}
}

// The key has to be storable by every backend whatever the inputs were, and it
// is fixed-width — so the name Azure Key Vault composes around it fits, for any
// id and any provider slug.
func TestSharedCredentialKey_IsAlwaysStorable(t *testing.T) {
	for _, id := range []string{"conn-1", "Odd.ID:v2", "with/slash", "../other", "a b", strings.Repeat("x", 500)} {
		key, ok := SharedCredentialKey("a-very-long-datasource-provider-slug", id)
		if !ok {
			t.Fatalf("SharedCredentialKey(%q) was refused", id)
		}
		if !cloudSafeKey.MatchString(key) {
			t.Errorf("SharedCredentialKey(%q) = %q, which a cloud backend would reject", id, key)
		}
		// namespace + "-" + a 24-character Mongo ObjectID + "-" + key.
		if composed := len("decisionbox") + 1 + 24 + 1 + len(key); composed > 127 {
			t.Errorf("composed Azure secret name is %d characters, over the 127 limit", composed)
		}
	}
}

// Half a name is not a name: an empty consumer or id would put every caller that
// omitted one in the same slot.
func TestSharedCredentialKey_RefusesHalfAName(t *testing.T) {
	if _, ok := SharedCredentialKey("", "conn-1"); ok {
		t.Error("an empty consumer was accepted")
	}
	if _, ok := SharedCredentialKey("ga4", ""); ok {
		t.Error("an empty id was accepted")
	}
}
