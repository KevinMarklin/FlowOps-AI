package handlers

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"flowops-ai/internal/analyzer"
	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/security"
)

func TestCopilotCurrentMessageSourcesAndDraftQuality(t *testing.T) {
	db := replyTestDB(t)
	ctx := security.WithPrincipal(context.Background(), security.Principal{ID: "quality-operator", TenantID: "default", Role: "operator"})
	base, err := knowledge.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base.SetManagedLoader(func(string) ([]knowledge.Document, error) {
		return []knowledge.Document{
			{Filename: "vpn", Department: "IT", Title: "VPN", Keywords: []string{"VPN"}, Content: "Проверьте сеть и перезапустите VPN."},
			{Filename: "leave", Department: "HR", Title: "Оформление отпуска", Keywords: []string{"отпуск"}, Content: "Заявление на отпуск передайте в отдел кадров."},
		}, nil
	})
	h := NewTicketsHandler(db, slog.New(slog.NewTextHandler(io.Discard, nil)), analyzer.New(base))
	var id int
	if err = db.QueryRow(ctx, `INSERT INTO tickets(tenant_id,raw_text,department,status) VALUES('default','VPN не подключается','IT','assigned_to_operator') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO ticket_messages(tenant_id,ticket_id,role,body,source_key) VALUES('default',$1,'employee','Как оформить отпуск?','latest-topic')`, id); err != nil {
		t.Fatal(err)
	}
	data, prompt, err := h.copilotContext(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Sources) != 1 || data.Sources[0].ID != "kb:leave" {
		t.Errorf("sources ignore latest topic: %+v", data.Sources)
	}
	if !strings.Contains(prompt, "Как оформить отпуск?") {
		t.Error("latest message missing")
	}
	h.structuredGenerator = func(context.Context, string, string) (string, bool, error) {
		return `{"summary":"Запрос отпуска","reply":"Мы подключим специалиста и вернёмся с ответом.","questions":[". На какие даты нужен отпуск?","2. Какой тип отпуска?"],"department":"HR","source_ids":[]}`, true, nil
	}
	r := httptest.NewRequest("POST", "/api/tickets/"+strconv.Itoa(id)+"/copilot", strings.NewReader(`{"action":"reply"}`)).WithContext(ctx)
	r.SetPathValue("id", strconv.Itoa(id))
	w := httptest.NewRecorder()
	h.Copilot(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var body struct {
		RequiresReview  bool     `json:"requires_review"`
		QualityWarnings []string `json:"quality_warnings"`
		Draft           struct {
			Reply     string   `json:"reply"`
			Questions []string `json:"questions"`
		} `json:"draft"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.RequiresReview || len(body.QualityWarnings) == 0 {
		t.Error("replacement must be marked for review")
	}
	if strings.Contains(body.Draft.Reply, "подключим") || strings.Contains(body.Draft.Reply, "вернёмся") {
		t.Errorf("unsupported operational promise: %q", body.Draft.Reply)
	}
	if len(body.Draft.Questions) != 2 || body.Draft.Questions[0] != "На какие даты нужен отпуск?" || body.Draft.Questions[1] != "Какой тип отпуска?" {
		t.Errorf("list artifacts retained: %#v", body.Draft.Questions)
	}
	h.structuredGenerator = func(context.Context, string, string) (string, bool, error) {
		return `{"summary":"Запрос отпуска","reply":"Заявление на отпуск передайте в отдел кадров.","questions":[],"department":"HR","source_ids":["kb:leave"]}`, true, nil
	}
	r = httptest.NewRequest("POST", "/api/tickets/"+strconv.Itoa(id)+"/copilot", strings.NewReader(`{"action":"reply"}`)).WithContext(ctx)
	r.SetPathValue("id", strconv.Itoa(id))
	w = httptest.NewRecorder()
	h.Copilot(w, r)
	if err = json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || body.RequiresReview || body.Draft.Reply != "Заявление на отпуск передайте в отдел кадров." {
		t.Errorf("grounded draft was altered: %d %s", w.Code, w.Body.String())
	}
	if _, err = db.Exec(ctx, `UPDATE ticket_messages SET body='Не помогло' WHERE ticket_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	data, _, err = h.copilotContext(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Sources) != 1 || data.Sources[0].ID != "kb:vpn" {
		t.Errorf("short continuation lost original topic: %+v", data.Sources)
	}

	if _, err = db.Exec(ctx, `UPDATE ticket_messages SET body='VPN уже работает. Сейчас нужно оформить отпуск.' WHERE ticket_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	data, prompt, err = h.copilotContext(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Sources) != 1 || data.Sources[0].ID != "kb:leave" {
		t.Errorf("resolved historical topic still dominates: %+v", data.Sources)
	}
	h.structuredGenerator = func(_ context.Context, system, text string) (string, bool, error) {
		if strings.Contains(text, "VPN") {
			t.Error("routing includes resolved VPN instead of current request")
		}
		return `{"summary":"Оформление отпуска","reply":"","questions":[],"department":"HR","source_ids":[]}`, true, nil
	}
	r = httptest.NewRequest("POST", "/api/tickets/"+strconv.Itoa(id)+"/copilot", strings.NewReader(`{"action":"routing"}`)).WithContext(ctx)
	r.SetPathValue("id", strconv.Itoa(id))
	w = httptest.NewRecorder()
	h.Copilot(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err = db.Exec(ctx, `DELETE FROM ticket_messages WHERE ticket_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `UPDATE tickets SET raw_text='Не подключается VPN, ошибка 809. Сеть проверил, перезапускал, не помогло.' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	base.SetManagedLoader(func(string) ([]knowledge.Document, error) {
		return []knowledge.Document{{Filename: "vpn", Department: "IT", Title: "VPN", Keywords: []string{"VPN"}, Content: "Проверьте сеть и перезапустите VPN. Если не помогло, передайте специалисту."}}, nil
	})
	h.structuredGenerator = func(context.Context, string, string) (string, bool, error) {
		return `{"summary":"Ошибка VPN","reply":"","questions":["Какой тип сети?","Как давно возникло?"],"department":"IT","source_ids":["kb:vpn"]}`, true, nil
	}
	r = httptest.NewRequest("POST", "/api/tickets/"+strconv.Itoa(id)+"/copilot", strings.NewReader(`{"action":"questions"}`)).WithContext(ctx)
	r.SetPathValue("id", strconv.Itoa(id))
	w = httptest.NewRecorder()
	h.Copilot(w, r)
	if err = json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(body.Draft.Questions) != 0 {
		t.Errorf("complete grounded escalation still overquestions: %d %s", w.Code, w.Body.String())
	}

	if _, err = db.Exec(ctx, `UPDATE tickets SET raw_text='Не подключается VPN, ошибка 809.' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	r = httptest.NewRequest("POST", "/api/tickets/"+strconv.Itoa(id)+"/copilot", strings.NewReader(`{"action":"questions"}`)).WithContext(ctx)
	r.SetPathValue("id", strconv.Itoa(id))
	w = httptest.NewRecorder()
	h.Copilot(w, r)
	if err = json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(body.Draft.Questions) == 0 {
		t.Errorf("incomplete case lost clarification questions: %d %s", w.Code, w.Body.String())
	}

}
