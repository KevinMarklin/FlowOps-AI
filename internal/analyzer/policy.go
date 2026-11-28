package analyzer

import (
	"context"
	"os"
	"regexp"
	"strings"

	"flowops-ai/internal/security"
	"flowops-ai/internal/settings"
)

type approvedProvider struct {
	name    string
	model   func() string
	analyze func(context.Context, string, settings.BuilderConfig) (Result, error)
}

// API keys are credentials, not authorization to export corporate data.
// Empty/invalid provider policies fail closed into local analysis.
func providerAllowed(name string) bool {
	allowed := false
	for _, value := range strings.Split(os.Getenv("FLOWOPS_AI_PROVIDERS"), ",") {
		value = strings.TrimSpace(value)
		switch value {
		case "yandex", "gigachat", "gemini", "compatible":
		default:
			return false
		}
		if value == name {
			allowed = true
		}
	}
	return allowed
}

func (a *Analyzer) approvedProviders() []approvedProvider {
	if a.providerLoader != nil {
		return a.providerLoader()
	}
	if !providerAllowed("yandex") || strings.TrimSpace(os.Getenv("YANDEX_API_KEY")) == "" {
		return nil
	}
	if _, _, err := yandexConfig(); err != nil {
		return nil
	}
	return []approvedProvider{{"Yandex AI Studio", func() string { _, model, _ := yandexConfig(); return model }, a.analyzeWithYandex}}
}

var sensitiveTopic = regexp.MustCompile(`(?i)(зарплат|оклад|выплат|плат[её]ж|оплат|финанс|банков|паспорт|снилс|персональн|сч[её]т|salary|payroll|payment|bank|account|passport|medical)`)

func sensitiveModelInputAllowed(text string, masked security.MaskResult) bool {
	// This is conservative triage, not proof of anonymization. Unknown PII can
	// remain; any external processing requires an explicit approved provider.
	return os.Getenv("FLOWOPS_AI_ALLOW_SENSITIVE") == "true" || (masked.MaskedCount == 0 && !sensitiveTopic.MatchString(text))
}

// ExternalModelInputAllowed checks original input before masking loses the
// evidence that personal data was present. Provider approval remains separate.
func ExternalModelInputAllowed(text string) bool {
	return sensitiveModelInputAllowed(text, security.MaskPII(text))
}
