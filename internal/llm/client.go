package llm

import (
	"context"
	"errors"
)

// Client defines the interface for LLM clients
type Client interface {
	Complete(ctx context.Context, conversation []string) (string, error)
}

// MockClient is a simple mock LLM client for testing
type MockClient struct{}

// Complete implements the Client interface
func (m *MockClient) Complete(ctx context.Context, conversation []string) (string, error) {
	select {
	case <-ctx.Done():
		return "", errors.New("request cancelled")
	default:
		// In a real implementation, you would call your LLM API here
		return "This is a mock response from the LLM. In a real implementation, you would integrate with an actual LLM API.", nil
	}
}

// NewMockClient creates a new mock LLM client
func NewMockClient() Client {
	return &MockClient{}
}

// You would implement actual LLM clients here, e.g.:
// - OpenAIClient for ChatGPT
// - AnthropicClient for Claude
// - LocalClient for a locally running model
