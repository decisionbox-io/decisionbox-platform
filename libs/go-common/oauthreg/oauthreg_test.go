package oauthreg

import "testing"

func TestKey_IsReadableAndSecretStoreSafe(t *testing.T) {
	for _, tc := range []struct{ provider, field, want string }{
		{"google", FieldClientID, "oauth-app-google-client-id"},
		{"google", FieldClientSecret, "oauth-app-google-client-secret"},
		{"google", FieldRedirectURI, "oauth-app-google-redirect-uri"},
	} {
		if got := Key(tc.provider, tc.field); got != tc.want {
			t.Errorf("Key(%q, %q) = %q, want %q", tc.provider, tc.field, got, tc.want)
		}
	}
}

// Azure Key Vault is the strictest backend in play and accepts only
// [A-Za-z0-9-]. A key it rejects fails to store at all, so this is the
// constraint the substitution exists for.
func TestKey_StaysInTheStrictestAlphabet(t *testing.T) {
	for _, provider := range []string{"google", "sales_force", "Weird.Provider", "a b"} {
		for _, field := range Fields {
			key := Key(provider, field)
			for _, r := range key {
				if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
					continue
				}
				t.Fatalf("Key(%q, %q) = %q contains %q, which Azure Key Vault rejects", provider, field, key, r)
			}
		}
	}
}

// Two providers must not share a key, or one deployment's registration would
// silently overwrite the other's.
func TestKey_DistinctProvidersDoNotCollide(t *testing.T) {
	seen := map[string]string{}
	for _, provider := range []string{"google", "salesforce", "hubspot"} {
		key := Key(provider, FieldClientSecret)
		if other, dup := seen[key]; dup {
			t.Fatalf("providers %q and %q share the key %q", other, provider, key)
		}
		seen[key] = provider
	}
}

// The config namespace is what lets applyOAuthAppRegistration clear everything
// it owns by prefix without touching a provider's own fields.
func TestConfigKey_IsPrefixed(t *testing.T) {
	if got := ConfigKey(FieldClientID); got != "oauth_app_client_id" {
		t.Fatalf("ConfigKey = %q, want oauth_app_client_id", got)
	}
	prefix := ConfigKey("")
	for _, f := range Fields {
		if len(ConfigKey(f)) <= len(prefix) || ConfigKey(f)[:len(prefix)] != prefix {
			t.Fatalf("ConfigKey(%q) is not under the %q namespace", f, prefix)
		}
	}
}

func TestFields_IsTheWholeRegistration(t *testing.T) {
	want := map[string]bool{FieldClientID: true, FieldClientSecret: true, FieldRedirectURI: true}
	if len(Fields) != len(want) {
		t.Fatalf("Fields = %v, want exactly the three registration fields", Fields)
	}
	for _, f := range Fields {
		if !want[f] {
			t.Errorf("unexpected field %q", f)
		}
	}
}
