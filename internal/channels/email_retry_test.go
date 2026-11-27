package channels

import (
	"bufio"
	"context"
	"errors"
	"flowops-ai/internal/models"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"
)

func TestEmailIngestFailureRemainsUnread(t *testing.T)  { testEmailAcknowledgement(t, true, false) }
func TestEmailReceiptFailureRemainsUnread(t *testing.T) { testEmailAcknowledgement(t, false, true) }
func TestEmailAcknowledgedAfterSuccessfulReceipt(t *testing.T) {
	testEmailAcknowledgement(t, false, false)
}

func testEmailAcknowledgement(t *testing.T, ingestFailure, sendFailure bool) {
	t.Helper()
	client, server := net.Pipe()
	defer client.Close()
	commands := make(chan []string, 1)
	go func() {
		defer server.Close()
		var seen []string
		reader := bufio.NewReader(server)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				commands <- seen
				return
			}
			parts := strings.SplitN(strings.TrimSpace(line), " ", 2)
			tag, command := parts[0], parts[1]
			seen = append(seen, command)
			switch {
			case command == "SELECT INBOX":
				fmt.Fprint(server, "* OK [UIDVALIDITY 123] valid\r\n")
			case command == "UID SEARCH UNSEEN":
				fmt.Fprint(server, "* SEARCH 7\r\n")
			case strings.HasPrefix(command, "UID FETCH"):
				raw := "From: employee@example.org\r\nSubject: Synthetic retry\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nVPN unavailable"
				fmt.Fprintf(server, "* 1 FETCH (BODY[] {%d}\r\n%s)\r\n", len(raw), raw)
			}
			fmt.Fprintf(server, "%s OK done\r\n", tag)
		}
	}()
	session := &imapSession{conn: client, reader: bufio.NewReader(client), next: 1}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := pollIMAPMailbox(ctx, session, Config{}, func(_ context.Context, req models.IngestRequest) (IngestResult, error) {
		if ingestFailure {
			return IngestResult{}, errors.New("database unavailable")
		}
		if req.IdempotencyKey == "" {
			t.Error("missing persistent retry key")
		}
		return IngestResult{TicketID: 1}, nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), func(context.Context, Config, string, string, int, string, string, bool) error {
		if sendFailure {
			return errors.New("SMTP unavailable")
		}
		return nil
	})
	client.Close()
	acknowledged := false
	for _, command := range <-commands {
		if strings.Contains(command, "UID STORE") {
			acknowledged = true
		}
	}
	failed := ingestFailure || sendFailure
	if acknowledged == failed {
		t.Errorf("acknowledged=%v, failure=%v", acknowledged, failed)
	}
	if (err != nil) != failed {
		t.Errorf("unexpected polling result: %v", err)
	}
}

func TestEmailRequestKeyIncludesMailboxGeneration(t *testing.T) {
	cfg := Config{MailIMAPHost: "imap.example.org:993", MailUser: "support@example.org"}
	first := emailRequestKey(cfg, "123", 7)
	if first != emailRequestKey(cfg, "123", 7) {
		t.Fatal("unstable retry key")
	}
	if first == emailRequestKey(cfg, "124", 7) || first == emailRequestKey(cfg, "123", 8) {
		t.Fatal("UID generation collision")
	}
	cfg.MailUser = "other@example.org"
	if first == emailRequestKey(cfg, "123", 7) {
		t.Fatal("mailbox collision")
	}
	for _, invalid := range []string{"", "* OK [UIDVALIDITY 0]", "* OK [UIDVALIDITY 4294967296]"} {
		if emailUIDValidity([]byte(invalid)) != "" {
			t.Fatal("invalid generation accepted")
		}
	}
	if emailUIDValidity([]byte("* OK [UIDVALIDITY 123] valid")) != "123" {
		t.Fatal("valid generation rejected")
	}
}
