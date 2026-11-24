package settings

import (
	"context"
	"encoding/json"
	"strings"
)

type Department struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type BuilderConfig struct {
	Departments     []Department `json:"departments"`
	Tone            string       `json:"tone"`
	StartTemplate   string       `json:"start_template"`
	HandoffTemplate string       `json:"handoff_template"`
	Signature       string       `json:"signature"`
}

type Loader func(context.Context) (BuilderConfig, error)

const defaultStartTemplate = "Здравствуйте! Я ИИ-помощник службы поддержки FlowOps AI. Опишите вашу проблему — я сразу найду решение по регламенту или передам заявку профильному специалисту."

func DefaultBuilderConfig() BuilderConfig {
	return BuilderConfig{
		Departments: []Department{
			{Name: "IT", Description: "ИТ-поддержка"},
			{Name: "Billing", Description: "Платежи, счета и тарифы"},
			{Name: "HR", Description: "Кадровые вопросы"},
			{Name: "Accounting", Description: "Бухгалтерия"},
			{Name: "Facilities", Description: "Офис и АХО"},
		},
		Tone:            "friendly",
		StartTemplate:   defaultStartTemplate,
		HandoffTemplate: "Заявка #{ticket_id} направлена в отдел {department}.",
		Signature:       "Служба поддержки FlowOps",
	}
}

// LoadBuilderConfig reads the current constructor settings on every call. It
// deliberately has no process-level cache so PUT changes take effect at once.
func LoadBuilderConfig(ctx context.Context, load func(context.Context) ([]byte, error)) (BuilderConfig, error) {
	raw, err := load(ctx)
	if err != nil {
		return BuilderConfig{}, err
	}

	config := DefaultBuilderConfig()
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "{}" {
		return config, nil
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return BuilderConfig{}, err
	}
	defaults := DefaultBuilderConfig()
	var provided map[string]json.RawMessage
	if err := json.Unmarshal(raw, &provided); err != nil {
		return BuilderConfig{}, err
	}
	if config.Departments == nil {
		config.Departments = defaults.Departments
	}
	if strings.TrimSpace(config.Tone) == "" {
		config.Tone = defaults.Tone
	}
	if strings.TrimSpace(config.StartTemplate) == "" {
		config.StartTemplate = defaults.StartTemplate
	}
	if _, exists := provided["handoff_template"]; !exists || strings.TrimSpace(config.HandoffTemplate) == "" {
		config.HandoffTemplate = defaults.HandoffTemplate
	}
	if _, exists := provided["signature"]; !exists {
		config.Signature = defaults.Signature
	}
	return config, nil
}

func LoadOrDefault(ctx context.Context, loader Loader) BuilderConfig {
	if loader == nil {
		return DefaultBuilderConfig()
	}
	config, err := loader(ctx)
	if err != nil {
		return DefaultBuilderConfig()
	}
	return config
}
