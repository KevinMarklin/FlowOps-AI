package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"flowops-ai/internal/workflows"
	"strings"
)

func validateWorkflowFields(d workflows.Definition, raw []byte, allForms bool) error {
	var form TicketFormDefinition
	if len(raw) > 0 && json.Unmarshal(raw, &form) != nil {
		return errors.New("Форма недоступна")
	}
	fields := map[string]bool{}
	for _, f := range form.Fields {
		fields[f.ID] = true
	}
	if allForms {
		for _, variant := range form.Forms {
			for _, f := range variant.Fields {
				fields[f.ID] = true
			}
		}
	}
	for _, node := range d.Nodes {
		for _, key := range node.Required {
			if !fields[key] {
				return errors.New("Обязательное поле шага отсутствует в форме обращения")
			}
		}
		for key := range node.If {
			if strings.HasPrefix(key, "field.") && !fields[strings.TrimPrefix(key, "field.")] {
				return errors.New("Поле условия отсутствует в форме обращения")
			}
		}
	}
	return nil
}
func workflowForm(ctx context.Context, query rowQuerier, tenant string, ticket int) ([]byte, error) {
	var raw []byte
	var err error
	if ticket > 0 {
		err = query.QueryRow(ctx, `SELECT COALESCE(NULLIF(form_snapshot,'{}'),(SELECT definition FROM organization_ticket_forms WHERE tenant_id=$1),'{}') FROM tickets WHERE tenant_id=$1 AND id=$2`, tenant, ticket).Scan(&raw)
	} else {
		err = query.QueryRow(ctx, `SELECT COALESCE((SELECT definition FROM organization_ticket_forms WHERE tenant_id=$1),'{}')`, tenant).Scan(&raw)
	}
	return raw, err
}
