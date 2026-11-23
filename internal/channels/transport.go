package channels

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"time"
)

func rejectChannelRedirect(*http.Request, []*http.Request) error {
	return errors.New("channel redirects are disabled")
}

// The deadline covers greetings and all protocol commands, not only TCP/TLS setup.
// Cancellation closes the connection to interrupt net/smtp and IMAP reads.
func dialMail(ctx context.Context, address string) (net.Conn, func(), error) {
	return dialMailSession(ctx, address, 30*time.Second)
}

func dialIMAP(ctx context.Context, address string) (net.Conn, func(), error) {
	// A mailbox poll may process many messages. Bound each I/O command rather
	// than expiring the connection while model/storage work is being performed.
	return dialMailSession(ctx, address, 0)
}

func dialMailSession(ctx context.Context, address string, lifetime time.Duration) (net.Conn, func(), error) {
	var cancel context.CancelFunc
	if lifetime > 0 {
		ctx, cancel = context.WithTimeout(ctx, lifetime)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: 15 * time.Second}, Config: &tls.Config{ServerName: hostName(address), MinVersion: tls.VersionTLS12}}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		cancel()
		return nil, nil, errors.New("mail connection failed")
	}
	deadline := time.Now().Add(30 * time.Second)
	if outer, ok := ctx.Deadline(); ok && outer.Before(deadline) {
		deadline = outer
	}
	if err := conn.SetDeadline(deadline); err != nil {
		conn.Close()
		cancel()
		return nil, nil, err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	cleanup := func() { stop(); cancel(); conn.Close() }
	return conn, cleanup, nil
}
