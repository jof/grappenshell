package shell

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"strings"
	"sync"
	"time"

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
	MotdCommand  string
	PromptString string
	ShellMode    bool
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

	prompt := state.Prompt()
	if config.PromptString != "" {
		prompt = config.PromptString
	}
	term := terminal.NewTerminal(channel, prompt)

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
	// If a MOTD command is configured, send it to the LLM on first connect
	if s.config.MotdCommand != "" {
		if err := s.streamToLLM(s.config.MotdCommand); err != nil {
			fmt.Fprintf(s.term, "Error: %v\n", err)
		}
	}

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

		// Record any side effects for future consistency
		s.state.RecordIfModifying(trimmed)

		// Send to LLM with streaming output
		if err := s.streamToLLM(trimmed); err != nil {
			fmt.Fprintf(s.term, "Error: %v\n", err)
			continue
		}

		// Update prompt in case the LLM response implies state we should track
		if s.config.PromptString == "" {
			s.term.SetPrompt(s.state.Prompt())
		}
	}
}

// buildSystemPrompt constructs the full system prompt with current shell state
func (s *Session) buildSystemPrompt() string {
	var b strings.Builder
	b.WriteString(s.config.SystemPrompt)
	b.WriteString("\n\n")
	if s.config.ShellMode {
		b.WriteString(s.state.StateDescription())
		b.WriteString("\nThe shell prompt currently shown to the user is: ")
		b.WriteString(s.state.Prompt())
		b.WriteString("\nRespond with ONLY the raw terminal output for the command. No prompt, no markdown.")
	} else {
		fmt.Fprintf(&b, "The connected user is: %s\n", s.state.User)
		b.WriteString("Respond with ONLY raw terminal output. No prompt, no markdown.")
	}
	return b.String()
}

// streamToLLM sends a command to the LLM and streams tokens to the terminal
// as they arrive, then records the full response in conversation history.
func (s *Session) streamToLLM(command string) error {
	s.mu.Lock()

	systemPrompt := s.buildSystemPrompt()
	if len(s.conversation) == 0 {
		s.conversation = append(s.conversation, systemPrompt)
	} else {
		s.conversation[0] = systemPrompt
	}
	s.conversation = append(s.conversation, "User: "+command)
	conv := make([]string, len(s.conversation))
	copy(conv, s.conversation)
	s.mu.Unlock()

	// Buffer tokens until newline so unescapeANSI sees complete escape
	// sequences that may be split across token boundaries.
	var lineBuf strings.Builder
	response, err := s.config.LLMClient.CompleteStream(s.ctx, conv, func(token string) {
		lineBuf.WriteString(token)
		for {
			text := lineBuf.String()
			idx := strings.Index(text, "\n")
			if idx == -1 {
				break
			}
			line := unescapeANSI(text[:idx])
			lineBuf.Reset()
			lineBuf.WriteString(text[idx+1:])
			s.channel.Write([]byte(line))
			s.channel.Write([]byte("\r\n"))
			// Small random delay per line for a more natural feel
			time.Sleep(time.Duration(10+rand.Intn(30)) * time.Millisecond)
		}
	})
	// Flush any remaining buffered text
	if remaining := lineBuf.String(); remaining != "" {
		s.channel.Write([]byte(unescapeANSI(remaining)))
	}
	s.channel.Write([]byte("\r\n"))

	if err != nil {
		return err
	}

	s.mu.Lock()
	s.conversation = append(s.conversation, "Assistant: "+response)
	s.mu.Unlock()
	return nil
}

// sendToLLM sends the command to the LLM and returns the response (non-streaming)
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

// unescapeANSI converts literal escape sequences in LLM output to real ANSI
// escape bytes so terminals render colors and formatting correctly.
// Handles \033[, \x1b[, and \e[ notations.
func unescapeANSI(s string) string {
	// CSI sequences (ESC [)
	s = strings.ReplaceAll(s, `\033[`, "\033[")
	s = strings.ReplaceAll(s, `\x1b[`, "\033[")
	s = strings.ReplaceAll(s, `\e[`, "\033[")
	s = strings.ReplaceAll(s, `<ESC>[`, "\033[")
	s = strings.ReplaceAll(s, `<033[`, "\033[")
	// OSC sequences (ESC ])
	s = strings.ReplaceAll(s, `\033]`, "\033]")
	s = strings.ReplaceAll(s, `\x1b]`, "\033]")
	s = strings.ReplaceAll(s, `<ESC>]`, "\033]")
	s = strings.ReplaceAll(s, `<033]`, "\033]")
	return s
}

// writeRaw writes directly to the SSH channel, bypassing terminal.Terminal's
// control-character sanitization so ANSI escape codes render correctly.
func (s *Session) writeRaw(text string) {
	// SSH terminals expect \r\n line endings
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		s.channel.Write([]byte(line))
		if i < len(lines)-1 {
			s.channel.Write([]byte("\r\n"))
		}
	}
	s.channel.Write([]byte("\r\n"))
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
