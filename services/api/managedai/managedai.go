// Package managedai implements an optional "managed inference gateway"
// mode for the API service. When the deployment sets AI_GATEWAY_URL,
// every project's analysis LLM, blurb LLM, and embedding config is
// overridden at write time to point at a single OpenAI-compatible
// gateway using operator-supplied model aliases, and per-project AI
// credential writes are refused. The credential itself rides the
// existing env fallback the agent already honors (LLM_API_KEY /
// EMBEDDING_API_KEY), so nothing is persisted in the project document.
//
// This is a generic capability: the gateway URL, the model aliases, and
// the credential all come from the environment. There is nothing
// DecisionBox-Cloud–specific in this package — a self-hosted operator
// pointing AI_GATEWAY_URL at any OpenAI-compatible gateway gets the same
// behavior. With AI_GATEWAY_URL unset the whole package is inert and
// provider choice is unchanged.
//
// The design leans on a property of every inference call site (community
// and plugin): they resolve the provider from the *stored* project
// document (project.LLM / BlurbLLM / Embedding) and fall back to the
// env credential when no per-project secret exists. Canonicalizing the
// stored document on the write path therefore routes all downstream
// inference through the gateway with no read-path changes.
package managedai

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// Environment variables that drive managed mode. AI_GATEWAY_URL is the
// single activation switch: present ⇒ managed mode ON.
const (
	EnvGatewayURL      = "AI_GATEWAY_URL"
	EnvAnalysisModel   = "AI_GATEWAY_ANALYSIS_MODEL"
	EnvBlurbModel      = "AI_GATEWAY_BLURB_MODEL"
	EnvEmbedModel      = "AI_GATEWAY_EMBED_MODEL"
	EnvEmbedDimensions = "AI_GATEWAY_EMBED_DIMENSIONS"

	// Credential env vars — the same ones the agent's resolveCredential
	// already falls back to. Managed mode requires both to be present so
	// analysis, blurb, and embedding all authenticate against the gateway.
	// These are the NAMES of the environment variables, not secrets.
	envLLMAPIKey       = "LLM_API_KEY"       //nolint:gosec // G101: env var name, not a credential
	envEmbeddingAPIKey = "EMBEDDING_API_KEY" //nolint:gosec // G101: env var name, not a credential

	// managedProvider is the provider all three roles are pinned to in
	// managed mode. The gateway speaks the OpenAI-compatible wire, which
	// the openai LLM and embedding providers already dispatch over.
	managedProvider = "openai"
)

// managedSecretKeys are the per-project secret keys that must NOT be
// writable in managed mode: a stored per-project AI credential would
// shadow the env-fallback gateway key in resolveCredential and let a
// crafted secret write route inference somewhere else.
var managedSecretKeys = map[string]struct{}{
	"llm-credentials":       {},
	"embedding-credentials": {},
	"blurb-llm-credentials": {},
}

// config is the resolved, immutable managed-mode configuration for the
// process. Built once from the environment via load().
type config struct {
	enabled         bool
	baseURL         string // normalized gateway base URL, includes /v1, no trailing slash
	analysisModel   string
	blurbModel      string
	embedModel      string
	embedDimensions string // optional; empty ⇒ let the agent probe (#282)
}

var (
	mu     sync.RWMutex
	loaded *config
)

// Load reads the AI_GATEWAY_* environment and installs the process-wide
// managed-mode configuration. Call once during startup (before serving
// traffic). Safe to call again — it simply re-reads the environment,
// which tests rely on after t.Setenv.
func Load() {
	c := fromEnv()
	mu.Lock()
	loaded = c
	mu.Unlock()
}

// current returns the installed configuration, deriving one from the
// environment on demand if Load has not run yet (keeps unit tests that
// exercise the package functions directly honest without forcing a Load
// call, and makes a missing startup Load fail safe rather than panic).
func current() *config {
	mu.RLock()
	c := loaded
	mu.RUnlock()
	if c != nil {
		return c
	}
	return fromEnv()
}

func fromEnv() *config {
	raw := strings.TrimSpace(os.Getenv(EnvGatewayURL))
	if raw == "" {
		return &config{enabled: false}
	}
	return &config{
		enabled:         true,
		baseURL:         strings.TrimRight(raw, "/"),
		analysisModel:   strings.TrimSpace(os.Getenv(EnvAnalysisModel)),
		blurbModel:      strings.TrimSpace(os.Getenv(EnvBlurbModel)),
		embedModel:      strings.TrimSpace(os.Getenv(EnvEmbedModel)),
		embedDimensions: strings.TrimSpace(os.Getenv(EnvEmbedDimensions)),
	}
}

// Enabled reports whether managed mode is active (AI_GATEWAY_URL set).
func Enabled() bool { return current().enabled }

// Validate returns a non-nil error when managed mode is on but its
// configuration is incomplete or malformed. Call at startup and fail
// fast (the auth/policy plugins use the same log-fatal-on-bad-config
// pattern) — a half-configured gateway would silently mis-route or fail
// every inference call at run time with a far less obvious error.
func Validate() error { return current().validate() }

// Apply overwrites p's analysis LLM, blurb LLM, and embedding config
// with the gateway preset when managed mode is on. It is a no-op when
// managed mode is off or p is nil. Whole objects (and their Config maps)
// are replaced, not field-patched, so a crafted Config["credentials_json"]
// / ["base_url"] / custom key in the request body cannot survive.
func Apply(p *models.Project) { current().apply(p) }

// IsManagedSecretKey reports whether a per-project secret key is one of
// the AI credential slots that must not be written in managed mode.
// Always false when managed mode is off.
func IsManagedSecretKey(key string) bool { return current().isManagedSecretKey(key) }

func (c *config) validate() error {
	if !c.enabled {
		return nil
	}

	u, err := url.Parse(c.baseURL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("managedai: %s must be an absolute URL (got %q)", EnvGatewayURL, c.baseURL)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("managedai: %s must use http or https (got %q)", EnvGatewayURL, c.baseURL)
	}
	// Require the OpenAI-compatible /v1 path: the openai providers append
	// /chat/completions and /embeddings to base_url, so a bare host would
	// POST to the wrong path. Catching it here beats a 404 at run time.
	if !strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/v1") {
		return fmt.Errorf("managedai: %s must include the /v1 path, e.g. https://host/v1 (got %q)", EnvGatewayURL, c.baseURL)
	}

	var missing []string
	if c.analysisModel == "" {
		missing = append(missing, EnvAnalysisModel)
	}
	if c.blurbModel == "" {
		missing = append(missing, EnvBlurbModel)
	}
	if c.embedModel == "" {
		missing = append(missing, EnvEmbedModel)
	}
	// The gateway credential rides the existing agent env fallback; both
	// keys must be present so analysis/blurb and embedding can authenticate.
	if strings.TrimSpace(os.Getenv(envLLMAPIKey)) == "" {
		missing = append(missing, envLLMAPIKey)
	}
	if strings.TrimSpace(os.Getenv(envEmbeddingAPIKey)) == "" {
		missing = append(missing, envEmbeddingAPIKey)
	}
	if len(missing) > 0 {
		return fmt.Errorf("managedai: %s is set but required vars are missing: %s", EnvGatewayURL, strings.Join(missing, ", "))
	}

	if c.embedDimensions != "" {
		if n, err := strconv.Atoi(c.embedDimensions); err != nil || n <= 0 {
			return fmt.Errorf("managedai: %s must be a positive integer when set (got %q)", EnvEmbedDimensions, c.embedDimensions)
		}
	}
	return nil
}

func (c *config) apply(p *models.Project) {
	if !c.enabled || p == nil {
		return
	}

	p.LLM = models.LLMConfig{
		Provider: managedProvider,
		Model:    c.analysisModel,
		Config:   map[string]string{"base_url": c.baseURL},
	}
	p.BlurbLLM = &models.BlurbLLMConfig{
		Provider: managedProvider,
		Model:    c.blurbModel,
		Config:   map[string]string{"base_url": c.baseURL},
	}

	embCfg := map[string]string{"base_url": c.baseURL}
	// Optional fast-path: pin the vector size so the agent skips the live
	// probe. When unset, the agent auto-detects from a live embedding
	// (#282), which is the recommended default.
	if c.embedDimensions != "" {
		embCfg["dimensions"] = c.embedDimensions
	}
	p.Embedding = goembedding.ProjectConfig{
		Provider: managedProvider,
		Model:    c.embedModel,
		Config:   embCfg,
	}
}

func (c *config) isManagedSecretKey(key string) bool {
	if !c.enabled {
		return false
	}
	_, ok := managedSecretKeys[key]
	return ok
}
