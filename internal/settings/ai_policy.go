package settings

import (
	"errors"
	"strings"
)

type AIPolicy struct {
	DenyPersonalData    bool     `json:"deny_personal_data"`
	RequireConfirmation *bool    `json:"require_confirmation,omitempty"`
	Mode                string   `json:"mode"`
	Classification      *bool    `json:"classification,omitempty"`
	Routing             *bool    `json:"routing,omitempty"`
	Prioritization      *bool    `json:"prioritization,omitempty"`
	Answers             *bool    `json:"answers,omitempty"`
	DailyLimit          int      `json:"daily_limit"`
	ClarificationLimit  int      `json:"clarification_limit"`
	Sources             []string `json:"sources,omitempty"`
}

func (p AIPolicy) Validate() error {
	if p.Mode == "automatic" && Disabled(p.RequireConfirmation) && len(p.Sources) == 0 {
		return errors.New("Автоматическое завершение требует списка разрешённых источников")
	}
	if p.Mode != "" && p.Mode != "manual" && p.Mode != "assistant" && p.Mode != "partial" && p.Mode != "automatic" {
		return errors.New("Неизвестный режим ИИ")
	}
	if p.DailyLimit < 0 || p.DailyLimit > 100000 || p.ClarificationLimit < 0 || p.ClarificationLimit > 20 || len(p.Sources) > 200 {
		return errors.New("Некорректные лимиты ИИ")
	}
	for _, source := range p.Sources {
		if strings.TrimSpace(source) == "" || len(source) > 200 {
			return errors.New("Некорректный источник ИИ")
		}
	}
	return nil
}
func Disabled(value *bool) bool { return value != nil && !*value }
