package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"flowops-ai/internal/security"
	"flowops-ai/internal/settings"
	"github.com/jackc/pgx/v5"
	"strings"
)

func (h *IntegrationsHandler) validateProcessReferences(ctx context.Context, c settings.PlatformConfig) error {
	tenant := security.TenantID(ctx)
	builder, err := h.LoadBuilderConfig(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		builder = settings.DefaultBuilderConfig()
	} else if err != nil {
		return err
	}
	departments := map[string]bool{"General": true, "Unassigned": true}
	for _, d := range builder.Departments {
		departments[d.Name] = true
	}
	if c.Process.FallbackDepartment != "" && !departments[c.Process.FallbackDepartment] {
		return errors.New("Резервный отдел не найден")
	}
	var form TicketFormDefinition
	var raw []byte
	formErr := h.db.QueryRow(ctx, `SELECT definition FROM organization_ticket_forms WHERE tenant_id=$1`, tenant).Scan(&raw)
	if formErr != nil && !errors.Is(formErr, pgx.ErrNoRows) {
		return formErr
	}
	if len(raw) > 0 && json.Unmarshal(raw, &form) != nil {
		return errors.New("Форма повреждена")
	}
	allFields := append([]CustomField{}, form.Fields...)
	for _, variant := range form.Forms {
		allFields = append(allFields, variant.Fields...)
	}
	checkMatch := func(match map[string]string) error {
		for key, value := range match {
			switch key {
			case "form_id":
				found := false
				for _, variant := range form.Forms {
					if variant.ID == value {
						found = true
					}
				}
				if !found {
					return errors.New("Форма условия не найдена")
				}
			case "department":
				if !departments[value] {
					return errors.New("Отдел условия не найден")
				}
			case "priority":
				if !hasValue(value, "low", "medium", "high") {
					return errors.New("Неизвестный приоритет условия")
				}
			case "category":
				if !taxonomyContains(form.Categories, value) {
					return errors.New("Категория условия не найдена")
				}
			case "ticket_type":
				if !taxonomyContains(form.Types, value) {
					return errors.New("Тип условия не найден")
				}
			case "requester_role":
				if !hasValue(value, "admin", "operator", "employee", "analyst") {
					return errors.New("Неизвестная роль условия")
				}
			case "channel":
				if !hasValue(value, "web", "telegram", "max", "email", "api", "bot") {
					return errors.New("Неизвестный канал условия")
				}
			case "requester_unit", "branch":
				var found bool
				if err := h.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM organization_units WHERE tenant_id=$1 AND id=$2 AND active)`, tenant, value).Scan(&found); err != nil {
					return err
				}
				if !found {
					return errors.New("Подразделение условия недоступно")
				}
			case "requester_id":
				var found bool
				if err := h.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM workspace_members WHERE tenant_id=$1 AND id=$2 AND is_active)`, tenant, value).Scan(&found); err != nil {
					return err
				}
				if !found {
					return errors.New("Заявитель условия недоступен")
				}
			default:
				if strings.HasPrefix(key, "field.") {
					found := false
					for _, field := range allFields {
						if field.ID == strings.TrimPrefix(key, "field.") {
							found = true
							if hasValue(field.Type, "select", "multi") && !hasValue(value, field.Options...) {
								return errors.New("Значение поля условия не найдено")
							}
						}
					}
					if !found {
						return errors.New("Поле условия не найдено")
					}
				}
			}
		}
		return nil
	}
	for _, rule := range c.Process.Routing {
		if err = checkMatch(rule.Match); err != nil {
			return err
		}
	}
	for _, rule := range c.Process.SLA {
		if err = checkMatch(rule.Match); err != nil {
			return err
		}
	}
	for _, rule := range c.Process.Routing {
		if !departments[rule.Department] {
			return errors.New("Отдел правила не найден")
		}
	}
	checkMember := func(id string) error {
		if id == "" {
			return nil
		}
		var found bool
		if err := h.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM workspace_members WHERE tenant_id=$1 AND id=$2 AND is_active AND role IN ('admin','operator'))`, tenant, id).Scan(&found); err != nil {
			return err
		}
		if !found {
			return errors.New("Исполнитель не найден в организации")
		}
		return nil
	}
	for _, rule := range c.Process.Routing {
		if err = checkMember(rule.Assignee); err != nil {
			return err
		}
	}
	for _, level := range c.Process.Escalations {
		if err = checkMember(level.Assignee); err != nil {
			return err
		}
	}
	for _, rule := range c.Process.SLA {
		if rule.Escalations != nil {
			for _, level := range *rule.Escalations {
				if err = checkMember(level.Assignee); err != nil {
					return err
				}
			}
		}
	}
	for _, source := range c.AI.Sources {
		if !strings.HasPrefix(source, "kb-managed-") || !strings.HasSuffix(source, ".md") {
			return errors.New("Выберите источник из опубликованных регламентов")
		}
		id := strings.TrimSuffix(strings.TrimPrefix(source, "kb-managed-"), ".md")
		var found bool
		if err = h.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM kb_articles WHERE tenant_id=$1 AND id=$2 AND published_revision IS NOT NULL AND state NOT IN ('archived','stale'))`, tenant, id).Scan(&found); err != nil {
			return err
		}
		if !found {
			return errors.New("Источник недоступен или не опубликован")
		}
	}
	return nil
}
