package shell

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/jof/grappenshell/internal/llm"
	"golang.org/x/crypto/ssh/terminal"
)

// Config holds configuration for the shell session
type Config struct {
	Prompt       string
	SystemPrompt string
	LLMClient    llm.Client
}

// Session represents a shell-like session
type Session struct {
	channel      io.ReadWriter
	config       *Config
	term         *terminal.Terminal
	ctx          context.Context
	cancel       context.CancelFunc
	conversation []string
	mu           sync.Mutex
}

// NewSession creates a new shell session
func NewSession(channel io.ReadWriter, config *Config) *Session {
	ctx, cancel := context.WithCancel(context.Background())

	term := terminal.NewTerminal(channel, config.Prompt)

	return &Session{
		channel:      channel,
		config:       config,
		term:         term,
		ctx:          ctx,
		cancel:       cancel,
		conversation: []string{config.SystemPrompt},
	}
}

// Start starts the shell session
func (s *Session) Start() error {
	// Main loop
	for {
		line, err := s.term.ReadLine()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}

		// Handle exit command
		if strings.TrimSpace(line) == "exit" {
			return nil
		}

		// Parse the command
		tokens := Parse(line)
		if len(tokens) == 0 {
			continue
		}

		// Send to LLM
		response, err := s.sendToLLM(tokens)
		if err != nil {
			fmt.Fprintf(s.term, "Error: %v\n", err)
			continue
		}

		// Strip any markdown artifacts and print response
		fmt.Fprintln(s.term, stripMarkdown(response))
	}
}

// sendToLLM sends the tokens to the LLM and returns the response
func (s *Session) sendToLLM(tokens []string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Add user message to conversation
	userMessage := strings.Join(tokens, " ")
	s.conversation = append(s.conversation, "User: "+userMessage)

	// Get response from LLM
	response, err := s.config.LLMClient.Complete(s.ctx, s.conversation)
	if err != nil {
		return "", err
	}

	// Add response to conversation history
	s.conversation = append(s.conversation, "Assistant: "+response)

	return response, nil
}

// stripMarkdown removes common markdown formatting artifacts from LLM output
func stripMarkdown(s string) string {
	lines := strings.Split(s, "\n")
	var result []string
	for _, line := range lines {
		// Remove code block fences (```bash, ```, etc.)
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			continue
		}
		// Remove inline backticks wrapping entire lines
		if len(trimmed) >= 2 && trimmed[0] == '`' && trimmed[len(trimmed)-1] == '`' {
			line = strings.TrimSpace(line)
			line = line[1 : len(line)-1]
		}
		result = append(result, line)
	}
	return strings.Join(result, "\n")
}
