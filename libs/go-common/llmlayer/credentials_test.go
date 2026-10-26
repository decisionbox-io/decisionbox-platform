package llmlayer

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestCredentialsKeyKeepsTheReservedEntriesOnTheirLegacyKeys(t *testing.T) {
	for _, id := range []string{"", DefaultProviderID} {
		if got := CredentialsKey(id); got != LegacyCredentialsKey {
			t.Errorf("CredentialsKey(%q) = %q, want %q — moving the default entry's key would orphan every existing project's credential", id, got, LegacyCredentialsKey)
		}
	}
	if got := CredentialsKey(BlurbProviderID); got != BlurbCredentialsKey {
		t.Errorf("CredentialsKey(blurb) = %q, want %q", got, BlurbCredentialsKey)
	}
}

func TestCredentialsKeyHexEncodesSoCloudBackendsAcceptIt(t *testing.T) {
	// GCP allows [A-Za-z0-9_-] and Azure Key Vault only [A-Za-z0-9-], so a raw
	// id with an underscore or a non-ASCII character would be rejected by the
	// backend when the secret is written.
	cases := []string{"fast_small", "a:b", "düsseldorf", "Mixed-Case", "x"}
	for _, id := range cases {
		got := CredentialsKey(id)
		suffix, ok := strings.CutPrefix(got, LegacyCredentialsKey+"-")
		if !ok {
			t.Fatalf("CredentialsKey(%q) = %q, want the namespaced prefix", id, got)
		}
		if decoded, err := hex.DecodeString(suffix); err != nil || string(decoded) != id {
			t.Errorf("CredentialsKey(%q) suffix %q does not hex-decode back to the id (err=%v)", id, suffix, err)
		}
		for _, r := range got {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			default:
				t.Errorf("CredentialsKey(%q) = %q contains %q, which Azure Key Vault rejects", id, got, r)
			}
		}
	}
}

func TestCredentialsKeyIsCollisionFreeAcrossDistinctIds(t *testing.T) {
	seen := map[string]string{}
	for _, id := range []string{"a", "b", "ab", "a-b", "a_b", "A", DefaultProviderID + "x"} {
		key := CredentialsKey(id)
		if prev, dup := seen[key]; dup {
			t.Fatalf("ids %q and %q both map to %q", prev, id, key)
		}
		seen[key] = id
	}
}

func TestComposedAzureNameFitsAtTheIdLengthLimit(t *testing.T) {
	// Azure Key Vault validates the composed "<namespace>-<projectID>-<key>"
	// at 127 characters. MaxProviderIDLen exists to keep the worst case
	// inside it; this pins the arithmetic so raising the limit fails here
	// rather than in a cloud deployment.
	const namespace = "decisionbox"
	projectID := strings.Repeat("a", 24) // a Mongo ObjectID hex string
	id := strings.Repeat("z", MaxProviderIDLen)
	composed := namespace + "-" + projectID + "-" + CredentialsKey(id)
	if len(composed) > 127 {
		t.Fatalf("composed Azure secret name is %d characters (%q), over the 127 limit", len(composed), composed)
	}
}

func TestValidateProviderID(t *testing.T) {
	cases := []struct {
		id      string
		wantErr bool
	}{
		{"", false},
		{DefaultProviderID, false},
		{"fast_small", false},
		{"bedrock-opus", false},
		{"A1", false},
		{strings.Repeat("z", MaxProviderIDLen), false},
		{strings.Repeat("z", MaxProviderIDLen+1), true},
		{"a:b", true},
		{"a b", true},
		{"düsseldorf", true},
		{"a/b", true},
	}
	for _, c := range cases {
		err := ValidateProviderID(c.id)
		if (err != nil) != c.wantErr {
			t.Errorf("ValidateProviderID(%q) error = %v, wantErr %t", c.id, err, c.wantErr)
		}
	}
}

func TestNormalizeAndReserved(t *testing.T) {
	if NormalizeProviderID("") != DefaultProviderID {
		t.Error(`NormalizeProviderID("") must be the default entry, so a stored id-less entry and an explicit "default" are one entry`)
	}
	if !IsReservedProviderID("") || !IsReservedProviderID(DefaultProviderID) || !IsReservedProviderID(BlurbProviderID) {
		t.Error("the two reserved ids (and the empty id) must report as reserved")
	}
	if IsReservedProviderID("deepinfra") {
		t.Error("a user-named entry must not report as reserved — it decides whether the env fallback applies")
	}
}

func TestCredentialChainPerEntry(t *testing.T) {
	type step struct{ key, env string }
	flatten := func(refs []CredentialRef) []step {
		out := make([]step, 0, len(refs))
		for _, r := range refs {
			out = append(out, step{r.SecretKey, r.EnvVar})
		}
		return out
	}
	eq := func(a, b []step) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}

	legacy := []step{{LegacyCredentialsKey, LegacyCredentialsEnv}}
	blurbChain := []step{{BlurbCredentialsKey, BlurbCredentialsEnv}, {LegacyCredentialsKey, LegacyCredentialsEnv}}

	cases := []struct {
		name       string
		layer      string
		providerID string
		cfg        map[string]string
		want       []step
	}{
		{"default entry, ordinary layer", LayerAnalysis, DefaultProviderID, nil, legacy},
		{"empty id normalises to default", LayerAnalysis, "", nil, legacy},
		{
			// resolveBlurbLLM tries the blurb secret first UNCONDITIONALLY —
			// including when the project has no blurb_llm override and whether
			// or not the provider slugs match. A project with no override but a
			// stored blurb-llm-credentials secret is using that key today.
			"table descriptions on the default entry keeps the four-step chain",
			LayerTableDescriptions, DefaultProviderID, nil, blurbChain,
		},
		{"the blurb entry keeps the four-step chain for any layer", LayerAnalysis, BlurbProviderID, nil, blurbChain},
		{
			"a non-reserved entry gets no shared env fallback",
			LayerExploration, "deepinfra", nil,
			[]step{{CredentialsKey("deepinfra"), ""}},
		},
		{
			"a non-reserved entry may name its own env var explicitly",
			LayerExploration, "deepinfra", map[string]string{EnvVarConfigKey: " DBX_LLM_DEEPINFRA_KEY "},
			[]step{{CredentialsKey("deepinfra"), "DBX_LLM_DEEPINFRA_KEY"}},
		},
		{
			// Whoever can edit the project picks this name, so an unprefixed
			// one must resolve to NO fallback rather than to the variable it
			// names: with an attacker-chosen base_url on the same entry, the
			// value would be sent to that endpoint as a bearer token.
			"an env var the deployment did not sanction yields no fallback",
			LayerExploration, "exfil", map[string]string{EnvVarConfigKey: "MONGODB_URI"},
			[]step{{CredentialsKey("exfil"), ""}},
		},
		{
			"the bare prefix is not a variable name",
			LayerExploration, "exfil", map[string]string{EnvVarConfigKey: EnvVarPrefix},
			[]step{{CredentialsKey("exfil"), ""}},
		},
		{
			"table descriptions on a non-reserved entry does NOT borrow the blurb chain",
			LayerTableDescriptions, "cheap", nil,
			[]step{{CredentialsKey("cheap"), ""}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := flatten(CredentialChain(c.layer, c.providerID, c.cfg))
			if !eq(got, c.want) {
				t.Errorf("CredentialChain(%q, %q) = %v, want %v", c.layer, c.providerID, got, c.want)
			}
		})
	}
}

func TestNonReservedChainNeverNamesTheDefaultsKeyOrEnv(t *testing.T) {
	// The whole point of D7-3: an OpenAI-compatible secondary entry sends
	// whatever credential it is handed as a bearer token to its own base_url,
	// so it must never be able to resolve the default entry's key — not even
	// through a transient secret-store error, which ResolveCredential treats
	// as "fall through to env".
	for _, ref := range CredentialChain(LayerExploration, "thirdparty", map[string]string{EnvVarConfigKey: "MY_KEY"}) {
		if ref.SecretKey == LegacyCredentialsKey || ref.SecretKey == BlurbCredentialsKey {
			t.Errorf("non-reserved entry may read %q", ref.SecretKey)
		}
		if ref.EnvVar == LegacyCredentialsEnv || ref.EnvVar == BlurbCredentialsEnv {
			t.Errorf("non-reserved entry may fall back to %q", ref.EnvVar)
		}
	}
}

func TestNoProjectDocumentCanNameTheDeploymentsOwnSecrets(t *testing.T) {
	// The names a real pod carries. None of them may be reachable by writing
	// a provider entry, whatever the entry is called.
	for _, name := range []string{
		"MONGODB_URI", "SECRET_ENCRYPTION_KEY", "AWS_SECRET_ACCESS_KEY",
		"AWS_SESSION_TOKEN", "GOOGLE_APPLICATION_CREDENTIALS", "DBX_LICENSE_KEY",
		LegacyCredentialsEnv, BlurbCredentialsEnv,
		// Near-misses: a lowercase prefix, and the prefix somewhere other
		// than the start.
		"dbx_llm_KEY", "MY_DBX_LLM_KEY",
	} {
		if AllowedEnvVar(name) {
			t.Errorf("AllowedEnvVar(%q) = true; a project document must not be able to name it", name)
		}
		chain := CredentialChain(LayerAnalysis, "thirdparty", map[string]string{EnvVarConfigKey: name})
		for _, ref := range chain {
			if ref.EnvVar != "" {
				t.Errorf("CredentialChain honoured env var %q", ref.EnvVar)
			}
		}
	}
	for _, name := range []string{EnvVarPrefix + "ACME_KEY", EnvVarPrefix + "X"} {
		if !AllowedEnvVar(name) {
			t.Errorf("AllowedEnvVar(%q) = false; a prefixed name is what an operator exports for this", name)
		}
	}
}
