package channels

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"html"
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
	"unicode"

	"flowops-ai/internal/models"
	"flowops-ai/internal/security"
	"golang.org/x/text/encoding/htmlindex"
)

func runEmail(ctx context.Context, cfg Config, ingest IngestFunc, logger interface {
	Info(string, ...any)
	Error(string, ...any)
}) {
	ctx = security.WithTenantID(ctx, cfg.TenantID)
	cfg.updateStatus("email", "starting", "")
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	failedUIDs := make(map[int]struct{})
	for ctx.Err() == nil {
		if err := pollEmail(ctx, cfg, ingest, logger, failedUIDs); err != nil && ctx.Err() == nil {
			cfg.updateStatus("email", "error", err.Error())
			logger.Error("email polling failed", "error", err.Error())
		} else if ctx.Err() == nil {
			cfg.updateStatus("email", "connected", "")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type imapSession struct {
	conn    net.Conn
	reader  *bufio.Reader
	next    int
	cleanup func()
}

func connectIMAP(ctx context.Context, address string) (*imapSession, error) {
	conn, cleanup, err := dialIMAP(ctx, address)
	if err != nil {
		return nil, err
	}
	session := &imapSession{conn: conn, reader: bufio.NewReader(conn), next: 1, cleanup: cleanup}
	if _, err := readIMAPLine(session.reader); err != nil {
		cleanup()
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
		line, err := readIMAPLine(s.reader)
		if err != nil {
			return nil, err
		}
		if result.Len()+len(line) > 32<<20 {
			return nil, fmt.Errorf("IMAP response exceeds limits")
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
			if parseErr != nil || n < 0 || n > 32<<20 || result.Len()+n > 32<<20 {
				return nil, fmt.Errorf("IMAP response exceeds limits")
			}
			if n >= 0 {
				literal := make([]byte, n)
				if _, err := io.ReadFull(s.reader, literal); err != nil {
					return nil, err
				}
				result.Write(literal)
				ending, err := readIMAPLine(s.reader)
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
}, failedUIDs map[int]struct{}) error {
	session, err := connectIMAP(ctx, cfg.MailIMAPHost)
	if err != nil {
		return err
	}
	defer session.cleanup()
	quote := func(value string) string { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"` }
	if _, err = session.command(ctx, "LOGIN "+quote(cfg.MailUser)+" "+quote(cfg.MailPassword)); err != nil {
		return err
	}
	if _, err = session.command(ctx, "SELECT INBOX"); err != nil {
		return err
	}
	result, err := session.command(ctx, "UID SEARCH UNSEEN")
	if err != nil {
		return err
	}
	ids := parseIMAPSearch(string(result))
	for _, id := range ids {
		if _, failed := failedUIDs[id]; failed {
			if _, err := session.command(ctx, fmt.Sprintf("UID STORE %d +FLAGS (\\Seen)", id)); err != nil {
				logger.Error("email acknowledgement failed", "message_uid", id, "error", err.Error())
			}
			continue
		}
		fetched, fetchErr := session.command(ctx, fmt.Sprintf("UID FETCH %d (BODY.PEEK[])", id))
		if fetchErr != nil {
			return fmt.Errorf("email fetch failed: %w", fetchErr)
		}
		raw := extractIMAPLiteral(fetched)
		message, parseErr := mail.ReadMessage(bytes.NewReader(raw))
		if parseErr != nil {
			logger.Error("email parse failed", "message_uid", id, "error", parseErr.Error())
			continue
		}
		from, parseErr := mail.ParseAddress(message.Header.Get("From"))
		if parseErr != nil {
			logger.Error("email sender parse failed", "message_uid", id, "error", parseErr.Error())
			if _, err := session.command(ctx, fmt.Sprintf("UID STORE %d +FLAGS (\\Seen)", id)); err != nil {
				logger.Error("email acknowledgement failed", "message_uid", id, "error", err.Error())
			}
			continue
		}
		if isSystemEmailSender(from.Address) {
			if _, err := session.command(ctx, fmt.Sprintf("UID STORE %d +FLAGS (\\Seen)", id)); err != nil {
				logger.Error("email acknowledgement failed", "message_uid", id, "error", err.Error())
			}
			continue
		}
		decoder := mime.WordDecoder{CharsetReader: decodeCharset}
		subject, _ := decoder.DecodeHeader(message.Header.Get("Subject"))
		bodyText, bodyErr := decodeMIMEBody(message.Header.Get("Content-Type"), message.Header.Get("Content-Transfer-Encoding"), message.Body)
		if bodyErr != nil {
			logger.Error("email body decode failed", "message_uid", id, "error", bodyErr.Error())
		}
		bodyText = cleanEmailText(bodyText)
		bodyText = trimEmailQuote(bodyText)
		if strings.TrimSpace(bodyText) == "" {
			bodyText = "(Письмо без текстового содержимого)"
		}
		result, ingestErr := ingest(ctx, models.IngestRequest{Channel: "email", Sender: from.Address, Subject: subject, Message: bodyText})
		if ingestErr != nil {
			logger.Error("email ingest failed", "message_uid", id, "error", ingestErr.Error())
			failedUIDs[id] = struct{}{}
			if _, err := session.command(ctx, fmt.Sprintf("UID STORE %d +FLAGS (\\Seen)", id)); err != nil {
				logger.Error("email acknowledgement failed", "message_uid", id, "error", err.Error())
			}
			continue
		}
		if err := sendEmail(ctx, cfg, from.Address, fmt.Sprintf("Re: [Заявка #%d] %s", result.TicketID, subject), result.TicketID, html.EscapeString(bodyText), result.ReplyText, result.AutoReplied); err != nil {
			LogOutgoing(logger, "email", from.Address, result.TicketID, "AI", err)
		} else {
			LogOutgoing(logger, "email", from.Address, result.TicketID, "AI", nil)
		}
		if _, err := session.command(ctx, fmt.Sprintf("UID STORE %d +FLAGS (\\Seen)", id)); err != nil {
			return fmt.Errorf("email acknowledgement failed: %w", err)
		}
	}
	return nil
}

// CheckEmailCredentials verifies both mailbox login and SMTP authentication
// before credentials are stored. Connections are TLS-only, matching workers.
func CheckEmailCredentials(ctx context.Context, imapHost, smtpHost, user, password string) error {
	session, err := connectIMAP(ctx, imapHost)
	if err != nil {
		return err
	}
	quote := func(value string) string { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"` }
	_, loginErr := session.command(ctx, "LOGIN "+quote(user)+" "+quote(password))
	session.cleanup()
	if loginErr != nil {
		return loginErr
	}
	conn, cleanup, err := dialMail(ctx, smtpHost)
	if err != nil {
		return err
	}
	defer cleanup()
	client, err := smtp.NewClient(conn, hostName(smtpHost))
	if err != nil {
		return err
	}
	defer client.Close()
	if err := client.Auth(smtp.PlainAuth("", user, password, hostName(smtpHost))); err != nil {
		return err
	}
	return nil
}

func isSystemEmailSender(address string) bool {
	parsed, err := mail.ParseAddress(address)
	if err == nil {
		address = parsed.Address
	}
	address = strings.ToLower(strings.TrimSpace(address))
	local, domain, ok := strings.Cut(address, "@")
	if !ok {
		local, domain = address, ""
	}
	if domain == "e.mail.ru" || domain == "corp.mail.ru" {
		return true
	}
	switch local {
	case "noreply", "no-reply", "mailer-daemon", "postmaster":
		return true
	default:
		return false
	}
}

func cleanEmailText(text string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, text)
}

// trimEmailQuote removes the most common reply separators before a user message
// is stored, preventing previously sent support templates from being duplicated.
func trimEmailQuote(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if trimmed == "---" || strings.HasPrefix(lower, "-----original message-----") ||
			strings.HasPrefix(lower, "----- forwarded message -----") ||
			strings.HasPrefix(lower, "от:") || strings.HasPrefix(lower, "from:") ||
			strings.HasPrefix(lower, "в ") && (strings.Contains(lower, "писал") || strings.Contains(lower, "wrote:")) ||
			strings.HasPrefix(lower, "on ") && strings.Contains(lower, "wrote:") || strings.HasPrefix(trimmed, ">") {
			return strings.TrimSpace(strings.Join(lines[:i], "\n"))
		}
	}
	return strings.TrimSpace(text)
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
	return decodeMIMEBodyDepth(contentType, transferEncoding, body, 0)
}

func decodeMIMEBodyDepth(contentType, transferEncoding string, body io.Reader, depth int) (string, error) {
	if depth > 16 {
		return "", fmt.Errorf("MIME nesting exceeds limits")
	}
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
			text, partErr := decodeMIMEBodyDepth(part.Header.Get("Content-Type"), part.Header.Get("Content-Transfer-Encoding"), part, depth+1)
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

func sendEmail(ctx context.Context, cfg Config, to, subject string, ticketID int, originalMessage, replyBody string, isAutoReply bool) error {
	address, err := mail.ParseAddress(to)
	if err != nil {
		return err
	}
	conn, cleanup, err := dialMail(ctx, cfg.MailSMTPHost)
	if err != nil {
		return err
	}
	defer cleanup()
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
	encodedSubject := mime.QEncoding.Encode("UTF-8", subject)
	htmlBody := FormatEmailHTML(ticketID, originalMessage, replyBody, isAutoReply)
	message := "From: " + from.String() + "\r\nTo: " + address.String() + "\r\nSubject: " + encodedSubject + "\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=\"UTF-8\"\r\nContent-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString([]byte(htmlBody)) + "\r\n"
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

// ReadSlice prevents an unterminated protocol line from allocating without limit.
func readIMAPLine(reader *bufio.Reader) (string, error) {
	var line []byte
	for {
		part, err := reader.ReadSlice('\n')
		if len(line)+len(part) > 64<<10 {
			return "", fmt.Errorf("IMAP line exceeds limits")
		}
		line = append(line, part...)
		if err == bufio.ErrBufferFull {
			continue
		}
		return string(line), err
	}
}
