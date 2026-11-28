package handlers

import (
	"encoding/json"
	"errors"
	"flowops-ai/internal/onec"
	"flowops-ai/internal/security"
	"github.com/jackc/pgx/v5"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// OneC has no generic proxy or write-to-1C operation. Only administrators with
// integration permission and organization scope may read external business data.
func (h *IntegrationsHandler) OneC(w http.ResponseWriter, r *http.Request) {
	p, ok := security.CurrentPrincipal(r.Context())
	if !ok {
		writeError(w, 401, "Необходим вход")
		return
	}
	if p.Role != "admin" || !security.HasPermission(p, "integrations_manage") {
		writeError(w, 403, "Недостаточно прав для настройки 1С")
		return
	}
	var scoped bool
	if err := h.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM organization_member_roles m JOIN organization_access_roles a ON a.tenant_id=m.tenant_id AND a.id=m.role_id WHERE m.tenant_id=$1 AND m.member_id=$2 AND a.scope<>'organization')`, p.TenantID, p.ID).Scan(&scoped); err != nil {
		writeError(w, 500, "Не удалось проверить область доступа")
		return
	}
	if scoped {
		writeError(w, 403, "Для подключения 1С нужен доступ ко всей организации")
		return
	}
	var cfg onec.Config
	var raw []byte
	var encrypted string
	err := h.db.QueryRow(r.Context(), `SELECT config,secret_ciphertext FROM onec_connections WHERE tenant_id=$1`, p.TenantID).Scan(&raw, &encrypted)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 500, "Не удалось загрузить подключение 1С")
		return
	}
	if len(raw) > 0 && json.Unmarshal(raw, &cfg) != nil {
		writeError(w, 500, "Повреждена конфигурация 1С")
		return
	}
	allowed := os.Getenv("FLOWOPS_1C_ALLOWED_URLS")
	key := os.Getenv("FLOWOPS_1C_SECRET_KEY")
	if r.Method == "PUT" {
		var input struct {
			onec.Config
			Secret      string `json:"secret"`
			ClearSecret bool   `json:"clear_secret"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		dec.DisallowUnknownFields()
		if dec.Decode(&input) != nil {
			writeError(w, 400, "Некорректная конфигурация 1С")
			return
		}
		input.BaseURL = strings.TrimRight(strings.TrimSpace(input.BaseURL), "/")
		if e := input.Config.Validate(allowed); e != nil {
			writeError(w, 400, e.Error())
			return
		}
		if len(input.Secret) > 4096 || strings.ContainsAny(input.Secret, "\r\n") {
			writeError(w, 400, "Некорректный секрет 1С")
			return
		}
		// Never forward an old password to a newly selected service or account.
		if input.ClearSecret || input.Auth == "none" || input.BaseURL != cfg.BaseURL || input.Auth != cfg.Auth || input.Username != cfg.Username {
			encrypted = ""
		}
		if input.Secret != "" {
			encrypted, err = onec.Seal(key, p.TenantID, input.Secret)
			if err != nil {
				writeError(w, 503, err.Error())
				return
			}
		}
		if input.Auth != "none" && encrypted == "" {
			writeError(w, 400, "Укажите пароль или токен для этого подключения")
			return
		}
		raw, _ = json.Marshal(input.Config)
		if _, err = h.db.Exec(r.Context(), `INSERT INTO onec_connections(tenant_id,config,secret_ciphertext) VALUES($1,$2,$3) ON CONFLICT(tenant_id) DO UPDATE SET config=excluded.config,secret_ciphertext=excluded.secret_ciphertext,updated_at=now()`, p.TenantID, raw, encrypted); err != nil {
			writeError(w, 500, "Не удалось сохранить подключение 1С")
			return
		}
		cfg = input.Config
	}
	if strings.HasSuffix(r.URL.Path, "/check") || strings.HasSuffix(r.URL.Path, "/data") {
		if cfg.BaseURL == "" {
			writeError(w, 400, "Сначала сохраните подключение 1С")
			return
		}
		secret := ""
		if encrypted != "" {
			secret, err = onec.Open(key, p.TenantID, encrypted)
			if err != nil {
				writeError(w, 503, err.Error())
				return
			}
		}
		resource := r.URL.Query().Get("resource")
		skip, limit := 0, 20
		check := strings.HasSuffix(r.URL.Path, "/check")
		if check {
			resource = cfg.HealthPath
			if cfg.Mode == "odata" {
				resource = "$metadata"
			}
			if resource == "" {
				writeError(w, 400, "Укажите путь проверки HTTP-сервиса")
				return
			}
			limit = 0
		} else {
			if q := r.URL.Query().Get("skip"); q != "" {
				skip, err = strconv.Atoi(q)
				if err != nil {
					skip = -1
				}
			}
			if q := r.URL.Query().Get("limit"); q != "" {
				limit, err = strconv.Atoi(q)
				if err != nil {
					limit = 0
				}
			}
			if skip < 0 || skip > 1000000 || limit < 1 || limit > 100 || resource == "$metadata" {
				writeError(w, 400, "Проверьте ресурс и размер страницы (1–100)")
				return
			}
		}
		data, e := cfg.Get(r.Context(), allowed, secret, resource, skip, limit)
		if e != nil {
			writeError(w, 502, e.Error())
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if check {
			sets := []string{}
			if cfg.Mode == "odata" {
				sets, e = onec.EntitySets(data)
				if e != nil {
					writeError(w, 502, e.Error())
					return
				}
			}
			writeJSON(w, 200, map[string]any{"ok": true, "resources": sets})
			return
		}
		writeJSON(w, 200, json.RawMessage(data))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"config": cfg, "has_secret": encrypted != "", "configured": cfg.BaseURL != "", "server_ready": allowed != ""})
}
