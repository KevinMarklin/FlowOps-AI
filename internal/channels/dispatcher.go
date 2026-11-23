package channels

import (
	"context"
	"fmt"
	"net/mail"
	"strconv"
	"strings"
)

// Dispatcher routes operator replies back to the originating channel.
type Dispatcher struct{ config Config }

func NewDispatcher(config Config) *Dispatcher { return &Dispatcher{config: config} }

func (d *Dispatcher) Dispatch(ctx context.Context, channel, sender string, ticketID int, reply string) error {
	switch channel {
	case "bot":
		if strings.HasPrefix(sender, "tg:") {
			if strings.TrimSpace(d.config.TelegramToken) == "" {
				return fmt.Errorf("Telegram channel disabled")
			}
			id, err := strconv.ParseInt(strings.TrimPrefix(sender, "tg:"), 10, 64)
			if err != nil {
				return fmt.Errorf("invalid Telegram chat id: %w", err)
			}
			return sendTelegramMessage(ctx, d.config.TelegramToken, id, reply)
		}
		if strings.HasPrefix(sender, "max:") {
			if strings.TrimSpace(d.config.MaxToken) == "" {
				return fmt.Errorf("MAX channel disabled")
			}
			return sendMaxMessage(ctx, d.config, "user_id", strings.TrimPrefix(sender, "max:"), reply)
		}
		if strings.HasPrefix(sender, "max_chat:") {
			if strings.TrimSpace(d.config.MaxToken) == "" {
				return fmt.Errorf("MAX channel disabled")
			}
			return sendMaxMessage(ctx, d.config, "chat_id", strings.TrimPrefix(sender, "max_chat:"), reply)
		}
	case "email":
		if !d.config.emailEnabled() {
			return fmt.Errorf("email channel disabled")
		}
		address, err := mail.ParseAddress(sender)
		if err != nil {
			return fmt.Errorf("invalid email recipient")
		}
		return sendEmail(ctx, d.config, address.Address, fmt.Sprintf("Re: [Заявка #%d] Ответ службы поддержки FlowOps AI", ticketID), reply)
	}
	return fmt.Errorf("unsupported delivery channel or recipient")
}

func (d *Dispatcher) EnabledChannels() []string {
	active := []string{"Web"}
	if strings.TrimSpace(d.config.TelegramToken) != "" {
		active = append(active, "Telegram")
	}
	if strings.TrimSpace(d.config.MaxToken) != "" {
		active = append(active, "MAX")
	}
	if d.config.emailEnabled() {
		active = append(active, "Email")
	}
	return active
}
