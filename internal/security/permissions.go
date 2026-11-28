package security

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

var PermissionNames = []string{"ticket_view", "ticket_create", "ticket_edit", "ticket_assign", "ticket_status", "ticket_close", "analytics_view", "data_export", "members_manage", "units_manage", "knowledge_manage", "knowledge_approve", "automation_manage", "ai_configure", "audit_view", "integrations_manage", "roles_manage", "settings_manage"}

func ValidPermission(value string) bool {
	for _, p := range PermissionNames {
		if value == p {
			return true
		}
	}
	return false
}
func requiredPermissions(w http.ResponseWriter, r *http.Request) ([]string, bool) {
	path := r.URL.Path
	method := r.Method
	result := []string{}
	if path == "/api/v1/auth/me" || strings.HasPrefix(path, "/api/v1/profile/") || path == "/api/v1/organization" || path == "/api/v1/organization/directory" || path == "/api/v1/organization/ticket-form" && method == "GET" {
		return result, true
	}
	switch {
	case path == "/api/tickets":
		if method == "POST" {
			result = append(result, "ticket_create")
		} else {
			result = append(result, "ticket_view")
		}
	case strings.HasPrefix(path, "/api/tickets/"):
		if method == "GET" || strings.HasSuffix(path, "/read") || strings.HasSuffix(path, "/rating") || strings.HasSuffix(path, "/queue/clear") {
			result = append(result, "ticket_view")
		} else {
			result = append(result, "ticket_edit")
		}
		if strings.HasSuffix(path, "/decision") || strings.HasSuffix(path, "/feedback") {
			result = []string{"ticket_close"}
		}
		if method == "PATCH" && len(strings.Split(strings.TrimPrefix(path, "/api/tickets/"), "/")) == 1 {
			body, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
			if err != nil || len(body) > 1<<20 {
				ingestError(w, 413, "Запрос слишком большой")
				return nil, false
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			result = nil
			var patch map[string]json.RawMessage
			if json.Unmarshal(body, &patch) != nil {
				ingestError(w, 400, "Некорректный запрос")
				return nil, false
			}
			if raw, ok := patch["status"]; ok {
				var status string
				json.Unmarshal(raw, &status)
				if status == "closed" || status == "auto_resolved" {
					result = append(result, "ticket_close")
				} else {
					result = append(result, "ticket_status")
				}
			}
			if _, ok := patch["department"]; ok {
				result = append(result, "ticket_assign")
			}
			if _, ok := patch["workflow_status"]; ok {
				result = append(result, "ticket_status")
			}
			if _, ok := patch["assignee_id"]; ok {
				result = append(result, "ticket_assign")
			}
			for key := range patch {
				if key != "status" && key != "department" && key != "assignee_id" && key != "closure_reason" && key != "workflow_status" {
					result = append(result, "ticket_edit")
					break
				}
			}
		}
	case strings.HasPrefix(path, "/api/analytics") || path == "/api/stats":
		result = append(result, "analytics_view")
		if r.URL.Query().Get("format") != "" {
			result = append(result, "data_export")
		}
	case strings.HasPrefix(path, "/api/v1/members"):
		result = append(result, "members_manage")
	case strings.HasPrefix(path, "/api/v1/organization/roles"):
		result = append(result, "roles_manage")
	case strings.HasPrefix(path, "/api/v1/organization/units"):
		result = append(result, "units_manage")
	case strings.HasPrefix(path, "/api/knowledge") || strings.HasPrefix(path, "/api/v1/kb/"):
		result = append(result, "knowledge_manage")
		if method == "PUT" && strings.HasPrefix(path, "/api/v1/kb/articles/") {
			body, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
			if err != nil || len(body) > 1<<20 {
				ingestError(w, 413, "Запрос слишком большой")
				return nil, false
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			var action struct {
				Action string `json:"action"`
			}
			json.Unmarshal(body, &action)
			if action.Action == "publish" || action.Action == "request_changes" {
				result = append(result, "knowledge_approve")
			}
		}
	case strings.HasPrefix(path, "/api/optimization"):
		result = append(result, "automation_manage")
	case strings.Contains(path, "/audit"):
		result = append(result, "audit_view")
	case path == "/api/v1/settings/organization":
		// Section-level checks compare the submitted document with persisted values.
	case path == "/api/v1/organization/ticket-form":
		result = append(result, "automation_manage")
	case strings.Contains(path, "/widget/") || strings.HasPrefix(path, "/api/v1/settings") || strings.Contains(path, "/integrations") || strings.Contains(path, "/keys") || strings.Contains(path, "/channels"):
		result = append(result, "integrations_manage")
	case path == "/api/v1/organization/directory":
		result = append(result, "ticket_view")
	case path == "/api/v1/intake/preview":
		result = append(result, "ticket_create")
	case path == "/api/v1/notifications/sla":
		result = append(result, "ticket_view")
	case path == "/api/health":
	default:
		result = append(result, "settings_manage")
	}
	return result, true
}

// HasPermission supplements the base-role checks; nil means the standard role.
func HasPermission(p Principal, permission string) bool {
	if p.Permissions == nil {
		return true
	}
	for _, value := range *p.Permissions {
		if value == permission {
			return true
		}
	}
	return false
}
