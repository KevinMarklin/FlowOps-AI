package channels

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"flowops-ai/internal/models"
	"golang.org/x/text/encoding/htmlindex"
)

func runEmail(ctx context.Context, cfg Config, ingest IngestFunc, logger interface {
	Info(string, ...any)
	Error(string, ...any)
}) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := pollEmail(ctx, cfg, ingest, logger); err != nil && ctx.Err() == nil {
			logger.Error("email polling failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type imapSession struct {
	conn   net.Conn
	reader *bufio.Reader
	next   int
}

func connectIMAP(ctx context.Context, address string) (*imapSession, error) {
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", address, &tls.Config{ServerName: hostName(address), MinVersion: tls.VersionTLS12})
	if err != nil {
		return nil, err
	}
	session := &imapSession{conn: conn, reader: bufio.NewReader(conn), next: 1}
	if _, err := session.reader.ReadString('\n'); err != nil {
		conn.Close()
		return nil, err
	}
	return session, nil
}

func hostName(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return strings.Split(address, ":")[0]
	}
	return host
}

func (s *imapSession) command(ctx context.Context, command string) ([]byte, error) {
	_ = s.conn.SetDeadline(time.Now().Add(30 * time.Second))
	tag := fmt.Sprintf("A%04d", s.next)
	s.next++
	if _, err := fmt.Fprintf(s.conn, "%s %s\r\n", tag, command); err != nil {
		return nil, err
	}
	var result bytes.Buffer
	for {
		line, err := s.reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		result.WriteString(line)
		trimmed := strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(trimmed, tag+" ") {
			if !strings.Contains(strings.ToUpper(trimmed), " OK") {
				return result.Bytes(), fmt.Errorf("IMAP command failed: %s", trimmed)
			}
			return result.Bytes(), nil
		}
		if open := strings.LastIndex(trimmed, "{"); open >= 0 && strings.HasSuffix(trimmed, "}") {
			n, parseErr := strconv.Atoi(strings.TrimSuffix(trimmed[open+1:], "}"))
			if parseErr == nil && n >= 0 && n < 32<<20 {
				literal := make([]byte, n)
				if _, err := io.ReadFull(s.reader, literal); err != nil {
					return nil, err
				}
				result.Write(literal)
				ending, err := s.reader.ReadString('\n')
				if err != nil {
					return nil, err
				}
				result.WriteString(ending)
			}
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
}

func pollEmail(ctx context.Context, cfg Config, ingest IngestFunc, logger interface {
	Info(string, ...any)
	Error(string, ...any)
}) error {
	session, err := connectIMAP(ctx, cfg.MailIMAPHost)
	if err != nil {
		return err
	}
	defer session.conn.Close()
	quote := func(value string) string { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"` }
	if _, err = session.command(ctx, "LOGIN "+quote(cfg.MailUser)+" "+quote(cfg.MailPassword)); err != nil {
		return err
	}
	if _, err = session.command(ctx, "SELECT INBOX"); err != nil {
		return err
	}
	result, err := session.command(ctx, "SEARCH UNSEEN")
	if err != nil {
		return err
	}
	ids := parseIMAPSearch(string(result))
	for _, id := range ids {
		fetched, fetchErr := session.command(ctx, fmt.Sprintf("FETCH %d (BODY.PEEK[])", id))
		if fetchErr != nil {
			logger.Error("email fetch failed", "message_id", id, "error", fetchErr)
			continue
		}
		raw := extractIMAPLiteral(fetched)
		message, parseErr := mail.ReadMessage(bytes.NewReader(raw))
		if parseErr != nil {
			logger.Error("email parse failed", "message_id", id, "error", parseErr)
			continue
		}
		from, parseErr := mail.ParseAddress(message.Header.Get("From"))
		if parseErr != nil {
			logger.Error("email sender parse failed", "message_id", id, "error", parseErr)
			_, _ = session.command(ctx, fmt.Sprintf("STORE %d +FLAGS (\\Seen)", id))
			continue
		}
		decoder := mime.WordDecoder{CharsetReader: decodeCharset}
		subject, _ := decoder.DecodeHeader(message.Header.Get("Subject"))
		bodyText, bodyErr := decodeMIMEBody(message.Header.Get("Content-Type"), message.Header.Get("Content-Transfer-Encoding"), message.Body)
		if bodyErr != nil {
			logger.Error("email body decode failed", "message_id", id, "error", bodyErr)
		}
		if strings.TrimSpace(bodyText) == "" {
			bodyText = "(Письмо без текстового содержимого)"
		}
		result, ingestErr := ingest(ctx, models.IngestRequest{Channel: "email", Sender: from.Address, Subject: subject, Message: bodyText})
		if ingestErr != nil {
			logger.Error("email ingest failed", "message_id", id, "error", ingestErr)
			continue
		}
		if err := sendEmail(ctx, cfg, from.Address, fmt.Sprintf("Re: [Заявка #%d] %s", result.TicketID, subject), result.ReplyText); err != nil {
			logger.Error("email auto-reply failed", "ticket_id", result.TicketID, "error", err)
		}
		if _, err := session.command(ctx, fmt.Sprintf("STORE %d +FLAGS (\\Seen)", id)); err != nil {
			logger.Error("email mark-seen failed", "message_id", id, "error", err)
		}
	}
	return nil
}

func parseIMAPSearch(response string) []int {
	for _, line := range strings.Split(response, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) > 2 && fields[0] == "*" && strings.EqualFold(fields[1], "SEARCH") {
			fields = fields[2:]
			ids := make([]int, 0, len(fields))
			for _, field := range fields {
				if id, err := strconv.Atoi(field); err == nil && id > 0 {
					ids = append(ids, id)
				}
			}
			return ids
		}
	}
	return nil
}

func extractIMAPLiteral(response []byte) []byte {
	for open := bytes.IndexByte(response, '{'); open >= 0; {
		closeOffset := bytes.IndexByte(response[open+1:], '}')
		if closeOffset < 0 {
			break
		}
		close := open + 1 + closeOffset
		n, err := strconv.Atoi(string(response[open+1 : close]))
		if err == nil && n >= 0 {
			start := close + 1
			for start < len(response) && (response[start] == '\r' || response[start] == '\n') {
				start++
			}
			if start+n <= len(response) {
				return response[start : start+n]
			}
		}
		next := bytes.IndexByte(response[open+1:], '{')
		if next < 0 {
			break
		}
		open += 1 + next
	}
	return response
}

func decodeCharset(label string, input io.Reader) (io.Reader, error) {
	encoding, err := htmlindex.Get(label)
	if err != nil {
		return nil, err
	}
	return encoding.NewDecoder().Reader(input), nil
}

func decodeMIMEBody(contentType, transferEncoding string, body io.Reader) (string, error) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = "text/plain"
		params = nil
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		reader := multipart.NewReader(body, params["boundary"])
		for {
			part, nextErr := reader.NextPart()
			if nextErr == io.EOF {
				return "", nil
			}
			if nextErr != nil {
				return "", nextErr
			}
			text, partErr := decodeMIMEBody(part.Header.Get("Content-Type"), part.Header.Get("Content-Transfer-Encoding"), part)
			part.Close()
			if partErr != nil {
				continue
			}
			if strings.TrimSpace(text) != "" {
				return text, nil
			}
		}
	}
	var reader io.Reader = body
	switch strings.ToLower(strings.TrimSpace(transferEncoding)) {
	case "base64":
		reader = base64.NewDecoder(base64.StdEncoding, body)
	case "quoted-printable":
		reader = quotedprintable.NewReader(body)
	}
	data, err := io.ReadAll(io.LimitReader(reader, 4<<20))
	if err != nil {
		return "", err
	}
	if charset := params["charset"]; charset != "" && !strings.EqualFold(charset, "utf-8") && !strings.EqualFold(charset, "us-ascii") {
		decoded, decodeErr := decodeCharset(charset, bytes.NewReader(data))
		if decodeErr == nil {
			data, err = io.ReadAll(decoded)
		}
	}
	return strings.ToValidUTF8(string(data), "�"), err
}

func sendEmail(ctx context.Context, cfg Config, to, subject, body string) error {
	address, err := mail.ParseAddress(to)
	if err != nil {
		return err
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 15 * time.Second}, "tcp", cfg.MailSMTPHost, &tls.Config{ServerName: hostName(cfg.MailSMTPHost), MinVersion: tls.VersionTLS12})
	if err != nil {
		return err
	}
	client, err := smtp.NewClient(conn, hostName(cfg.MailSMTPHost))
	if err != nil {
		conn.Close()
		return err
	}
	defer client.Close()
	if cfg.MailUser != "" {
		if err := client.Auth(smtp.PlainAuth("", cfg.MailUser, cfg.MailPassword, hostName(cfg.MailSMTPHost))); err != nil {
			return err
		}
	}
	from, err := mail.ParseAddress(cfg.MailUser)
	if err != nil {
		from = &mail.Address{Address: cfg.MailUser}
	}
	if err := client.Mail(from.Address); err != nil {
		return err
	}
	if err := client.Rcpt(address.Address); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	encodedSubject := "=?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(subject)) + "?="
	message := "From: " + from.String() + "\r\nTo: " + address.String() + "\r\nSubject: " + encodedSubject + "\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString([]byte(body)) + "\r\n"
	if _, err := io.WriteString(writer, message); err != nil {
		writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return client.Quit()
}
