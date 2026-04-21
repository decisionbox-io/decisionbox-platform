package embedding

import (
	"errors"
	"os"
)

// ResolveConfig computes the effective embedding provider/model/credentials
// for a run, combining:
//   - the project-persisted ProjectConfig (set by the UI),
//   - optional EMBEDDING_PROVIDER / EMBEDDING_MODEL / EMBEDDING_PROVIDER_API_KEY
//     environment overrides injected by the platform (DecisionBox Cloud
//     injects these from its managed secrets today; self-hosted can
//     set them via deployment config),
//   - a byokEmbeddingEnabled flag that, when true, tells the resolver to
//     prefer project-supplied credentials over the env override.
//
// In v1 byokEmbeddingEnabled is always false on every cloud plan, so the
// env override always wins when present. Flipping the flag on a future
// paid plan is the only change required to move that deployment to BYOK.
//
// Returns an error only when the effective provider ends up empty — that
// is a misconfigured deployment, caller-visible so we do not silently
// skip embeddings.
type ResolvedConfig struct {
	Provider    string
	Model       string
	APIKey      string
	Source      string // "env" | "project" | "project-byok"
}

var ErrNoProvider = errors.New("embedding: no provider configured (neither project nor env)")

// ResolveConfig chooses the effective embedding provider/model/key.
// Env vars win over project-supplied credentials, unless
// byokEmbeddingEnabled is true, in which case the priority flips and
// the project credentials are used.
func ResolveConfig(project ProjectConfig, byokEmbeddingEnabled bool) (*ResolvedConfig, error) {
	envProvider := os.Getenv("EMBEDDING_PROVIDER")
	envModel := os.Getenv("EMBEDDING_MODEL")
	envKey := os.Getenv("EMBEDDING_PROVIDER_API_KEY")

	// Step 1: env override wins unless BYOK is explicitly enabled.
	if envKey != "" && !byokEmbeddingEnabled {
		provider := project.Provider
		if envProvider != "" {
			provider = envProvider
		}
		model := project.Model
		if envModel != "" {
			model = envModel
		}
		if provider == "" {
			return nil, ErrNoProvider
		}
		return &ResolvedConfig{
			Provider: provider,
			Model:    model,
			APIKey:   envKey,
			Source:   "env",
		}, nil
	}

	// Step 2: BYOK path — use project credentials if present. Distinguish
	// "env override was there but BYOK told us to ignore it" from
	// "no env override at all" so observability can spot when a plan
	// flipped to BYOK but the provisioner still leaked the env var.
	if project.Provider == "" {
		return nil, ErrNoProvider
	}
	source := "project"
	if envKey != "" && byokEmbeddingEnabled {
		source = "project-byok"
	}
	return &ResolvedConfig{
		Provider: project.Provider,
		Model:    project.Model,
		APIKey:   project.Credentials,
		Source:   source,
	}, nil
}
