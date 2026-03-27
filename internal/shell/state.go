package shell

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ShellState tracks the simulated shell environment
type ShellState struct {
	User     string
	Hostname string
	Cwd      string
	Home     string
	Env      map[string]string

	// Track the "outer" user for returning from sudo/su
	outerUser string
	outerHome string
	outerCwd  string

	// Log of commands that modified the simulated filesystem/system state
	Modifications []string
}

// NewShellState creates a new shell state with initial values
func NewShellState(hostname, user, home string) *ShellState {
	return &ShellState{
		User:     user,
		Hostname: hostname,
		Cwd:      home,
		Home:     home,
		Env: map[string]string{
			"HOME":  home,
			"USER":  user,
			"SHELL": "/bin/bash",
			"PATH":  "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
			"TERM":  "xterm-256color",
			"LANG":  "en_US.UTF-8",
		},
	}
}

// IsRoot returns true if the current user is root
func (s *ShellState) IsRoot() bool {
	return s.User == "root"
}

// Prompt returns the current bash-style prompt
func (s *ShellState) Prompt() string {
	dir := s.displayCwd()
	suffix := "$"
	if s.IsRoot() {
		suffix = "#"
	}
	return fmt.Sprintf("%s@%s:%s%s ", s.User, s.Hostname, dir, suffix)
}

// displayCwd returns CWD with ~ substitution for home directory
func (s *ShellState) displayCwd() string {
	if s.Cwd == s.Home {
		return "~"
	}
	if strings.HasPrefix(s.Cwd, s.Home+"/") {
		return "~" + s.Cwd[len(s.Home):]
	}
	return s.Cwd
}

// StateDescription returns a description of current state for the system prompt
func (s *ShellState) StateDescription() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Current shell state:\n")
	fmt.Fprintf(&b, "- User: %s\n", s.User)
	fmt.Fprintf(&b, "- Hostname: %s\n", s.Hostname)
	fmt.Fprintf(&b, "- CWD: %s\n", s.Cwd)
	fmt.Fprintf(&b, "- Home: %s\n", s.Home)
	if s.IsRoot() {
		fmt.Fprintf(&b, "- Running as ROOT (prompt ends with #)\n")
	}

	if len(s.Env) > 0 {
		fmt.Fprintf(&b, "- Environment variables:\n")
		for k, v := range s.Env {
			fmt.Fprintf(&b, "  %s=%s\n", k, v)
		}
	}

	if len(s.Modifications) > 0 {
		fmt.Fprintf(&b, "\nSESSION MODIFICATIONS (the user has run these commands that changed system state — be consistent with their effects):\n")
		for _, m := range s.Modifications {
			fmt.Fprintf(&b, "- %s\n", m)
		}
	}

	return b.String()
}

// RecordIfModifying checks if a command line could have side effects and records it.
// Call this for every command sent to the LLM.
func (s *ShellState) RecordIfModifying(commandLine string) {
	tokens := strings.Fields(commandLine)
	if len(tokens) == 0 {
		return
	}

	// Check for output redirection anywhere in the command
	hasRedirect := false
	for _, t := range tokens {
		if t == ">" || t == ">>" || strings.HasPrefix(t, ">") || strings.HasPrefix(t, ">>") {
			hasRedirect = true
			break
		}
	}
	if hasRedirect {
		s.Modifications = append(s.Modifications, fmt.Sprintf("[cwd:%s] %s", s.Cwd, commandLine))
		return
	}

	// Check if the base command is known to have side effects
	cmd := tokens[0]
	// Strip leading path (e.g. /usr/bin/touch -> touch)
	if idx := strings.LastIndexByte(cmd, '/'); idx >= 0 {
		cmd = cmd[idx+1:]
	}

	if isModifyingCommand(cmd) {
		s.Modifications = append(s.Modifications, fmt.Sprintf("[cwd:%s] %s", s.Cwd, commandLine))
	}
}

// isModifyingCommand returns true if the command is known to potentially modify
// filesystem, process, or system state.
func isModifyingCommand(cmd string) bool {
	switch cmd {
	case
		// File operations
		"touch", "mkdir", "rmdir", "rm", "mv", "cp", "ln",
		"install", "rsync", "tar", "unzip", "zip", "gzip", "gunzip",
		// File content modification
		"tee", "sed", "truncate", "dd", "patch",
		// Permissions
		"chmod", "chown", "chgrp",
		// Package management
		"apt", "apt-get", "dpkg", "yum", "dnf", "pip", "pip3", "npm", "cargo",
		// Process management
		"kill", "killall", "pkill",
		// System
		"systemctl", "service", "mount", "umount",
		// Editors (imply file modification)
		"vi", "vim", "nano", "ed",
		// Docker
		"docker", "podman",
		// Git
		"git",
		// Network
		"wget", "curl":
		return true
	}
	// sudo <modifying command>
	if cmd == "sudo" {
		return true
	}
	return false
}

// ApplyCommand inspects a command and updates state heuristically.
// Returns true if the command was a state-changing builtin that
// should NOT be sent to the LLM (like bare `cd`).
// Returns false if the command should still be sent to the LLM for output.
func (s *ShellState) ApplyCommand(tokens []string) (handled bool) {
	if len(tokens) == 0 {
		return false
	}

	cmd := tokens[0]

	switch cmd {
	case "cd":
		s.handleCd(tokens)
		return true

	case "export":
		if len(tokens) >= 2 {
			s.handleExport(tokens[1:])
			return true
		}
		return false

	case "unset":
		for _, key := range tokens[1:] {
			delete(s.Env, key)
		}
		return true

	case "sudo":
		return s.handleSudo(tokens)

	case "su":
		return s.handleSu(tokens)

	case "exit":
		// If root, return to outer user instead of disconnecting
		if s.outerUser != "" {
			s.User = s.outerUser
			s.Home = s.outerHome
			s.Cwd = s.outerCwd
			s.Env["USER"] = s.outerUser
			s.Env["HOME"] = s.outerHome
			s.outerUser = ""
			s.outerHome = ""
			s.outerCwd = ""
			return true
		}
		return false // let session handle disconnect

	default:
		return false
	}
}

func (s *ShellState) handleCd(tokens []string) {
	if len(tokens) < 2 || tokens[1] == "~" {
		s.Cwd = s.Home
		return
	}

	target := tokens[1]

	// Handle ~ prefix
	if strings.HasPrefix(target, "~/") {
		target = s.Home + target[1:]
	}

	// Handle relative vs absolute
	if !strings.HasPrefix(target, "/") {
		target = s.Cwd + "/" + target
	}

	// Clean the path
	s.Cwd = filepath.Clean(target)

	// Handle cd ..
	if s.Cwd == "." {
		s.Cwd = "/"
	}
}

func (s *ShellState) handleExport(args []string) {
	for _, arg := range args {
		if idx := strings.IndexByte(arg, '='); idx >= 0 {
			key := arg[:idx]
			val := arg[idx+1:]
			s.Env[key] = val
		}
	}
}

func (s *ShellState) handleSudo(tokens []string) bool {
	// sudo -i, sudo -s, sudo su, sudo su -
	for _, arg := range tokens[1:] {
		switch arg {
		case "-i", "-s":
			s.becomeRoot()
			return true
		case "su":
			s.becomeRoot()
			return true
		}
	}
	// sudo <command> — don't change state, let LLM handle output
	return false
}

func (s *ShellState) handleSu(tokens []string) bool {
	// su, su -, su -l, su root — all become root
	// su <user> — become that user (simplified: just become root)
	if len(tokens) == 1 || tokens[1] == "-" || tokens[1] == "-l" || tokens[1] == "root" {
		s.becomeRoot()
		return true
	}
	// su <other_user> — we don't track arbitrary users, let LLM handle
	return false
}

func (s *ShellState) becomeRoot() {
	if s.User != "root" {
		s.outerUser = s.User
		s.outerHome = s.Home
		s.outerCwd = s.Cwd
	}
	s.User = "root"
	s.Home = "/root"
	s.Cwd = "/root"
	s.Env["USER"] = "root"
	s.Env["HOME"] = "/root"
}
