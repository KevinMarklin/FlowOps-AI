package security

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"errors"
	"github.com/jackc/pgx/v5/pgconn"
)

type member struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Email         string    `json:"email"`
	Role          string    `json:"role"`
	Active        bool      `json:"active"`
	CreatedAt     time.Time `json:"created_at"`
	PasswordReady bool      `json:"password_ready"`
}

func (h *WorkspaceHandler) Members(w http.ResponseWriter, r *http.Request) {
	p, ok := CurrentPrincipal(r.Context())
	if !ok || p.Role != "admin" {
		ingestError(w, 403, "Только администратор может управлять доступами")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		rows, err := h.db.Query(r.Context(), `SELECT id,display_name,email,role,is_active,created_at,password_hash<>'' FROM workspace_members WHERE tenant_id=$1 ORDER BY created_at,id`, p.TenantID)
		if err != nil {
			ingestError(w, 500, "Не удалось загрузить пользователей")
			return
		}
		defer rows.Close()
		members := []member{}
		for rows.Next() {
			var m member
			if rows.Scan(&m.ID, &m.Name, &m.Email, &m.Role, &m.Active, &m.CreatedAt, &m.PasswordReady) != nil {
				ingestError(w, 500, "Не удалось прочитать пользователей")
				return
			}
			members = append(members, m)
		}
		if rows.Err() != nil {
			ingestError(w, 500, "Не удалось прочитать пользователей")
			return
		}
		roleJSON(w, 200, members)
		return
	}
	var input struct {
		Name  string `json:"name"`
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil {
		ingestError(w, 400, "Некорректные данные пользователя")
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	address, err := mail.ParseAddress(input.Email)
	if err != nil || address.Address != input.Email || len(input.Email) > 254 || input.Name == "" || len([]rune(input.Name)) > 120 || !ValidRole(input.Role) {
		ingestError(w, 400, "Укажите имя, email и допустимую роль")
		return
	}
	secret := make([]byte, 32)
	identity := make([]byte, 16)
	if _, err = rand.Read(secret); err != nil {
		ingestError(w, 500, "Не удалось создать доступ")
		return
	}
	if _, err = rand.Read(identity); err != nil {
		ingestError(w, 500, "Не удалось создать пользователя")
		return
	}
	token := "usr_" + base64.RawURLEncoding.EncodeToString(secret)
	sum := sha256.Sum256([]byte(token))
	id := "usr_" + hex.EncodeToString(identity)
	tx, err := h.db.Begin(r.Context())
	if err != nil {
		ingestError(w, 500, "Не удалось создать пользователя")
		return
	}
	defer tx.Rollback(r.Context())
	_, err = tx.Exec(r.Context(), `INSERT INTO workspace_members(id,tenant_id,display_name,email,role,token_hash) VALUES($1,$2,$3,$4,$5,$6)`, id, p.TenantID, input.Name, input.Email, input.Role, hex.EncodeToString(sum[:]))
	if err != nil {
		var pgerr *pgconn.PgError
		if errors.As(err, &pgerr) && pgerr.Code == "23505" {
			ingestError(w, 409, "Пользователь с таким email уже существует")
		} else {
			ingestError(w, 500, "Не удалось создать пользователя")
		}
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO audit_logs(tenant_id,event_type,actor,details) VALUES($1,'MEMBER_CREATED',$2,$3)`, p.TenantID, p.ID, "member="+id+"; role="+input.Role); err != nil {
		ingestError(w, 500, "Не удалось записать аудит")
		return
	}
	if tx.Commit(r.Context()) != nil {
		ingestError(w, 500, "Не удалось сохранить пользователя")
		return
	}
	roleJSON(w, 201, map[string]any{"id": id, "name": input.Name, "email": input.Email, "role": input.Role, "token": token})
}

func (h *WorkspaceHandler) UpdateMember(w http.ResponseWriter, r *http.Request) {
	p, ok := CurrentPrincipal(r.Context())
	if !ok || p.Role != "admin" {
		ingestError(w, 403, "Только администратор может управлять доступами")
		return
	}
	id := r.PathValue("id")
	if id == p.ID {
		ingestError(w, 400, "Собственный доступ изменять нельзя")
		return
	}
	var input struct {
		Role   *string `json:"role"`
		Active *bool   `json:"active"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || (input.Role == nil && input.Active == nil) || (input.Role != nil && !ValidRole(*input.Role)) {
		ingestError(w, 400, "Укажите роль или состояние доступа")
		return
	}
	tx, err := h.db.Begin(r.Context())
	if err != nil {
		ingestError(w, 500, "Не удалось обновить доступ")
		return
	}
	defer tx.Rollback(r.Context())
	result, err := tx.Exec(r.Context(), `UPDATE workspace_members SET role=COALESCE($1,role),is_active=COALESCE($2,is_active) WHERE id=$3 AND tenant_id=$4`, input.Role, input.Active, id, p.TenantID)
	if err != nil {
		ingestError(w, 500, "Не удалось обновить доступ")
		return
	}
	if result.RowsAffected() != 1 {
		ingestError(w, 404, "Пользователь не найден")
		return
	}
	if input.Active != nil && !*input.Active {
		if _, err = tx.Exec(r.Context(), `UPDATE workspace_members SET auth_version=auth_version+1 WHERE id=$1`, id); err != nil {
			ingestError(w, 500, "Не удалось отозвать доступ")
			return
		}
		if _, err = tx.Exec(r.Context(), `DELETE FROM auth_sessions WHERE member_id=$1`, id); err != nil {
			ingestError(w, 500, "Не удалось отозвать сеансы")
			return
		}
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO audit_logs(tenant_id,event_type,actor,details) VALUES($1,'MEMBER_ACCESS_CHANGED',$2,$3)`, p.TenantID, p.ID, "member="+id); err != nil {
		ingestError(w, 500, "Не удалось записать аудит")
		return
	}
	if tx.Commit(r.Context()) != nil {
		ingestError(w, 500, "Не удалось сохранить доступ")
		return
	}
	roleJSON(w, 200, map[string]bool{"updated": true})
}

func roleJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// RotateMember replaces the stored hash atomically; old codes stop working.
func (h *WorkspaceHandler) RotateMember(w http.ResponseWriter, r *http.Request) {
	p, ok := CurrentPrincipal(r.Context())
	if !ok || p.Role != "admin" {
		ingestError(w, 403, "Только администратор может управлять доступами")
		return
	}
	id := r.PathValue("id")
	if id == p.ID {
		ingestError(w, 400, "Собственный код меняет владелец компании")
		return
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		ingestError(w, 500, "Не удалось создать код")
		return
	}
	token := "usr_" + base64.RawURLEncoding.EncodeToString(secret)
	sum := sha256.Sum256([]byte(token))
	tx, err := h.db.Begin(r.Context())
	if err != nil {
		ingestError(w, 500, "Не удалось обновить код")
		return
	}
	defer tx.Rollback(r.Context())
	var m member
	err = tx.QueryRow(r.Context(), `UPDATE workspace_members SET token_hash=$1,password_hash='',auth_version=auth_version+1 WHERE id=$2 AND tenant_id=$3 AND is_active=TRUE RETURNING id,display_name,email,role`, hex.EncodeToString(sum[:]), id, p.TenantID).Scan(&m.ID, &m.Name, &m.Email, &m.Role)
	if err != nil {
		ingestError(w, 404, "Активный пользователь не найден")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO audit_logs(tenant_id,event_type,actor,details) VALUES($1,'MEMBER_CODE_ROTATED',$2,$3)`, p.TenantID, p.ID, "member="+id); err != nil {
		ingestError(w, 500, "Не удалось записать аудит")
		return
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM auth_sessions WHERE member_id=$1`, id); err != nil {
		ingestError(w, 500, "Не удалось отозвать сеансы")
		return
	}
	if tx.Commit(r.Context()) != nil {
		ingestError(w, 500, "Не удалось сохранить код")
		return
	}
	roleJSON(w, 200, map[string]string{"id": m.ID, "name": m.Name, "email": m.Email, "role": m.Role, "token": token})
}
