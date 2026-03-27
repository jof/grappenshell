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
	SystemPrompt string
	Hostname     string
	DefaultUser  string
	DefaultHome  string
	LLMClient    llm.Client
}

// Session represents a shell-like session
type Session struct {
	channel      io.ReadWriter
	config       *Config
	term         *terminal.Terminal
	state        *ShellState
	ctx          context.Context
	cancel       context.CancelFunc
	conversation []string
	mu           sync.Mutex
}

// NewSession creates a new shell session. sshUser overrides the default username
// so the session starts as whoever the SSH client authenticated as.
func NewSession(channel io.ReadWriter, config *Config, sshUser string) *Session {
	ctx, cancel := context.WithCancel(context.Background())

	user := config.DefaultUser
	home := config.DefaultHome
	if sshUser != "" {
		user = sshUser
		if sshUser == "root" {
			home = "/root"
		} else {
			home = "/home/" + sshUser
		}
	}

	state := NewShellState(config.Hostname, user, home)
	term := terminal.NewTerminal(channel, state.Prompt())

	return &Session{
		channel: channel,
		config:  config,
		term:    term,
		state:   state,
		ctx:     ctx,
		cancel:  cancel,
	}
}

// Start starts the shell session
func (s *Session) Start() error {
	for {
		line, err := s.term.ReadLine()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}

		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// Parse the command
		tokens := Parse(trimmed)
		if len(tokens) == 0 {
			continue
		}

		// Check if the command changes shell state (cd, export, sudo, etc.)
		handled := s.state.ApplyCommand(tokens)
		if handled {
			// Update the prompt to reflect new state
			s.term.SetPrompt(s.state.Prompt())

			// For state-only commands like cd, sudo -i, etc. — no LLM call needed.
			// But if they exited from root back to user via "exit", check if
			// we should actually disconnect (handled by ApplyCommand returning false).
			continue
		}

		// "exit" when not root — disconnect
		if tokens[0] == "exit" {
			return nil
		}

		// Send to LLM with current state context
		response, err := s.sendToLLM(trimmed)
		if err != nil {
			fmt.Fprintf(s.term, "Error: %v\n", err)
			continue
		}

		// Strip any markdown artifacts and print response
		cleaned := stripMarkdown(response)
		if cleaned != "" {
			fmt.Fprintln(s.term, cleaned)
		}

		// Update prompt in case the LLM response implies state we should track
		s.term.SetPrompt(s.state.Prompt())
	}
}

// buildSystemPrompt constructs the full system prompt with current shell state
func (s *Session) buildSystemPrompt() string {
	var b strings.Builder
	b.WriteString(s.config.SystemPrompt)
	b.WriteString("\n\n")
	b.WriteString(s.state.StateDescription())
	b.WriteString("\nThe shell prompt currently shown to the user is: ")
	b.WriteString(s.state.Prompt())
	b.WriteString("\nRespond with ONLY the raw terminal output for the command. No prompt, no markdown.")
	return b.String()
}

// sendToLLM sends the command to the LLM and returns the response
func (s *Session) sendToLLM(command string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Rebuild system prompt with current state and place it at conversation[0]
	systemPrompt := s.buildSystemPrompt()
	if len(s.conversation) == 0 {
		s.conversation = append(s.conversation, systemPrompt)
	} else {
		s.conversation[0] = systemPrompt
	}

	// Add user message
	s.conversation = append(s.conversation, "User: "+command)

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
