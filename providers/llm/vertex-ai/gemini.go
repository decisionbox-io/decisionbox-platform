package vertexai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// geminiChat sends a request to Gemini on Vertex AI.
// Uses the Vertex AI generateContent REST API.
func (p *VertexAIProvider) geminiChat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	// Build Gemini request
	contents := make([]geminiContent, 0, len(req.Messages)+1)

	// System instruction is handled separately in Gemini
	if req.SystemPrompt != "" {
		contents = append(contents, geminiContent{
			Role:  "user",
			Parts: []geminiPart{{Text: req.SystemPrompt}},
		})
		contents = append(contents, geminiContent{
			Role:  "model",
			Parts: []geminiPart{{Text: "Understood. I will follow these instructions."}},
		})
	}

	for _, msg := range req.Messages {
		role := msg.Role
		if role == "assistant" {
			role = "model" // Gemini uses "model" instead of "assistant"
		}
		contents = append(contents, geminiContent{
			Role:  role,
			Parts: []geminiPart{{Text: msg.Content}},
		})
	}

	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}

	body := geminiRequest{
		Contents: contents,
		GenerationConfig: geminiGenerationConfig{
			MaxOutputTokens: maxTokens,
		},
	}

	if req.Temperature > 0 {
		temp := req.Temperature
		body.GenerationConfig.Temperature = &temp
	}

	reqBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("vertex-ai/gemini: failed to marshal request: %w", err)
	}

	// Vertex AI endpoint for Gemini
	// Global endpoint has no region prefix: https://aiplatform.googleapis.com/...
	// Regional endpoints use: https://{location}-aiplatform.googleapis.com/...
	var host string
	if p.location == "global" {
		host = "aiplatform.googleapis.com"
	} else {
		host = fmt.Sprintf("%s-aiplatform.googleapis.com", p.location)
	}
	endpoint := fmt.Sprintf(
		"https://%s/v1/projects/%s/locations/%s/publishers/google/models/%s:generateContent",
		host, p.projectID, p.location, req.Model,
	)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("vertex-ai/gemini: failed to create request: %w", err)
	}

	token, err := p.auth.token(ctx)
	if err != nil {
		return nil, err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("vertex-ai/gemini: request failed: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("vertex-ai/gemini: failed to read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("vertex-ai/gemini: API error (status %d): %s", httpResp.StatusCode, string(respBody))
	}

	var geminiResp geminiResponse
	if err := json.Unmarshal(respBody, &geminiResp); err != nil {
		return nil, fmt.Errorf("vertex-ai/gemini: failed to parse response: %w", err)
	}

	// Extract text from response
	content := ""
	if len(geminiResp.Candidates) > 0 && len(geminiResp.Candidates[0].Content.Parts) > 0 {
		content = geminiResp.Candidates[0].Content.Parts[0].Text
	}

	stopReason := ""
	if len(geminiResp.Candidates) > 0 {
		stopReason = geminiResp.Candidates[0].FinishReason
	}

	return &gollm.ChatResponse{
		Content:    content,
		Model:      req.Model,
		StopReason: stopReason,
		Usage: gollm.Usage{
			InputTokens:  geminiResp.UsageMetadata.PromptTokenCount,
			OutputTokens: geminiResp.UsageMetadata.CandidatesTokenCount,
		},
	}, nil
}

// Gemini API types

type geminiRequest struct {
	Contents         []geminiContent         `json:"contents"`
	GenerationConfig geminiGenerationConfig  `json:"generationConfig"`
}

type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiGenerationConfig struct {
	MaxOutputTokens int      `json:"maxOutputTokens"`
	Temperature     *float64 `json:"temperature,omitempty"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}
