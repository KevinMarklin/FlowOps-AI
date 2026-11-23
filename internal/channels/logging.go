package channels

import (
	"fmt"
	"net/mail"
	"regexp"
	"strings"

	"flowops-ai/internal/security"
)

var logSafeID = regexp.MustCompile(`^[0-9]{1,24}$`)
var logSafeDomain = regexp.MustCompile(`(?i)^[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?$`)

func DisplayChannel(channel, sender string) string {
	switch strings.ToLower(strings.TrimSpace(channel)) {
	case "email":
		return "Email"
	case "bot":
		if strings.HasPrefix(sender, "tg:") {
			return "Telegram"
		}
		if strings.HasPrefix(sender, "max:") || strings.HasPrefix(sender, "max_chat:") {
			return "MAX"
		}
		return "Web"
	default:
		return "Web"
	}
}

// SafeSender only emits platform numeric IDs or an email domain. Any
// unrecognized sender value is intentionally hidden.
func SafeSender(channel, sender string) string {
	if strings.EqualFold(channel, "bot") {
		for _, prefix := range []string{"tg:", "max:", "max_chat:"} {
			if strings.HasPrefix(sender, prefix) {
				id := strings.TrimPrefix(sender, prefix)
				if logSafeID.MatchString(id) {
					return prefix + id
				}
			}
		}
		return "unknown"
	}
	address, err := mail.ParseAddress(sender)
	if err == nil {
		_, domain, ok := strings.Cut(address.Address, "@")
		if ok && logSafeDomain.MatchString(domain) && !strings.Contains(domain, "..") {
			return "***@" + strings.ToLower(domain)
		}
	}
	return "***"
}

func LogIncoming(logger interface{ Info(string, ...any) }, channel, sender string) {
	logger.Info(fmt.Sprintf("[INCOMING] channel=%s sender=%s", DisplayChannel(channel, sender), SafeSender(channel, sender)))
}

func LogOutgoing(logger interface {
	Info(string, ...any)
	Error(string, ...any)
}, channel, sender string, ticketID int, source string, err error) {
	name := DisplayChannel(channel, sender)
	if err != nil {
		logger.Error(fmt.Sprintf("[OUTGOING] Failed to send reply via %s (ticket=#%d): %s", name, ticketID, security.SafeLogError(err)))
		return
	}
	recipient := SafeSender(channel, sender)
	logger.Info(fmt.Sprintf("[OUTGOING] Reply sent via %s (to=%s, ticket=#%d, source=%s)", name, recipient, ticketID, source))
}
