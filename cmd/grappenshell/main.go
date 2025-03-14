package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/jof/grappenshell/internal/llm"
	"github.com/jof/grappenshell/internal/shell"
	"github.com/jof/grappenshell/internal/ssh"
)

func main() {
	// Parse command line flags
	hostname := flag.String("hostname", "grappenshell", "Tailscale hostname")
	systemPrompt := flag.String("system-prompt", "You are a helpful assistant in an SSH shell. Answer user queries concisely.", "System prompt for the LLM")
	flag.Parse()

	// Create LLM client
	llmClient := llm.NewMockClient()

	// Create shell config
	shellConfig := &shell.Config{
		SystemPrompt: *systemPrompt,
		LLMClient:    llmClient,
	}

	// Create SSH server
	server, err := ssh.NewServer(*hostname, shellConfig)
	if err != nil {
		log.Fatalf("Failed to create SSH server: %v", err)
	}

	// Handle signals
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Println("Shutting down...")
		cancel()
	}()

	// Start the server
	log.Fatal(server.Start(ctx))
}
