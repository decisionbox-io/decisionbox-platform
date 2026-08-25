package embedding

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"time"
)

// Config keys for per-project custom TLS on an embedding endpoint. These
// mirror the LLM side (llm.TLSCACertKey / llm.TLSSkipVerifyKey) — an
// on-prem LiteLLM proxy fronts both chat and embeddings behind the same
// private CA, so the embedding provider must trust it the same way.
// Stored in project.Embedding.Config (plaintext — a CA certificate is
// public material, not a secret).
const (
	// TLSCACertKey holds a PEM-encoded CA certificate appended to the
	// system trust store when dialing the embedding endpoint.
	TLSCACertKey = "tls_ca_cert"

	// TLSSkipVerifyKey, when "true", disables TLS certificate
	// verification (InsecureSkipVerify). Insecure escape hatch; prefer
	// uploading a CA via TLSCACertKey.
	TLSSkipVerifyKey = "tls_skip_verify"
)

// TLSConfigFields returns the dashboard config fields for per-project
// custom TLS (CA upload + skip-verify) on an embedding provider. Kept
// identical to llm.TLSConfigFields so the two forms render the same
// controls. Providers that can point at a self-hosted / private-CA HTTPS
// endpoint append these to their ProviderMeta.ConfigFields.
func TLSConfigFields() []ConfigField {
	return []ConfigField{
		{
			Key:         TLSCACertKey,
			Label:       "Custom CA certificate (PEM)",
			Type:        "file",
			Placeholder: "-----BEGIN CERTIFICATE-----",
			Description: "Paste or upload the PEM CA certificate for an HTTPS endpoint fronted by a private / internal CA. Appended to the system trust store. Leave blank for endpoints with a publicly-trusted certificate.",
		},
		{
			Key:         TLSSkipVerifyKey,
			Label:       "Disable TLS verification",
			Type:        "boolean",
			Default:     "false",
			Description: "INSECURE — skips TLS certificate verification entirely. Prefer uploading a CA certificate above; use only as a temporary escape hatch on a trusted network.",
		},
	}
}

// HasCustomTLS reports whether cfg requests any non-default TLS behaviour
// (a CA cert to append or verification disabled). Providers whose
// transport is SDK-managed (Bedrock) consult this before overriding it.
func HasCustomTLS(cfg ProviderConfig) bool {
	if cfg == nil {
		return false
	}
	return cfg[TLSCACertKey] != "" || cfg[TLSSkipVerifyKey] == "true"
}

// HTTPClientFor returns the *http.Client an embedding provider should use,
// honouring the per-project custom-TLS keys:
//
//   - neither key set → &http.Client{Timeout: timeout} (unchanged default).
//   - TLSCACertKey set → RootCAs = system pool + the appended CA.
//   - TLSSkipVerifyKey == "true" → InsecureSkipVerify: true.
//
// A non-empty but unparseable TLSCACertKey returns an error so the
// misconfiguration surfaces at provider construction (Test connection /
// Load models) rather than as an opaque handshake failure later.
func HTTPClientFor(cfg ProviderConfig, timeout time.Duration) (*http.Client, error) {
	if !HasCustomTLS(cfg) {
		return &http.Client{Timeout: timeout}, nil
	}

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}

	if pem := cfg[TLSCACertKey]; pem != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(pem)) {
			return nil, fmt.Errorf("%s is not a valid PEM certificate", TLSCACertKey)
		}
		tlsCfg.RootCAs = pool
	}

	if cfg[TLSSkipVerifyKey] == "true" {
		tlsCfg.InsecureSkipVerify = true //nolint:gosec // G402: intentional per-project opt-in, surfaced with an insecure warning in the UI
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsCfg

	return &http.Client{Timeout: timeout, Transport: transport}, nil
}
