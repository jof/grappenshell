package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/jof/grappenshell/internal/config"
	"github.com/jof/grappenshell/internal/llm"
	"github.com/jof/grappenshell/internal/shell"
	"github.com/jof/grappenshell/internal/ssh"
	"tailscale.com/tsnet"
)

func main() {
	configPath := flag.String("config", "config.jsonc", "Path to config file (JSON or JSONC/JWCC)")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Create shared Tailscale tsnet server
	tsServer := &tsnet.Server{
		Hostname: cfg.Hostname,
	}

	// Get a Tailscale-aware HTTP client for the LLM API
	httpClient := tsServer.HTTPClient()

	// Create LLM client that talks over Tailscale
	llmClient := llm.NewOpenAIClient(httpClient, cfg.LLMURL, cfg.LLMModel)

	// Create shell config
	shellConfig := &shell.Config{
		SystemPrompt: cfg.SystemPrompt,
		Hostname:     cfg.SimHostname,
		DefaultUser:  cfg.DefaultUser,
		DefaultHome:  cfg.DefaultHome,
		LLMClient:    llmClient,
	}

	// Create SSH server using the shared tsnet server
	server, err := ssh.NewServer(tsServer, shellConfig, cfg.SSHPort)
	if err != nil {
		log.Fatalf("Failed to create SSH server: %v", err)
	}

	// Handle signals
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start the server in a goroutine
	errChan := make(chan error, 1)
	go func() {
		errChan <- server.Start(ctx)
	}()

	// Wait for either an error or a signal
	select {
	case err := <-errChan:
		log.Fatalf("Server error: %v", err)
	case sig := <-sigChan:
		log.Printf("Received signal %v, shutting down...", sig)
		cancel()
		if err := server.Close(); err != nil {
			log.Printf("Error closing server: %v", err)
		}
		log.Println("Server shutdown complete")
	}
}
