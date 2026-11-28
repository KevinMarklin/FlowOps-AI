package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flowops-ai/internal/models"
	"net/http"
	"path/filepath"
	"strings"
)

// Files are validated before analysis and inserted atomically with their ticket.
func prepareTicketUploads(form TicketFormDefinition, request *models.CreateTicketRequest) ([]byte, error) {
	if len(request.Attachments) > 20 {
		return nil, errors.New("Не более 20 вложений")
	}
	if request.Fields == nil {
		request.Fields = map[string]any{}
	}
	items := []map[string]any{}
	total := 0
	for fieldID, upload := range request.Attachments {
		found := false
		for _, field := range form.Fields {
			if field.ID == fieldID && field.Type == "attachment" {
				found = true
			}
		}
		if !found {
			return nil, errors.New("Неизвестное поле вложения")
		}
		total += len(upload.Content)
		kind := http.DetectContentType(upload.Content)
		name := filepath.Base(strings.ReplaceAll(upload.Filename, "\\", "/"))
		if len(upload.Content) == 0 || total > 5<<20 || !hasValue(kind, "application/pdf", "image/png", "image/jpeg", "text/plain; charset=utf-8") || name == "" || name == "." || len([]rune(name)) > 180 || strings.ContainsAny(name, "\r\n") {
			return nil, errors.New("Допустимы PDF, PNG, JPEG и текст; общий размер до 5 МБ")
		}
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, err
		}
		id := hex.EncodeToString(random[:])
		request.Fields[fieldID] = id
		items = append(items, map[string]any{"id": id, "filename": name, "mime": kind, "content": upload.Content})
	}
	for _, field := range form.Fields {
		if field.Type == "attachment" {
			if _, supplied := request.Fields[field.ID]; supplied {
				if _, uploaded := request.Attachments[field.ID]; !uploaded {
					return nil, errors.New("Вложение должно быть передано с обращением")
				}
			}
		}
	}
	return json.Marshal(items)
}
