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

	// Shell prompt displayed to the user (e.g. "user@myhost:~$ ")
	Prompt string `json:"prompt"`

	// LLM API base URL (OpenAI-compatible)
	LLMURL string `json:"llm_url"`

	// Model name for the LLM API
	LLMModel string `json:"llm_model"`

	// System prompt that defines the shell persona
	SystemPrompt string `json:"system_prompt"`

	// SSH listen port on the tsnet node
	SSHPort int `json:"ssh_port"`
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

	cfg.applyDefaults()
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Hostname == "" {
		c.Hostname = "grappenshell"
	}
	if c.Prompt == "" {
		c.Prompt = "user@grappen:~$ "
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
}
