package channels

import (
	"bufio"
	"context"
	"net"
	"testing"
	"time"
)

func TestIMAPCommandHonorsContextDeadline(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := (&imapSession{conn: client, reader: bufio.NewReader(client), next: 1}).command(ctx, "NOOP")
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("command exceeded context deadline: %v", err)
	}
}
