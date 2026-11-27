// Package intake collects a small, bounded service context without a model call.
// Every value remains untrusted requester data, never a routing instruction.
package intake

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"flowops-ai/internal/security"
)

type Details struct {
	Location string `json:"location,omitempty"`
	Service  string `json:"service,omitempty"`
	Symptom  string `json:"symptom,omitempty"`
	Impact   string `json:"impact,omitempty"`
	Tried    string `json:"tried,omitempty"`
	Skip     bool   `json:"skip,omitempty"`
}
type Field struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Hint  string `json:"hint"`
}
type Plan struct {
	Kind   string  `json:"kind"`
	Title  string  `json:"title"`
	Fields []Field `json:"fields"`
	Ready  bool    `json:"ready"`
}

var fields = map[string]Field{
	"location": {"location", "Где нужна помощь?", "Корпус и аудитория или рабочее место. Если неизвестно, так и напишите."},
	"service":  {"service", "В какой сервис не получается войти?", "Название сервиса или адрес сайта. Пароль и коды доступа не присылайте."},
	"symptom":  {"symptom", "Что именно происходит?", "Например: проектор включён, но нет изображения; или текст ошибки входа."},
	"impact":   {"impact", "Как проблема влияет на работу?", "Занятие уже идёт, начнётся позже или работа пока может продолжаться."},
	"tried":    {"tried", "Что уже проверили?", "Коротко опишите проверки или напишите «Пока ничего»."},
}

func Value(d Details, key string) string {
	switch key {
	case "location":
		return d.Location
	case "service":
		return d.Service
	case "symptom":
		return d.Symptom
	case "impact":
		return d.Impact
	case "tried":
		return d.Tried
	}
	return ""
}
func Set(d *Details, key, value string) {
	value = strings.TrimSpace(value)
	switch key {
	case "location":
		d.Location = value
	case "service":
		d.Service = value
	case "symptom":
		d.Symptom = value
	case "impact":
		d.Impact = value
	case "tried":
		d.Tried = value
	}
}
func Validate(message string, d Details) error {
	if strings.TrimSpace(message) == "" || len(message) > security.MaxTicketBytes {
		return fmt.Errorf("Опишите проблему в пределах допустимого объёма.")
	}
	for _, k := range []string{"location", "service", "symptom", "impact", "tried"} {
		v := Value(d, k)
		if !utf8.ValidString(v) || utf8.RuneCountInString(v) > 300 || strings.ContainsAny(v, "\r\n\x00") {
			return fmt.Errorf("Каждый ответ должен занимать одну строку до 300 символов.")
		}
		if security.CheckPromptInput(v, 1200) != "" {
			return fmt.Errorf("Ответ требует ручной проверки. Отправьте обращение без уточнений.")
		}
	}
	return nil
}

var impactEvidence = regexp.MustCompile(`(?i)(?:через\s+[0-9]{1,3}\s+минут[а-я]*|(?:занятие|пара)\s+уже\s+ид[её]т)`)
var locationEvidence = regexp.MustCompile(`(?i)корпус\s+[^,;\n]{1,60}[,;]?\s+ауд(?:итория|\.)?\s*[0-9а-яa-z-]{1,12}`)

// Reuse explicit facts; do not invent a location, service or urgency.
func explicitDetails(message string, d Details) Details {
	lower := strings.ToLower(message)
	if d.Location == "" {
		d.Location = locationEvidence.FindString(message)
	}
	if d.Impact == "" {
		d.Impact = impactEvidence.FindString(message)
	}
	if d.Service == "" {
		for _, name := range []string{"личный кабинет", "образовательный портал", "корпоративная почта", "lk.fa.ru", "org.fa.ru", "itsm.fa.ru"} {
			if strings.Contains(lower, name) {
				d.Service = name
				break
			}
		}
	}
	if d.Symptom == "" {
		for _, symptom := range []string{"не показывает изображение", "нет изображения", "не включается", "неверный пароль", "доступ запрещён", "доступ запрещен"} {
			if strings.Contains(lower, symptom) {
				d.Symptom = symptom
				break
			}
		}
	}
	if d.Tried == "" {
		for _, step := range []string{"перезапуск не помог", "перезагрузка не помогла", "пока ничего", "ничего не пробовал"} {
			if strings.Contains(lower, step) {
				d.Tried = step
				break
			}
		}
	}
	return d
}

func Evaluate(message string, d Details) Plan {
	d = explicitDetails(message, d)
	text := strings.ToLower(message)
	p := Plan{Kind: "general", Title: "Обращение готово к отправке", Fields: []Field{}, Ready: true}
	var keys []string
	switch {
	case contains(text, "проектор", "компьютер в аудитории", "оборудование в аудитории", "экран в аудитории"):
		p.Kind = "classroom"
		p.Title = "Помощь в аудитории"
		keys = []string{"location", "symptom", "impact", "tried"}
	case contains(text, "не могу войти", "не получается войти", "не могу зайти", "не удаётся войти", "не удается войти", "восстановить пароль", "забыл пароль"):
		p.Kind = "access"
		p.Title = "Доступ к университетскому сервису"
		keys = []string{"service", "symptom", "tried"}
	}
	if d.Skip {
		return p
	}
	for _, k := range keys {
		if strings.TrimSpace(Value(d, k)) == "" {
			p.Fields = append(p.Fields, fields[k])
		}
	}
	p.Ready = len(p.Fields) == 0
	return p
}
func contains(text string, words ...string) bool {
	for _, word := range words {
		if strings.Contains(text, word) {
			return true
		}
	}
	return false
}

// Compose stores context in the existing ticket text, so it survives restarts,
// appears in every channel and exports without a second, divergent data store.
func Compose(message string, d Details) string {
	d = explicitDetails(message, d)
	var b strings.Builder
	b.WriteString(strings.TrimSpace(message))
	var lines []string
	for _, k := range []string{"location", "service", "symptom", "impact", "tried"} {
		if v := strings.TrimSpace(Value(d, k)); v != "" {
			lines = append(lines, fields[k].Label+" "+v)
		}
	}
	if len(lines) > 0 {
		b.WriteString("\n\nУточнения заявителя:\n")
		b.WriteString(strings.Join(lines, "\n"))
	}
	if d.Skip {
		b.WriteString("\nУточнения пропущены: специалисту может потребоваться дополнительная информация.")
	}
	return b.String()
}
