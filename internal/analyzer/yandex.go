package analyzer

import (
	"context"
	"fmt"
	"os"
	"strings"

	"flowops-ai/internal/settings"
)

const yandexBaseURL = "https://ai.api.cloud.yandex.net/v1"

func yandexConfig() (folder, model string, err error) {
	folder = strings.TrimSpace(os.Getenv("YANDEX_FOLDER_ID"))
	model = strings.TrimSpace(os.Getenv("YANDEX_MODEL"))
	if folder == "" || strings.ContainsAny(folder, "/\r\n\t ") {
		return "", "", fmt.Errorf("YANDEX_FOLDER_ID must contain a folder ID")
	}
	if model == "" {
		model = "aliceai-llm"
	}
	if !strings.HasPrefix(model, "gpt://") {
		model = "gpt://" + folder + "/" + model
	}
	if !strings.HasPrefix(model, "gpt://"+folder+"/") || strings.ContainsAny(model, "\r\n\t ") || strings.TrimPrefix(model, "gpt://"+folder+"/") == "" {
		return "", "", fmt.Errorf("YANDEX_MODEL must belong to YANDEX_FOLDER_ID")
	}
	return folder, model, nil
}

func (a *Analyzer) analyzeWithYandex(ctx context.Context, text string, config settings.BuilderConfig) (Result, error) {
	folder, model, err := yandexConfig()
	if err != nil {
		return Result{}, err
	}
	return a.analyzeWithCompatible(ctx, text, config, yandexBaseURL, model, strings.TrimSpace(os.Getenv("YANDEX_API_KEY")), folder)
}
