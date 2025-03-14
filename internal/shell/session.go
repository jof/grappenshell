package shell

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"

	"github.com/jof/grappenshell/internal/llm"
	"golang.org/x/crypto/ssh/terminal"
)

// Config holds configuration for the shell session
type Config struct {
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

	term := terminal.NewTerminal(channel, "llm> ")

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
	// Print welcome message
	fmt.Fprintln(s.term, "Welcome to the LLM Shell. Type 'exit' to quit.")
	fmt.Fprintln(s.term, "Use Ctrl+C to interrupt the current response.")
	fmt.Fprintln(s.term, "")

	// Set up terminal raw mode for handling control characters
	oldState, err := terminal.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("failed to set terminal to raw mode: %v", err)
	}
	defer terminal.Restore(int(os.Stdin.Fd()), oldState)

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

		// Print response
		fmt.Fprintln(s.term, response)
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

// HandleSignal handles terminal signals like Ctrl+C and Ctrl+Z
func (s *Session) HandleSignal(signal syscall.Signal) {
	switch signal {
	case syscall.SIGINT: // Ctrl+C
		s.cancel()
		s.ctx, s.cancel = context.WithCancel(context.Background())
		fmt.Fprintln(s.term, "\n^C")
	case syscall.SIGTSTP: // Ctrl+Z
		fmt.Fprintln(s.term, "\n^Z (Job control not supported)")
	}
}
