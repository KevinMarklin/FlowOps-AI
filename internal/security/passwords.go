package security

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

const sessionCookie = "flowops_session"
const csrfCookie = "flowops_csrf"
const sessionLifetime = 8 * time.Hour
const passwordCost = 12

var passwordSlots = make(chan struct{}, 4)
var dummyPasswordHash = func() []byte {
	hash, err := bcrypt.GenerateFromPassword([]byte("invalid-account-timing-placeholder"), passwordCost)
	if err != nil {
		panic(err)
	}
	return hash
}()

type PasswordHandler struct {
	db      *pgxpool.Pool
	mu      sync.Mutex
	windows map[string]workspaceRateWindow
}

func NewPasswordHandler(db *pgxpool.Pool) *PasswordHandler {
	return &PasswordHandler{db: db, windows: map[string]workspaceRateWindow{}}
}
func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func randomCredential() (string, error) {
	value := make([]byte, 32)
	_, err := rand.Read(value)
	return base64.RawURLEncoding.EncodeToString(value), err
}
func validPassword(value string) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) >= 12 && len(value) <= 72 && strings.TrimSpace(value) != ""
}
func normalizedEmail(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	address, err := mail.ParseAddress(value)
	return value, err == nil && address.Address == value && len(value) <= 254
}

// Only same-origin JSON can change cookie-authenticated state. CLI requests
// without Origin still need the session-bound CSRF token for protected writes.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && strings.EqualFold(parsed.Host, r.Host) && parsed.User == nil
}
func publicAuthJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if !sameOrigin(r) || err != nil || mediaType != "application/json" {
		ingestError(w, 403, "Запрос должен быть отправлен с сайта сервиса")
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		ingestError(w, 400, "Проверьте данные формы")
		return false
	}
	return true
}
func (h *PasswordHandler) permit(w http.ResponseWriter, r *http.Request, email string) bool {
	ip := r.RemoteAddr
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	for key, window := range h.windows {
		if now.Sub(window.started) > time.Minute {
			delete(h.windows, key)
		}
	}
	for _, key := range []string{"ip:" + ip, "email:" + digest(email)} {
		window := h.windows[key]
		if now.Sub(window.started) > time.Minute {
			window = workspaceRateWindow{started: now}
		}
		limit := 15
		if strings.HasPrefix(key, "ip:") {
			limit = 60
		}
		if window.count >= limit || len(h.windows) > 10000 {
			w.Header().Set("Retry-After", "60")
			ingestError(w, 429, "Слишком много попыток. Повторите через минуту.")
			return false
		}
		window.count++
		h.windows[key] = window
	}
	return true
}
func passwordSlot(w http.ResponseWriter) bool {
	select {
	case passwordSlots <- struct{}{}:
		return true
	default:
		w.Header().Set("Retry-After", "5")
		ingestError(w, 429, "Сервис входа занят. Повторите через несколько секунд.")
		return false
	}
}

type sessionSecrets struct{ token, csrf string }

func newSessionSecrets() (sessionSecrets, error) {
	token, err := randomCredential()
	if err != nil {
		return sessionSecrets{}, err
	}
	csrf, err := randomCredential()
	return sessionSecrets{token, csrf}, err
}
func insertSession(ctx context.Context, tx pgx.Tx, memberID, passwordHash string, secrets sessionSecrets) error {
	result, err := tx.Exec(ctx, `INSERT INTO auth_sessions(token_hash,csrf_hash,member_id,auth_version,expires_at) SELECT $1,$2,id,auth_version,NOW()+INTERVAL '8 hours' FROM workspace_members WHERE id=$3 AND is_active=TRUE AND password_hash=$4`, digest(secrets.token), digest(secrets.csrf), memberID, passwordHash)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}
func setSessionCookies(w http.ResponseWriter, r *http.Request, secrets sessionSecrets) {
	secure := r.TLS != nil || os.Getenv("FLOWOPS_COOKIE_SECURE") == "true"
	for name, value := range map[string]string{sessionCookie: secrets.token, csrfCookie: secrets.csrf} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: name == sessionCookie, Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: int(sessionLifetime.Seconds()), Expires: time.Now().Add(sessionLifetime)})
	}
}
func clearSessionCookies(w http.ResponseWriter, r *http.Request) {
	for _, name := range []string{sessionCookie, csrfCookie} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", HttpOnly: name == sessionCookie, Secure: r.TLS != nil || os.Getenv("FLOWOPS_COOKIE_SECURE") == "true", SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(1, 0)})
	}
}

type sessionContextKey struct{}

func cookiePrincipal(r *http.Request, db *pgxpool.Pool) (Principal, string, error) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return Principal{}, "", pgx.ErrNoRows
	}
	p := Principal{}
	var csrfHash string
	err = db.QueryRow(r.Context(), `SELECT m.id,m.tenant_id,m.display_name,m.email,m.role,s.csrf_hash FROM auth_sessions s JOIN workspace_members m ON m.id=s.member_id WHERE s.token_hash=$1 AND s.expires_at>NOW() AND m.is_active=TRUE AND m.password_hash<>'' AND s.auth_version=m.auth_version`, digest(cookie.Value)).Scan(&p.ID, &p.TenantID, &p.Name, &p.Email, &p.Role, &csrfHash)
	return p, csrfHash, err
}
func validSessionWrite(r *http.Request, csrfHash string) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return true
	}
	supplied := digest(r.Header.Get("X-CSRF-Token"))
	return sameOrigin(r) && r.Header.Get("X-CSRF-Token") != "" && subtle.ConstantTimeCompare([]byte(supplied), []byte(csrfHash)) == 1
}

func (h *PasswordHandler) Login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email     string `json:"email"`
		Password  string `json:"password"`
		Workspace string `json:"workspace"`
	}
	if !publicAuthJSON(w, r, &input) {
		return
	}
	email, valid := normalizedEmail(input.Email)
	if !h.permit(w, r, email) {
		return
	}
	// A valid invitation is a first-login credential, not a permanent password.
	activationPending := false
	if valid && len(input.Password) >= 32 && len(input.Password) <= 256 {
		p, lookupErr := LookupPrincipal(r.Context(), h.db, input.Password)
		if lookupErr == nil && (p.Owner || p.Email == email) {
			pending := p.Owner
			if !pending {
				if err := h.db.QueryRow(r.Context(), `SELECT password_hash='' FROM workspace_members WHERE id=$1 AND is_active=TRUE`, p.ID).Scan(&pending); err != nil {
					ingestError(w, 503, "Сервис входа недоступен")
					return
				}
			}
			activationPending = pending && (input.Workspace == "" || input.Workspace == p.TenantID)
		} else if lookupErr != nil && !errors.Is(lookupErr, pgx.ErrNoRows) {
			ingestError(w, 503, "Сервис входа недоступен")
			return
		}
	}
	if !passwordSlot(w) {
		return
	}
	defer func() { <-passwordSlots }()
	if !valid || len(input.Password) > 72 || len(input.Workspace) > 100 {
		_ = bcrypt.CompareHashAndPassword(dummyPasswordHash, []byte("invalid"))
		if activationPending && len(input.Workspace) <= 100 {
			roleJSON(w, 409, map[string]any{"activation_required": true, "message": "Код принят. Задайте пароль для входа в сервис."})
			return
		}
		ingestError(w, 401, "Неверная почта или пароль, либо доступ отключён")
		return
	}
	rows, err := h.db.Query(r.Context(), `SELECT m.id,m.tenant_id,m.display_name,m.email,m.role,m.password_hash,w.name FROM workspace_members m JOIN workspaces w ON w.tenant_id=m.tenant_id WHERE m.email=$1 AND m.is_active=TRUE AND m.password_hash<>'' AND ($2='' OR m.tenant_id=$2) ORDER BY m.tenant_id LIMIT 9`, email, input.Workspace)
	if err != nil {
		ingestError(w, 503, "Сервис входа недоступен")
		return
	}
	type candidate struct {
		p          Principal
		hash, name string
	}
	candidates := []candidate{}
	for rows.Next() {
		var c candidate
		if rows.Scan(&c.p.ID, &c.p.TenantID, &c.p.Name, &c.p.Email, &c.p.Role, &c.hash, &c.name) != nil {
			rows.Close()
			ingestError(w, 503, "Сервис входа недоступен")
			return
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		ingestError(w, 503, "Сервис входа недоступен")
		return
	}
	if len(candidates) > 8 {
		_ = bcrypt.CompareHashAndPassword(dummyPasswordHash, []byte(input.Password))
		ingestError(w, 401, "Неверная почта или пароль, либо доступ отключён")
		return
	}
	matches := []candidate{}
	if len(candidates) == 0 {
		_ = bcrypt.CompareHashAndPassword(dummyPasswordHash, []byte(input.Password))
	}
	for _, c := range candidates {
		if bcrypt.CompareHashAndPassword([]byte(c.hash), []byte(input.Password)) == nil {
			matches = append(matches, c)
		}
	}
	if len(matches) == 0 {
		if activationPending {
			roleJSON(w, 409, map[string]any{"activation_required": true, "message": "Код принят. Задайте пароль для входа в сервис."})
			return
		}
		ingestError(w, 401, "Неверная почта или пароль, либо доступ отключён")
		return
	}
	if len(matches) > 1 {
		companies := []map[string]string{}
		for _, c := range matches {
			companies = append(companies, map[string]string{"id": c.p.TenantID, "name": c.name})
		}
		roleJSON(w, 409, map[string]any{"error": "Выберите компанию для входа", "workspaces": companies})
		return
	}
	c := matches[0]
	secrets, err := newSessionSecrets()
	if err != nil {
		ingestError(w, 500, "Не удалось открыть сеанс")
		return
	}
	tx, err := h.db.Begin(r.Context())
	if err != nil {
		ingestError(w, 503, "Сервис входа недоступен")
		return
	}
	defer tx.Rollback(r.Context())
	// A password reset racing verification must not mint a session from the old hash.
	if err = insertSession(r.Context(), tx, c.p.ID, c.hash, secrets); err != nil {
		ingestError(w, 401, "Доступ изменился. Повторите вход.")
		return
	}
	if old, err := r.Cookie(sessionCookie); err == nil {
		if _, err = tx.Exec(r.Context(), `DELETE FROM auth_sessions WHERE token_hash=$1`, digest(old.Value)); err != nil {
			ingestError(w, 500, "Не удалось обновить сеанс")
			return
		}
	}
	if tx.Commit(r.Context()) != nil {
		ingestError(w, 500, "Не удалось открыть сеанс")
		return
	}
	setSessionCookies(w, r, secrets)
	roleJSON(w, 200, map[string]any{"user": c.p})
}

func (h *PasswordHandler) Activate(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Code     string `json:"code"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !publicAuthJSON(w, r, &input) {
		return
	}
	email, valid := normalizedEmail(input.Email)
	if !h.permit(w, r, email) {
		return
	}
	if !valid || !validPassword(input.Password) {
		ingestError(w, 400, "Укажите рабочую почту и пароль от 12 символов. Слишком длинный пароль сократите.")
		return
	}
	if len(input.Code) > 256 {
		ingestError(w, 401, "Код активации не распознан")
		return
	}
	p, err := LookupPrincipal(r.Context(), h.db, input.Code)
	if err != nil || (!p.Owner && p.Email != email) {
		ingestError(w, 401, "Код активации или почта не совпадают, либо доступ отключён")
		return
	}
	if !passwordSlot(w) {
		return
	}
	defer func() { <-passwordSlots }()
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), passwordCost)
	if err != nil {
		ingestError(w, 500, "Не удалось сохранить пароль")
		return
	}
	secrets, err := newSessionSecrets()
	if err != nil {
		ingestError(w, 500, "Не удалось открыть сеанс")
		return
	}
	unusedCode, err := randomCredential()
	if err != nil {
		ingestError(w, 500, "Не удалось активировать доступ")
		return
	}
	tx, err := h.db.Begin(r.Context())
	if err != nil {
		ingestError(w, 503, "Сервис входа недоступен")
		return
	}
	defer tx.Rollback(r.Context())
	if p.Owner {
		// The bootstrap owner code creates a personal admin, never a shared login.
		var tenant string
		if tx.QueryRow(r.Context(), `SELECT tenant_id FROM workspaces WHERE tenant_id=$1 AND token_hash=$2 FOR UPDATE`, p.TenantID, digest(input.Code)).Scan(&tenant) != nil {
			ingestError(w, 401, "Код владельца изменился")
			return
		}
		p.Owner = false
		p.Email = email
		p.Name = email
		var existingRole string
		err = tx.QueryRow(r.Context(), `SELECT id,display_name,role FROM workspace_members WHERE tenant_id=$1 AND email=$2 FOR UPDATE`, p.TenantID, email).Scan(&p.ID, &p.Name, &existingRole)
		if errors.Is(err, pgx.ErrNoRows) {
			identity, generateErr := randomCredential()
			if generateErr != nil {
				ingestError(w, 500, "Не удалось создать профиль")
				return
			}
			p.ID = "usr_" + identity
			_, err = tx.Exec(r.Context(), `INSERT INTO workspace_members(id,tenant_id,display_name,email,role,token_hash,password_hash) VALUES($1,$2,$3,$4,'admin',$5,$6)`, p.ID, p.TenantID, p.Name, email, digest(unusedCode), string(hash))
		} else if err == nil && existingRole == "admin" {
			_, err = tx.Exec(r.Context(), `UPDATE workspace_members SET password_hash=$1,token_hash=$2,is_active=TRUE,auth_version=auth_version+1 WHERE id=$3`, string(hash), digest(unusedCode), p.ID)
		} else {
			ingestError(w, 409, "Для этой почты используйте персональный код участника")
			return
		}
		if err != nil {
			ingestError(w, 409, "Профиль с этой почтой уже существует. Войдите по паролю или запросите новый код участника.")
			return
		}
	} else {
		err = tx.QueryRow(r.Context(), `UPDATE workspace_members SET password_hash=$1,token_hash=$2,auth_version=auth_version+1 WHERE id=$3 AND token_hash=$4 AND is_active=TRUE AND password_hash='' RETURNING display_name,email,role`, string(hash), digest(unusedCode), p.ID, digest(input.Code)).Scan(&p.Name, &p.Email, &p.Role)
		if err != nil {
			ingestError(w, 401, "Код уже использован или доступ изменился")
			return
		}
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM auth_sessions WHERE member_id=$1`, p.ID); err != nil {
		ingestError(w, 500, "Не удалось обновить сеансы")
		return
	}
	if err = insertSession(r.Context(), tx, p.ID, string(hash), secrets); err != nil {
		ingestError(w, 500, "Не удалось открыть сеанс")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO audit_logs(tenant_id,event_type,actor,details) VALUES($1,'ACCOUNT_ACTIVATED',$2,'email/password login enabled')`, p.TenantID, p.ID); err != nil {
		ingestError(w, 500, "Не удалось записать аудит")
		return
	}
	if tx.Commit(r.Context()) != nil {
		ingestError(w, 500, "Не удалось активировать профиль")
		return
	}
	setSessionCookies(w, r, secrets)
	roleJSON(w, 200, map[string]any{"user": p})
}

func (h *PasswordHandler) Logout(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		ingestError(w, 403, "Запрос должен быть отправлен с сайта сервиса")
		return
	}
	p, csrfHash, err := cookiePrincipal(r, h.db)
	if expected := r.Header.Get("X-FlowOps-Member"); err == nil && expected != "" && expected != p.ID {
		roleJSON(w, http.StatusConflict, map[string]string{"code": "session_changed", "error": "В другой вкладке выполнен вход в другой аккаунт. Войдите заново."})
		return
	}
	if err == nil && !validSessionWrite(r, csrfHash) {
		ingestError(w, 403, "Проверка безопасности сеанса не пройдена")
		return
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		ingestError(w, 503, "Не удалось завершить сеанс")
		return
	}
	if cookie, cookieErr := r.Cookie(sessionCookie); cookieErr == nil {
		if _, err = h.db.Exec(r.Context(), `DELETE FROM auth_sessions WHERE token_hash=$1`, digest(cookie.Value)); err != nil {
			ingestError(w, 503, "Не удалось завершить сеанс")
			return
		}
	}
	clearSessionCookies(w, r)
	roleJSON(w, 200, map[string]bool{"logged_out": true})
}
