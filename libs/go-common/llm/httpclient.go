package llm

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"time"
)

// Config keys for per-project custom TLS on an LLM endpoint. Stored in
// project.LLM.Config (plaintext — a CA certificate is public material,
// not a secret) so they reach the API, ask-serve, and the spawned
// discovery agent through the same config the provider factory reads.
const (
	// TLSCACertKey holds a PEM-encoded CA certificate that is appended to
	// the system trust store when dialing the endpoint. Use for HTTPS
	// endpoints fronted by a private / internal CA.
	TLSCACertKey = "tls_ca_cert"

	// TLSSkipVerifyKey, when "true", disables TLS certificate
	// verification entirely (InsecureSkipVerify). Insecure escape hatch;
	// prefer uploading a CA via TLSCACertKey.
	TLSSkipVerifyKey = "tls_skip_verify"
)

// TLSConfigFields returns the dashboard config fields for per-project
// custom TLS (CA upload + skip-verify). Providers that can point at a
// self-hosted or private-CA HTTPS endpoint append these to their
// ProviderMeta.ConfigFields; the dashboard renders them generically.
// Kept here so the field keys, labels, and types stay in one place.
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

// HasCustomTLS reports whether cfg requests any non-default TLS
// behaviour (a CA cert to append or verification disabled). Providers
// whose transport is otherwise managed by an SDK (e.g. Bedrock's AWS
// SDK) consult this to decide whether to override that transport at all.
func HasCustomTLS(cfg ProviderConfig) bool {
	if cfg == nil {
		return false
	}
	return cfg[TLSCACertKey] != "" || cfg[TLSSkipVerifyKey] == "true"
}

// HTTPClientFor returns the *http.Client an LLM provider should use for
// outbound model calls, honouring the per-project custom-TLS keys:
//
//   - neither key set → &http.Client{Timeout: timeout}, using Go's
//     default transport and system trust store (unchanged default).
//   - TLSCACertKey set → RootCAs = system pool + the appended CA, so a
//     private-CA HTTPS endpoint is trusted without an image rebuild.
//   - TLSSkipVerifyKey == "true" → InsecureSkipVerify: true.
//
// When TLSCACertKey is non-empty but contains no parseable certificate,
// HTTPClientFor returns an error so the misconfiguration surfaces at
// provider construction time (Load models / Test connection) rather
// than as an opaque handshake failure later.
func HTTPClientFor(cfg ProviderConfig, timeout time.Duration) (*http.Client, error) {
	if !HasCustomTLS(cfg) {
		// Preserve the exact historical default — no custom transport.
		return &http.Client{Timeout: timeout}, nil
	}

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}

	if pem := cfg[TLSCACertKey]; pem != "" {
		// Start from the system pool so system-trusted CAs keep working
		// alongside the uploaded one. SystemCertPool can fail on some
		// platforms (or return nil); fall back to an empty pool so the
		// uploaded CA is still honoured.
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
		// Operator-opted escape hatch, warned in the dashboard. Prefer a
		// CA upload; this disables all certificate verification.
		tlsCfg.InsecureSkipVerify = true //nolint:gosec // G402: intentional per-project opt-in, surfaced with an insecure warning in the UI
	}

	// Clone the default transport so timeouts/keep-alives/proxy settings
	// match the standard client; only the TLS config differs.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsCfg

	return &http.Client{Timeout: timeout, Transport: transport}, nil
}
