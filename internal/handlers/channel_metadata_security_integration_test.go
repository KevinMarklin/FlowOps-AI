package handlers

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"flowops-ai/internal/database"
	"flowops-ai/internal/security"
)

func TestChannelMetadataHidesAndMigratesOldTelegramTokens(t *testing.T) {
	db := replyTestDB(t)
	secret := "123456789:CANARY_TelegramSecret_abcdefghijklmnopqrstuvwxyz"
	metadata := map[string]any{"telegram": map[string]string{"last_error": "Get https://api.telegram.org/bot" + secret + "/getUpdates: network offline", "bot_username": "fixture_bot", "status": "error"}}
	raw, _ := json.Marshal(metadata)
	ctx := security.WithPrincipal(context.Background(), security.Principal{ID: "admin", TenantID: "default", Role: "admin"})
	if _, err := db.Exec(ctx, `UPDATE app_settings SET channel_metadata=$1 WHERE tenant_id='default'`, raw); err != nil {
		t.Fatal(err)
	}
	h := NewIntegrationsHandler(db)
	w := httptest.NewRecorder()
	h.ChannelSettings(w, httptest.NewRequest("GET", "/api/v1/settings/channels", nil).WithContext(ctx))
	if w.Code != 200 || strings.Contains(w.Body.String(), secret) || !strings.Contains(w.Body.String(), "fixture_bot") {
		t.Fatal("GET exposed a stored token or lost bot identity")
	}
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var cleaned string
	if err := db.QueryRow(ctx, `SELECT channel_metadata::text FROM app_settings WHERE tenant_id='default'`).Scan(&cleaned); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cleaned, secret) || !strings.Contains(cleaned, "fixture_bot") {
		t.Fatal("migration did not scrub existing secret safely")
	}
}
