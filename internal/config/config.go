package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tailscale/hujson"
)

// Config holds all configuration for grappenshell
type Config struct {
	// Tailscale hostname for the tsnet node
	Hostname string `json:"hostname"`

	// Simulated hostname shown in the shell prompt
	SimHostname string `json:"sim_hostname"`

	// Default username for the simulated shell
	DefaultUser string `json:"default_user"`

	// Default home directory
	DefaultHome string `json:"default_home"`

	// LLM API base URL (OpenAI-compatible)
	LLMURL string `json:"llm_url"`

	// Model name for the LLM API
	LLMModel string `json:"llm_model"`

	// System prompt inline (used if system_prompt_file is not set)
	SystemPrompt string `json:"system_prompt"`

	// Path to a file containing the system prompt (takes precedence over system_prompt)
	SystemPromptFile string `json:"system_prompt_file"`

	// SSH listen port on the tsnet node
	SSHPort int `json:"ssh_port"`

	// tsnet state directory (derived from hostname if not set)
	StateDir string `json:"state_dir"`

	// Command to auto-send to the LLM on session start (e.g. for a login banner)
	MotdCommand string `json:"motd_command"`
}

// Load reads a config file (JSON or JWCC/HuJSON) from the given path
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", path, err)
	}

	ext := filepath.Ext(path)
	switch ext {
	case ".jsonc", ".jwcc":
		// Standardize HuJSON (comments + trailing commas) to plain JSON
		data, err = hujson.Standardize(data)
		if err != nil {
			return nil, fmt.Errorf("failed to parse HuJSON config: %w", err)
		}
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	// Load system prompt from file if specified
	if cfg.SystemPromptFile != "" {
		promptPath := cfg.SystemPromptFile
		if !filepath.IsAbs(promptPath) {
			promptPath = filepath.Join(filepath.Dir(path), promptPath)
		}
		promptData, err := os.ReadFile(promptPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read system prompt file %s: %w", promptPath, err)
		}
		cfg.SystemPrompt = string(promptData)
	}

	cfg.applyDefaults()
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Hostname == "" {
		c.Hostname = "grappenshell"
	}
	if c.SimHostname == "" {
		c.SimHostname = "grappenshell"
	}
	if c.DefaultUser == "" {
		c.DefaultUser = "user"
	}
	if c.DefaultHome == "" {
		c.DefaultHome = "/home/" + c.DefaultUser
	}
	if c.LLMURL == "" {
		c.LLMURL = "http://localhost:11434/v1"
	}
	if c.LLMModel == "" {
		c.LLMModel = "llama3"
	}
	if c.SSHPort == 0 {
		c.SSHPort = 2222
	}
	if c.StateDir == "" {
		c.StateDir = filepath.Join("/var/lib", "tsnet-"+c.Hostname)
	}
}
