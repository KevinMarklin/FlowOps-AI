package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"flowops-ai/internal/knowledge"
	"github.com/jackc/pgx/v5/pgxpool"
)

type KnowledgeHandler struct {
	base *knowledge.Base
	db   *pgxpool.Pool
}

func NewKnowledgeHandler(base *knowledge.Base, db ...*pgxpool.Pool) *KnowledgeHandler {
	handler := &KnowledgeHandler{base: base}
	if len(db) > 0 {
		handler.db = db[0]
	}
	return handler
}

type knowledgeDocument struct {
	Filename      string     `json:"filename"`
	Department    string     `json:"department"`
	Title         string     `json:"title"`
	Keywords      []string   `json:"keywords"`
	Content       string     `json:"content"`
	Kind          string     `json:"kind"`
	IncidentState string     `json:"incident_state"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
}

type createKnowledgeRequest struct {
	Department     string   `json:"department"`
	Title          string   `json:"title"`
	Keywords       []string `json:"keywords"`
	Content        string   `json:"content"`
	TTLSeconds     int      `json:"ttl_seconds"`
	TTLMinutes     int      `json:"ttl_minutes"`
	Kind           string   `json:"kind"`
	IncidentState  string   `json:"incident_state"`
	Action         string   `json:"action"`
	ResolutionNote string   `json:"resolution_note"`
}

func (h *KnowledgeHandler) List(w http.ResponseWriter, r *http.Request) {
	_ = h.base.PurgeExpired()
	documents := h.base.Documents()
	result := make([]knowledgeDocument, 0, len(documents))
	department := r.URL.Query().Get("department")
	if department != "" {
		department = knowledge.NormalizeDepartment(department)
	}
	for _, document := range documents {
		if department != "" && document.Department != department {
			continue
		}
		result = append(result, knowledgeDocument{Filename: document.Filename, Department: document.Department, Title: document.Title, Keywords: document.Keywords, Content: document.Content, Kind: document.Kind, IncidentState: document.IncidentState, ExpiresAt: document.ExpiresAt})
	}
	slog.Info("knowledge documents listed", "count", len(result))
	writeJSON(w, http.StatusOK, result)
}

func (h *KnowledgeHandler) Create(w http.ResponseWriter, r *http.Request) {
	var request createKnowledgeRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(request.Keywords) == 0 && h.db != nil && r.URL.Query().Get("ticket_id") != "" {
		var rawText, summary string
		_ = h.db.QueryRow(r.Context(), `SELECT raw_text, COALESCE(extracted_summary, '') FROM tickets WHERE id = $1`, r.URL.Query().Get("ticket_id")).Scan(&rawText, &summary)
		request.Keywords = extractKeywords(rawText + " " + summary)
	}
	if len(request.Keywords) == 0 {
		request.Keywords = strings.Fields(request.Title)
	}
	var expiresAt *time.Time
	ttl := request.TTLSeconds
	if ttl == 0 {
		ttl = request.TTLMinutes * 60
	}
	if ttl > 0 {
		value := time.Now().Add(time.Duration(ttl) * time.Second)
		expiresAt = &value
	}
	document, err := h.base.AddWithOptions(request.Department, request.Title, request.Keywords, request.Content, request.Kind, request.IncidentState, expiresAt)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	slog.Info("knowledge document accepted through API", "filename", document.Filename, "department", document.Department, "title", document.Title)
	writeJSON(w, http.StatusCreated, knowledgeDocument{Filename: document.Filename, Department: document.Department, Title: document.Title, Keywords: document.Keywords, Content: document.Content, Kind: document.Kind, IncidentState: document.IncidentState, ExpiresAt: document.ExpiresAt})
}

func (h *KnowledgeHandler) Update(w http.ResponseWriter, r *http.Request) {
	filename := filepath.Base(r.PathValue("filename"))
	var request createKnowledgeRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	for _, doc := range h.base.Documents() {
		if doc.Filename != filename {
			continue
		}
		if request.Title == "" {
			request.Title = doc.Title
		}
		if request.Department == "" {
			request.Department = doc.Department
		}
		if request.Content == "" {
			request.Content = doc.Content
		}
		if len(request.Keywords) == 0 {
			request.Keywords = doc.Keywords
		}
		if request.Action == "resolve_incident" {
			request.Content = fmt.Sprintf("Сбой по теме «%s» устранён, все системы работают в штатном режиме. %s Если проблема у вас сохраняется — передайте обращение специалисту.", request.Title, request.ResolutionNote)
			request.IncidentState = "resolved"
		}
		if err := h.base.Delete(filename); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		kind := request.Kind
		if kind == "" {
			kind = doc.Kind
		}
		state := request.IncidentState
		if state == "" {
			state = doc.IncidentState
		}
		expiresAt := doc.ExpiresAt
		if request.TTLSeconds > 0 || request.TTLMinutes > 0 {
			expiresAt = ttlTime(request.TTLSeconds, request.TTLMinutes)
		}
		created, err := h.base.AddWithOptions(request.Department, request.Title, request.Keywords, request.Content, kind, state, expiresAt)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, knowledgeDocument{Filename: created.Filename, Department: created.Department, Title: created.Title, Keywords: created.Keywords, Content: created.Content, Kind: created.Kind, IncidentState: created.IncidentState, ExpiresAt: created.ExpiresAt})
		return
	}
	writeError(w, http.StatusNotFound, "knowledge document not found")
}

func (h *KnowledgeHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if err := h.base.Delete(filepath.Base(r.PathValue("filename"))); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}
func ttlTime(seconds, minutes int) *time.Time {
	ttl := seconds
	if ttl == 0 {
		ttl = minutes * 60
	}
	if ttl <= 0 {
		return nil
	}
	value := time.Now().Add(time.Duration(ttl) * time.Second)
	return &value
}

func extractKeywords(text string) []string {
	stop := map[string]bool{"и": true, "в": true, "на": true, "не": true, "что": true, "как": true, "для": true, "это": true, "при": true, "ошибка": true, "срочно": true}
	result := make([]string, 0, 6)
	seen := make(map[string]bool)
	for _, word := range strings.Fields(strings.ToLower(text)) {
		word = strings.Trim(word, ".,!?():;\"'()")
		if len([]rune(word)) < 4 || stop[word] || seen[word] {
			continue
		}
		seen[word] = true
		result = append(result, word)
		if len(result) == 6 {
			break
		}
	}
	return result
}
