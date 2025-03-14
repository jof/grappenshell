package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"log"
	"net"

	"github.com/jof/grappenshell/internal/shell"
	"golang.org/x/crypto/ssh"
	"tailscale.com/tsnet"
)

// Server represents the SSH server
type Server struct {
	tsServer    *tsnet.Server
	sshConfig   *ssh.ServerConfig
	shellConfig *shell.Config
	listener    net.Listener
}

// NewServer creates a new SSH server
func NewServer(hostname string, shellConfig *shell.Config) (*Server, error) {
	s := &Server{
		tsServer: &tsnet.Server{
			Hostname: hostname,
		},
		shellConfig: shellConfig,
	}

	// Set up SSH server authentication
	s.sshConfig = &ssh.ServerConfig{
		// Allow all authentications - Tailscale handles auth
		NoClientAuth: true,
	}

	// Generate server key
	privateKey, err := generateHostKey()
	if err != nil {
		return nil, fmt.Errorf("failed to generate host key: %v", err)
	}
	s.sshConfig.AddHostKey(privateKey)

	return s, nil
}

// Start starts the SSH server
func (s *Server) Start(ctx context.Context) error {
	// Start Tailscale
	ln, err := s.tsServer.Listen("tcp", ":22")
	if err != nil {
		return fmt.Errorf("failed to listen on Tailscale: %v", err)
	}
	s.listener = ln

	ipn, err := s.tsServer.LocalClient()
	if err != nil {
		return fmt.Errorf("failed to get Tailscale client: %v", err)
	}
	st, err := ipn.Status(ctx)
	if err != nil {
		return fmt.Errorf("failed to get Tailscale status: %v", err)
	}

	log.Printf("SSH server running on Tailscale node %s", st.Self.DNSName)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("Failed to accept connection: %v", err)
			continue
		}
		go s.handleConnection(conn)
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

		// Handle the session
		go s.handleSession(channel, requests)
	}
}

// handleSession handles an SSH session channel
func (s *Server) handleSession(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer channel.Close()

	// Create a new shell session
	session := shell.NewSession(channel, s.shellConfig)

	// Handle requests
	for req := range requests {
		switch req.Type {
		case "shell", "exec":
			// Accept the request
			req.Reply(true, nil)

			// Start the shell session
			if err := session.Start(); err != nil {
				log.Printf("Failed to start shell session: %v", err)
				return
			}

		case "pty-req":
			// Accept the request
			req.Reply(true, nil)

		case "window-change":
			// Handle window resize
			req.Reply(true, nil)

		default:
			log.Printf("Unhandled request type: %s", req.Type)
			req.Reply(false, nil)
		}
	}
}

// generateHostKey generates a new SSH host key
func generateHostKey() (ssh.Signer, error) {
	// In a real application, you'd want to persist this key
	_, privKey, err := ed25519.GenerateKey(rand.Reader)
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
