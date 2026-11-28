package analyzer

import (
	"context"
	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/settings"
	"os"
	"strings"
)

func newLegacyAnalyzer(base *knowledge.Base) *Analyzer {
	a := New(base)
	a.providerLoader = a.legacyTestProviders
	a.setProviderForConfig()
	return a
}
func (a *Analyzer) legacyTestProviders() []approvedProvider {
	var providers []approvedProvider
	if providerAllowed("yandex") && strings.TrimSpace(os.Getenv("YANDEX_API_KEY")) != "" {
		providers = append(providers, approvedProvider{"Alice AI (Yandex AI Studio)", func() string { _, model, _ := yandexConfig(); return model }, a.analyzeWithYandex})
	}
	giga := loadGigaChatConfig()
	if giga.authKey != "" && !giga.insecure && providerAllowed("gigachat") {
		providers = append(providers, approvedProvider{"GigaChat (РФ контур)", func() string { return giga.model }, a.analyzeWithGigaChat})
	}
	if key := googleAPIKey(); key != "" && providerAllowed("gemini") {
		providers = append(providers, approvedProvider{"Gemini", a.currentGoogleModel, func(ctx context.Context, text string, config settings.BuilderConfig) (Result, error) {
			return a.analyzeWithGoogle(ctx, text, key, config)
		}})
	} else if key == "" && os.Getenv("AI_API_KEY") != "" && providerAllowed("compatible") {
		providers = append(providers, approvedProvider{"AI API", func() string { _, model := aiConfig(); return model }, a.analyzeWithAI})
	}
	return providers
}
