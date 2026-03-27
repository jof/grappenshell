package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// Message represents a chat message in the OpenAI API format
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest represents an OpenAI-compatible chat completion request
type ChatRequest struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	MaxTokens int       `json:"max_tokens,omitempty"`
}

// ChatResponse represents an OpenAI-compatible chat completion response
type ChatResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
}

// OpenAIClient is an LLM client that speaks the OpenAI chat completions API
type OpenAIClient struct {
	httpClient *http.Client
	baseURL    string
	model      string
}

// NewOpenAIClient creates a new OpenAI-compatible client.
// httpClient should be a Tailscale-aware HTTP client if the endpoint is on a tailnet.
func NewOpenAIClient(httpClient *http.Client, baseURL string, model string) Client {
	return &OpenAIClient{
		httpClient: httpClient,
		baseURL:    baseURL,
		model:      model,
	}
}

// Complete implements the Client interface
func (c *OpenAIClient) Complete(ctx context.Context, conversation []string) (string, error) {
	// Convert the conversation string slice into OpenAI messages.
	// First entry is the system prompt; subsequent entries alternate "User:" / "Assistant:".
	messages := make([]Message, 0, len(conversation))
	if len(conversation) > 0 {
		messages = append(messages, Message{Role: "system", Content: conversation[0]})
	}
	for _, line := range conversation[1:] {
		switch {
		case len(line) > 6 && line[:6] == "User: ":
			messages = append(messages, Message{Role: "user", Content: line[6:]})
		case len(line) > 11 && line[:11] == "Assistant: ":
			messages = append(messages, Message{Role: "assistant", Content: line[11:]})
		default:
			messages = append(messages, Message{Role: "user", Content: line})
		}
	}

	reqBody := ChatRequest{
		Model:     c.model,
		Messages:  messages,
		MaxTokens: 2048,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	url := c.baseURL + "/chat/completions"

	// Use a timeout so we don't hang forever
	reqCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	log.Printf("LLM request: model=%s messages=%d", c.model, len(messages))
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("LLM API request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("LLM API returned %d: %s", resp.StatusCode, string(respBytes))
	}

	var chatResp ChatResponse
	if err := json.Unmarshal(respBytes, &chatResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("LLM API returned no choices")
	}

	return chatResp.Choices[0].Message.Content, nil
}
