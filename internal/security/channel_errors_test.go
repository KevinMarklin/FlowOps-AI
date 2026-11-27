package security

import (
	"errors"
	"strings"
	"testing"
)

func TestChannelErrorRedactsTelegramURLCredential(t *testing.T) {
	secret := "123456789:CANARY_TelegramSecret_abcdefghijklmnopqrstuvwxyz"
	err := errors.New(`Get "https://api.telegram.org/bot` + secret + `/getUpdates?timeout=30": connection refused`)
	if sanitized := SafeLogError(err); strings.Contains(sanitized, secret) {
		t.Fatal("Telegram token leaked from network error")
	}
}
