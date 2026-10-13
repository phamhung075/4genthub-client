package clientruntime

import (
	"testing"
)

func TestDefaultSocketPath(t *testing.T) {
	path := DefaultSocketPath()
	if path == "" {
		t.Fatal("expected non-empty socket path")
	}
}

func TestNewClient(t *testing.T) {
	client := NewClient("/tmp/test.sock")
	if client.SocketPath != "/tmp/test.sock" {
		t.Fatalf("expected /tmp/test.sock, got %s", client.SocketPath)
	}
}
