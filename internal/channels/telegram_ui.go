package channels

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Owned by one polling worker. Operator notifications deliberately bypass it.
// The bounded cache retains no ticket text or credentials.
type telegramUI struct {
	token string
	chats map[int64]telegramUIScreen
}
type telegramUIScreen struct {
	id      int64
	touched time.Time
}

func newTelegramUI(token string) *telegramUI {
	return &telegramUI{token: token, chats: make(map[int64]telegramUIScreen)}
}

func (ui *telegramUI) command(ctx context.Context, method string, value any) error {
	body, ok := value.(map[string]any)
	if !ok {
		return telegramCommand(ctx, ui.token, method, value)
	}
	if keep, _ := body["keep_history"].(bool); keep {
		delete(body, "keep_history")
		return telegramCommand(ctx, ui.token, method, body)
	}
	chatID, _ := body["chat_id"].(int64)
	if method != "sendMessage" && method != "editMessageText" && method != "deleteMessage" {
		return telegramCommand(ctx, ui.token, method, body)
	}
	if method == "sendMessage" {
		if screen, exists := ui.chats[chatID]; exists && time.Since(screen.touched) < 24*time.Hour {
			body["message_id"] = screen.id
			method = "editMessageText"
		}
	}
	id, err := telegramCommandResult(ctx, ui.token, method, body)
	var apiErr *telegramAPIError
	if errors.As(err, &apiErr) && apiErr.code == 400 {
		description := strings.ToLower(apiErr.description)
		if method == "editMessageText" && strings.Contains(description, "message is not modified") {
			err = nil
		}
		if method == "editMessageText" && (strings.Contains(description, "message to edit not found") || strings.Contains(description, "message can't be edited")) {
			delete(body, "message_id")
			id, err = telegramCommandResult(ctx, ui.token, "sendMessage", body)
		}
		if method == "deleteMessage" && strings.Contains(description, "message to delete not found") {
			err = nil
		}
		// Telegram can refuse to delete notifications older than 48 hours.
		// Remove their text and keyboard instead; the database reply is untouched.
		if method == "deleteMessage" && strings.Contains(description, "message can't be deleted") {
			_, err = telegramCommandResult(ctx, ui.token, "editMessageText", map[string]any{"chat_id": chatID, "message_id": body["message_id"], "text": "✅ Прочитано", "reply_markup": map[string]any{"inline_keyboard": [][]telegramButton{}}})
		}
	}
	if err != nil {
		return err
	}
	if method == "deleteMessage" {
		if ui.chats[chatID].id == body["message_id"] {
			delete(ui.chats, chatID)
		}
		return nil
	}
	if id == 0 {
		id, _ = body["message_id"].(int64)
	}
	if id > 0 {
		if len(ui.chats) >= 4096 { // Evict the least recently used chat, keeping memory bounded.
			var oldest int64
			var touched time.Time
			for chat, screen := range ui.chats {
				if touched.IsZero() || screen.touched.Before(touched) {
					oldest, touched = chat, screen.touched
				}
			}
			delete(ui.chats, oldest)
		}
		ui.chats[chatID] = telegramUIScreen{id: id, touched: time.Now()}
	}
	return nil
}
