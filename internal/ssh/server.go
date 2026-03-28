package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/jof/grappenshell/internal/shell"
	"golang.org/x/crypto/ssh"
	"tailscale.com/tsnet"
)

// Server represents the SSH server
type Server struct {
	tsServer    *tsnet.Server
	sshConfig   *ssh.ServerConfig
	shellConfig *shell.Config
	sshPort     int
	listener    net.Listener
}

// NewServer creates a new SSH server using the provided tsnet.Server.
// stateDir is used to persist the SSH host key across restarts.
func NewServer(tsServer *tsnet.Server, shellConfig *shell.Config, sshPort int, stateDir string) (*Server, error) {
	s := &Server{
		tsServer:    tsServer,
		shellConfig: shellConfig,
		sshPort:     sshPort,
	}

	// Set up SSH server authentication
	s.sshConfig = &ssh.ServerConfig{
		// Allow all authentications - Tailscale handles auth
		NoClientAuth: true,
	}

	// Load or generate host key, persisted in stateDir
	privateKey, err := loadOrGenerateHostKey(stateDir)
	if err != nil {
		return nil, fmt.Errorf("failed to load host key: %v", err)
	}
	s.sshConfig.AddHostKey(privateKey)

	return s, nil
}

// Start starts the SSH server
func (s *Server) Start(ctx context.Context) error {
	// Ensure tsnet node is fully connected before listening
	log.Println("Waiting for Tailscale to come up...")
	status, err := s.tsServer.Up(ctx)
	if err != nil {
		return fmt.Errorf("failed to start Tailscale: %v", err)
	}
	log.Printf("Tailscale is up: %s (%s)", status.TailscaleIPs[0], status.Self.DNSName)

	// Debug HTTP listener to test tsnet TCP connectivity
	httpLn, err := s.tsServer.Listen("tcp", ":8080")
	if err != nil {
		log.Printf("Warning: failed to start debug HTTP listener: %v", err)
	} else {
		log.Println("Debug HTTP listener on :8080")
		go http.Serve(httpLn, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "grappenshell is alive\n")
		}))
	}

	addr := fmt.Sprintf(":%d", s.sshPort)
	log.Printf("Starting Tailscale listener on %s...", addr)
	listener, err := s.tsServer.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on Tailscale: %v", err)
	}
	s.listener = listener
	log.Println("Tailscale listener ready")

	log.Println("Accepting connections...")
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
			conn, err := listener.Accept()
			if err != nil {
				log.Printf("Failed to accept connection: %v", err)
				continue
			}
			go s.handleConnection(conn)
		}
	}
}

// handleConnection handles an incoming SSH connection
func (s *Server) handleConnection(conn net.Conn) {
	defer conn.Close()

	// Perform SSH handshake
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, s.sshConfig)
	if err != nil {
		log.Printf("Failed to handshake: %v", err)
		return
	}
	defer sshConn.Close()

	log.Printf("New SSH connection from %s (%s)", sshConn.RemoteAddr(), sshConn.ClientVersion())

	// Discard all global requests
	go ssh.DiscardRequests(reqs)

	// Accept all channels
	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
			continue
		}

		channel, requests, err := newChannel.Accept()
		if err != nil {
			log.Printf("Failed to accept channel: %v", err)
			continue
		}

		// Handle the session with the SSH username
		go s.handleSession(channel, requests, sshConn.User())
	}
}

// handleSession handles an SSH session channel
func (s *Server) handleSession(channel ssh.Channel, requests <-chan *ssh.Request, sshUser string) {
	defer channel.Close()

	// Create a new shell session using the SSH username
	session := shell.NewSession(channel, s.shellConfig, sshUser)

	// Handle requests
	for req := range requests {
		switch req.Type {
		case "shell", "exec":
			// Accept the request
			req.Reply(true, nil)

			// Start the shell session
			if err := session.Start(); err != nil {
				log.Printf("Failed to start shell session: %v", err)
			}

			// Send exit-status so the SSH client knows the session is over
			exitStatus := make([]byte, 4)
			binary.BigEndian.PutUint32(exitStatus, 0)
			channel.SendRequest("exit-status", false, exitStatus)
			return

		case "pty-req":
			// Accept the request
			req.Reply(true, nil)

		case "window-change":
			// Handle window resize
			req.Reply(true, nil)

		case "env":
			// Accept environment variable requests silently
			req.Reply(true, nil)

		default:
			log.Printf("Unhandled request type: %s", req.Type)
			req.Reply(false, nil)
		}
	}
}

// loadOrGenerateHostKey loads an ed25519 host key from stateDir/ssh_host_ed25519_key,
// or generates and persists a new one if the file doesn't exist.
func loadOrGenerateHostKey(stateDir string) (ssh.Signer, error) {
	keyPath := filepath.Join(stateDir, "ssh_host_ed25519_key")

	// Try to load existing key
	data, err := os.ReadFile(keyPath)
	if err == nil {
		signer, err := ssh.ParsePrivateKey(data)
		if err == nil {
			log.Printf("Loaded SSH host key from %s", keyPath)
			return signer, nil
		}
		log.Printf("Warning: failed to parse existing host key %s: %v (regenerating)", keyPath, err)
	}

	// Generate new key
	_, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate ed25519 key: %w", err)
	}

	// Marshal to PEM
	keyBytes, err := x509.MarshalPKCS8PrivateKey(privKey)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal private key: %w", err)
	}
	pemBlock := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: keyBytes,
	})

	// Persist to disk
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create state dir %s: %w", stateDir, err)
	}
	if err := os.WriteFile(keyPath, pemBlock, 0600); err != nil {
		return nil, fmt.Errorf("failed to write host key to %s: %w", keyPath, err)
	}
	log.Printf("Generated and saved new SSH host key to %s", keyPath)

	signer, err := ssh.NewSignerFromKey(privKey)
	if err != nil {
		return nil, err
	}
	return signer, nil
}

// Close shuts down the SSH server and cleans up resources
func (s *Server) Close() error {
	// Close the listener if it exists
	if s.listener != nil {
		s.listener.Close()
	}

	// Close the Tailscale server
	return s.tsServer.Close()
}
