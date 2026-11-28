package analyzer

import (
	"bufio"
	"context"
	"encoding/json"
	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/security"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLiveYandexStructured(t *testing.T) {
	if os.Getenv("FLOWOPS_LIVE_YANDEX_TEST") != "true" {
		t.Skip("explicit live verification only")
	}
	file, err := os.Open(filepath.Join("..", "..", ".env"))
	if err != nil {
		t.Fatal("Live Yandex configuration unavailable")
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		key = strings.TrimSpace(key)
		if ok && (key == "YANDEX_API_KEY" || key == "YANDEX_FOLDER_ID" || key == "YANDEX_MODEL") {
			t.Setenv(key, strings.Trim(strings.TrimSpace(value), "\"'"))
		}
	}
	if scanner.Err() != nil {
		t.Fatal("Cannot read live configuration")
	}
	if os.Getenv("YANDEX_API_KEY") == "" || os.Getenv("YANDEX_FOLDER_ID") == "" {
		t.Fatal("YANDEX_API_KEY or YANDEX_FOLDER_ID is missing")
	}
	t.Setenv("FLOWOPS_AI_PROVIDERS", "yandex")
	base, _ := knowledge.Load(t.TempDir())
	a := New(base)
	content, used, err := a.GenerateStructured(context.Background(), "Return JSON object with boolean ok=true. No additional text.", "Public synthetic connectivity check.")
	if err != nil {
		t.Fatal("Live Yandex generation:", security.SafeLogError(err))
	}
	var result struct {
		OK bool `json:"ok"`
	}
	if !used || json.Unmarshal([]byte(content), &result) != nil || !result.OK {
		t.Fatal("Live Yandex returned invalid connectivity result")
	}
}
