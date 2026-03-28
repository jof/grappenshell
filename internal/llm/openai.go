package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
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
	Stream    bool      `json:"stream,omitempty"`
}

// StreamChunk represents a single SSE chunk from the streaming API
type StreamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
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
func NewOpenAIClient(httpClient *http.Client, baseURL string, model string) *OpenAIClient {
	return &OpenAIClient{
		httpClient: httpClient,
		baseURL:    baseURL,
		model:      model,
	}
}

// Complete implements the Client interface
func (c *OpenAIClient) Complete(ctx context.Context, conversation []string) (string, error) {
	messages := c.buildMessages(conversation)

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

// CompleteStream sends a streaming chat completion request and calls onToken
// with each content delta as it arrives. Returns the full concatenated response.
func (c *OpenAIClient) CompleteStream(ctx context.Context, conversation []string, onToken func(string)) (string, error) {
	messages := c.buildMessages(conversation)

	reqBody := ChatRequest{
		Model:     c.model,
		Messages:  messages,
		MaxTokens: 2048,
		Stream:    true,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	url := c.baseURL + "/chat/completions"

	reqCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	log.Printf("LLM stream request: model=%s messages=%d", c.model, len(messages))
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("LLM API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("LLM API returned %d: %s", resp.StatusCode, string(respBytes))
	}

	var full strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk StreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			token := chunk.Choices[0].Delta.Content
			full.WriteString(token)
			onToken(token)
		}
	}

	if err := scanner.Err(); err != nil {
		return full.String(), fmt.Errorf("stream read error: %w", err)
	}

	return full.String(), nil
}

// buildMessages converts the conversation string slice into OpenAI messages.
func (c *OpenAIClient) buildMessages(conversation []string) []Message {
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
	return messages
}
