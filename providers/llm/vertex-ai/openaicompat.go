package vertexai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/libs/go-common/llm/openaicompat"
)

// chatOpenAICompat sends a request to a Vertex AI model that speaks the
// OpenAI /chat/completions wire. Two endpoint shapes are supported,
// selected by whether endpoint_id is configured:
//
//   - Shared Model Garden MaaS (endpoint_id blank): Llama MaaS, Qwen
//     MaaS, DeepSeek MaaS, Mistral MaaS, and Gemini's OpenAI-compatible
//     surface, all served under .../endpoints/openapi/chat/completions.
//     The model ID is namespaced with the publisher
//     (e.g. "meta/llama-3.3-70b-instruct-maas"); Vertex strips the slash
//     internally. We pass it through verbatim because every documented
//     example from Google uses this form.
//   - User-deployed endpoint (endpoint_id set): a model the operator
//     deployed themselves (self-fine-tuned, quantised, …), served under
//     .../endpoints/{endpoint_id}/chat/completions with no /openapi/
//     segment. The deployed model is named by its serving container, so
//     the request body's model is passed through verbatim with no
//     publisher prefix. The host is resolved per endpoint — a
//     "dedicated" endpoint (the default for Model Garden one-click
//     deploys and most custom deployments) is served on its own DNS, not
//     the shared aiplatform host; see resolveEndpointHost.
//
// Both shapes authenticate with the same GCP bearer token as Gemini.
func (p *VertexAIProvider) chatOpenAICompat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	body := openaicompat.BuildRequestBody(req.Model, req)

	reqBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("vertex-ai/openai-compat: failed to marshal request: %w", err)
	}

	endpoint, err := p.openAICompatURL(ctx)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("vertex-ai/openai-compat: failed to create request: %w", err)
	}

	token, err := p.auth.token(ctx)
	if err != nil {
		return nil, err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	// A user-account ADC token needs a quota project when the request hits
	// aiplatform.googleapis.com (the MaaS path and non-dedicated
	// endpoints); without it a gcloud-login user with no configured quota
	// project gets a 403. Skipped for service-account keys, which bill to
	// their own project and can hit a serviceusage 403 if the header
	// forces a quota project they lack permission on. The dedicated
	// endpoint DNS ignores the header either way.
	if p.useQuotaProjectHeader {
		httpReq.Header.Set("X-Goog-User-Project", p.projectID)
	}

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("vertex-ai/openai-compat: request failed: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("vertex-ai/openai-compat: failed to read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		if apiErr := openaicompat.ExtractAPIError(respBody); apiErr != nil {
			return nil, fmt.Errorf("vertex-ai/openai-compat: API error (%d): %s - %s", httpResp.StatusCode, apiErr.Type, apiErr.Message)
		}
		// Collapse whitespace so Vertex's multi-line HTML error pages stay
		// readable in one-line log output.
		snippet := strings.TrimSpace(string(respBody))
		if len(snippet) > 500 {
			snippet = snippet[:500] + "..."
		}
		return nil, fmt.Errorf("vertex-ai/openai-compat: API error (%d): %s", httpResp.StatusCode, snippet)
	}

	resp, err := openaicompat.ParseResponseBody(respBody)
	if err != nil {
		return nil, fmt.Errorf("vertex-ai/openai-compat: %w", err)
	}
	return resp, nil
}

// openAICompatURL builds the chat-completions URL for the configured
// path.
//
//   - endpoint_id blank → shared Model Garden MaaS path
//     .../endpoints/openapi/chat/completions on the regional aiplatform
//     host (no network call).
//   - endpoint_id set → .../endpoints/{endpoint_id}/chat/completions on
//     the host returned by resolveEndpointHost (the endpoint's dedicated
//     DNS when it has one, else the regional aiplatform host).
func (p *VertexAIProvider) openAICompatURL(ctx context.Context) (string, error) {
	if p.endpointID == "" {
		return p.maasChatURL(), nil
	}
	host, err := p.resolveEndpointHost(ctx)
	if err != nil {
		return "", err
	}
	return p.endpointChatURL(host), nil
}

// aiplatformHost is the region-scoped Vertex AI host: regional for every
// location except "global", which uses the bare host.
func (p *VertexAIProvider) aiplatformHost() string {
	if p.location == "global" {
		return "aiplatform.googleapis.com"
	}
	return fmt.Sprintf("%s-aiplatform.googleapis.com", p.location)
}

// maasChatURL is the shared Model Garden MaaS chat-completions URL.
func (p *VertexAIProvider) maasChatURL() string {
	return fmt.Sprintf(
		"https://%s/v1beta1/projects/%s/locations/%s/endpoints/openapi/chat/completions",
		p.aiplatformHost(), p.projectID, p.location,
	)
}

// endpointChatURL is the user-deployed endpoint chat-completions URL on
// the given host (the dedicated DNS or the shared aiplatform host).
func (p *VertexAIProvider) endpointChatURL(host string) string {
	return fmt.Sprintf(
		"https://%s/v1beta1/projects/%s/locations/%s/endpoints/%s/chat/completions",
		host, p.projectID, p.location, p.endpointID,
	)
}

// resolveEndpointHost returns the host to send predictions to for the
// configured endpoint_id, caching the result for the provider's life.
//
// Vertex serves a "dedicated" endpoint on its own DNS and rejects
// predictions sent to the shared aiplatform host with HTTP 400
// FAILED_PRECONDITION. Model Garden one-click deploys and most custom
// deployments are dedicated, and the dedicated DNS embeds an internal
// tenant project number that cannot be derived from the caller's
// project — so the only reliable source is the endpoint resource's
// dedicatedEndpointDns field, which we fetch via the management API
// (that call works on the shared host even for dedicated endpoints).
//
// A non-dedicated endpoint has no dedicated DNS and is served on the
// shared aiplatform host. A dedicated endpoint that reports no DNS yet
// is still provisioning (or has no model deployed); we surface that as
// an actionable error rather than falling back to a host that will
// return the opaque FAILED_PRECONDITION.
func (p *VertexAIProvider) resolveEndpointHost(ctx context.Context) (string, error) {
	p.endpointHostMu.Lock()
	defer p.endpointHostMu.Unlock()
	if p.endpointHost != "" {
		return p.endpointHost, nil
	}

	dns, dedicated, err := p.fetchEndpointDNS(ctx)
	if err != nil {
		return "", err
	}
	switch {
	case dns != "":
		p.endpointHost = dns
	case dedicated:
		return "", fmt.Errorf(
			"vertex-ai: endpoint %q is a dedicated endpoint but reports no DNS yet "+
				"— ensure a model is deployed and the endpoint has finished provisioning",
			p.endpointID,
		)
	default:
		p.endpointHost = p.aiplatformHost()
	}
	return p.endpointHost, nil
}

// fetchEndpointDNS fetches the endpoint resource via the management API
// and returns its dedicated DNS (empty when not dedicated) and whether
// the endpoint is flagged dedicated.
func (p *VertexAIProvider) fetchEndpointDNS(ctx context.Context) (dns string, dedicated bool, err error) {
	url := fmt.Sprintf(
		"https://%s/v1beta1/projects/%s/locations/%s/endpoints/%s",
		p.aiplatformHost(), p.projectID, p.location, p.endpointID,
	)
	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", false, fmt.Errorf("vertex-ai: failed to create endpoint lookup request: %w", err)
	}

	token, err := p.auth.token(ctx)
	if err != nil {
		return "", false, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	// Quota-project header for user-account ADC only — see chatOpenAICompat.
	if p.useQuotaProjectHeader {
		httpReq.Header.Set("X-Goog-User-Project", p.projectID)
	}

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return "", false, fmt.Errorf("vertex-ai: endpoint lookup failed: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return "", false, fmt.Errorf("vertex-ai: failed to read endpoint lookup response: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		snippet := strings.TrimSpace(string(respBody))
		if len(snippet) > 500 {
			snippet = snippet[:500] + "..."
		}
		return "", false, fmt.Errorf(
			"vertex-ai: endpoint lookup for %q failed (%d): %s",
			p.endpointID, httpResp.StatusCode, snippet,
		)
	}

	var decoded struct {
		DedicatedEndpointEnabled bool   `json:"dedicatedEndpointEnabled"`
		DedicatedEndpointDNS     string `json:"dedicatedEndpointDns"`
	}
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return "", false, fmt.Errorf("vertex-ai: failed to parse endpoint lookup response: %w", err)
	}
	return normalizeEndpointHost(decoded.DedicatedEndpointDNS), decoded.DedicatedEndpointEnabled, nil
}

// normalizeEndpointHost reduces a dedicatedEndpointDns value to a bare
// host. The aiplatform API documents the field in URL form
// (https://{endpoint_id}.{region}-{uid}.prediction.vertexai.goog) while
// the live v1beta1 surface returns a bare host; accept either by
// stripping any scheme and trailing slash so endpointChatURL — which
// prefixes "https://" itself — never builds "https://https://…". An
// empty value stays empty so the "dedicated but no DNS" case is
// preserved.
func normalizeEndpointHost(dns string) string {
	h := strings.TrimSpace(dns)
	h = strings.TrimPrefix(h, "https://")
	h = strings.TrimPrefix(h, "http://")
	h = strings.TrimSuffix(h, "/")
	return h
}
