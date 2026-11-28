package settings

import (
	"errors"
	"strconv"
	"strings"
)

type RoutingRule struct {
	ID         string            `json:"id"`
	Order      int               `json:"order"`
	Match      map[string]string `json:"match"`
	Department string            `json:"department"`
	Priority   string            `json:"priority,omitempty"`
	Assignee   string            `json:"assignee,omitempty"`
}
type ScopedSLA struct {
	Calendar      *Calendar          `json:"calendar,omitempty"`
	Timezone      string             `json:"timezone,omitempty"`
	PauseStatuses *[]string          `json:"pause_statuses,omitempty"`
	Escalations   *[]EscalationLevel `json:"escalations,omitempty"`
	ID            string             `json:"id"`
	Match         map[string]string  `json:"match"`
	Policy        SLAPolicy          `json:"policy"`
}
type EscalationLevel struct {
	ID           string `json:"id"`
	MinutesAfter int    `json:"minutes_after"`
	Assignee     string `json:"assignee,omitempty"`
}
type ProcessSettings struct {
	PauseStatuses      []string          `json:"pause_statuses,omitempty"`
	WarningMinutes     int               `json:"warning_minutes"`
	Escalations        []EscalationLevel `json:"escalations,omitempty"`
	Routing            []RoutingRule     `json:"routing"`
	SLA                []ScopedSLA       `json:"sla"`
	FallbackDepartment string            `json:"fallback_department,omitempty"`
}

func validMatch(match map[string]string) bool {
	if len(match) > 10 {
		return false
	}
	for key, value := range match {
		switch key {
		case "form_id", "department", "priority", "channel", "category", "ticket_type", "requester_id", "requester_role", "requester_unit", "branch", "working_time", "max_open":
		default:
			if !strings.HasPrefix(key, "field.") || len(key) < 7 || len(key) > 106 {
				return false
			}
		}
		if key == "working_time" && value != "true" && value != "false" {
			return false
		}
		if key == "max_open" {
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 || n > 100000 {
				return false
			}
		}
		if value == "" || len(value) > 200 {
			return false
		}
	}
	return true
}
func (p ProcessSettings) Validate() error {
	if p.WarningMinutes < 0 || p.WarningMinutes > 10080 || len(p.Escalations) > 10 || len(p.PauseStatuses) > 10 {
		return errors.New("Некорректные параметры эскалации")
	}
	levels := map[string]bool{}
	for _, level := range p.Escalations {
		if !configID.MatchString(level.ID) || levels[level.ID] || level.MinutesAfter < 0 || level.MinutesAfter > 525600 {
			return errors.New("Некорректный уровень эскалации")
		}
		levels[level.ID] = true
	}
	for _, status := range p.PauseStatuses {
		if status != "pending_confirmation" && status != "awaiting_confirmation" && status != "waiting_employee" {
			return errors.New("Приостановка разрешена только при ожидании сотрудника")
		}
	}

	if len(p.Routing) > 200 || len(p.SLA) > 200 {
		return errors.New("Не более 200 правил каждого типа")
	}
	seen := map[string]bool{}
	for _, rule := range p.Routing {
		if !configID.MatchString(rule.ID) || seen[rule.ID] || rule.Department == "" || len(rule.Department) > 100 || !validMatch(rule.Match) || rule.Order < 0 || rule.Order > 10000 {
			return errors.New("Некорректное правило маршрутизации")
		}
		seen[rule.ID] = true
		if rule.Priority != "" && rule.Priority != "low" && rule.Priority != "medium" && rule.Priority != "high" {
			return errors.New("Некорректный приоритет правила")
		}
	}
	seen = map[string]bool{}
	for _, rule := range p.SLA {
		if rule.Calendar != nil || rule.Timezone != "" {
			calendar := DefaultPlatformConfig()
			calendar.Name = "SLA calendar"
			if rule.Calendar != nil {
				calendar.Calendar = *rule.Calendar
			}
			if rule.Timezone != "" {
				calendar.Timezone = rule.Timezone
			}
			if err := calendar.Validate(); err != nil {
				return err
			}
		}
		local := ProcessSettings{}
		if rule.PauseStatuses != nil {
			local.PauseStatuses = *rule.PauseStatuses
		}
		if rule.Escalations != nil {
			local.Escalations = *rule.Escalations
		}
		if err := local.Validate(); err != nil {
			return err
		}

		if !configID.MatchString(rule.ID) || seen[rule.ID] || !validMatch(rule.Match) || rule.Policy.FirstResponseMinutes < 0 || rule.Policy.FirstResponseMinutes > 525600 || rule.Policy.ResolutionMinutes < 0 || rule.Policy.ResolutionMinutes > 525600 || (rule.Policy.Clock != "calendar" && rule.Policy.Clock != "business") {
			return errors.New("Некорректная политика SLA")
		}
		seen[rule.ID] = true
	}
	return nil
}
