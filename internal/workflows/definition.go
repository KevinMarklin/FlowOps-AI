package workflows

import (
	"errors"
	"regexp"
	"strings"
)

var code = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

type Node struct {
	ID         string            `json:"id"`
	Kind       string            `json:"kind"`
	Title      string            `json:"title"`
	After      []string          `json:"after"`
	If         map[string]string `json:"if,omitempty"`
	Required   []string          `json:"required,omitempty"`
	Assignee   string            `json:"assignee,omitempty"`
	Department string            `json:"department,omitempty"`
	Event      string            `json:"event,omitempty"`
	Endpoint   string            `json:"endpoint,omitempty"`
	Idempotent bool              `json:"idempotent,omitempty"`
	Retries    int               `json:"retries"`
}
type Definition struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Revision       int64  `json:"revision"`
	Active         bool   `json:"active"`
	TimeoutMinutes int    `json:"timeout_minutes"`
	Nodes          []Node `json:"nodes"`
}

func (d Definition) Validate(validateEndpoint func(string) error) error {
	if !code.MatchString(d.ID) || strings.TrimSpace(d.Name) == "" || len([]rune(d.Name)) > 160 || len(d.Nodes) < 2 || len(d.Nodes) > 60 || d.TimeoutMinutes < 1 || d.TimeoutMinutes > 10080 {
		return errors.New("Некорректный процесс: 2–60 шагов и срок до 7 дней")
	}
	nodes := map[string]Node{}
	starts, ends := 0, 0
	for _, n := range d.Nodes {
		if !code.MatchString(n.ID) || nodes[n.ID].ID != "" || len([]rune(n.Title)) > 200 || n.Retries < 0 || n.Retries > 5 || len(n.After) > 60 || len(n.Required) > 100 || len(n.If) > 10 {
			return errors.New("Некорректный шаг процесса")
		}
		if (n.Kind == "start" || n.Kind == "end") && len(n.If) > 0 {
			return errors.New("Начало и завершение выполняются без условия")
		}
		switch n.Kind {
		case "start":
			starts++
			if len(n.After) > 0 {
				return errors.New("Начало не имеет предшественников")
			}
		case "end":
			ends++
		case "condition", "collect", "check", "assign", "task", "transfer", "approve", "notify", "wait", "http":
		default:
			return errors.New("Неизвестный тип шага")
		}
		if n.Kind == "condition" && len(n.If) == 0 {
			return errors.New("Добавьте условие для проверки")
		}
		if n.Kind != "start" && len(n.After) == 0 {
			return errors.New("Укажите предшествующий шаг")
		}
		if (n.Kind == "assign" || n.Kind == "approve") && n.Assignee == "" {
			return errors.New("Укажите ответственного")
		}
		if n.Kind == "transfer" && n.Department == "" {
			return errors.New("Укажите подразделение")
		}
		if n.Kind == "wait" && n.Event == "" {
			return errors.New("Укажите ожидаемое событие")
		}
		if n.Kind == "http" {
			if !n.Idempotent || n.Endpoint == "" || validateEndpoint == nil || validateEndpoint(n.Endpoint) != nil {
				return errors.New("API должен быть разрешён системой и поддерживать ключ идемпотентности")
			}
		}
		for k, v := range n.If {
			if len(k) > 100 || len(v) > 200 || v == "" {
				return errors.New("Некорректное условие")
			}
			switch k {
			case "department", "priority", "channel", "category", "ticket_type", "status", "form_id", "asset_id":
			default:
				if !strings.HasPrefix(k, "field.") {
					return errors.New("Неизвестное поле условия")
				}
			}
		}
		switch n.Event {
		case "", "employee_message", "response_available", "status_changed", "department_changed", "assignee_changed", "reopened", "sla_resumed":
		default:
			return errors.New("Неизвестное событие ожидания")
		}
		nodes[n.ID] = n
	}
	if starts != 1 || ends != 1 {
		return errors.New("Нужны ровно одно начало и одно завершение")
	}
	visited := map[string]int{}
	var visit func(string) error
	visit = func(id string) error {
		n, ok := nodes[id]
		if !ok {
			return errors.New("Предшествующий шаг не найден")
		}
		if visited[id] == 1 {
			return errors.New("Цикл в процессе")
		}
		if visited[id] == 2 {
			return nil
		}
		visited[id] = 1
		seen := map[string]bool{}
		for _, parent := range n.After {
			if seen[parent] || nodes[parent].Kind == "end" {
				return errors.New("Некорректная связь шагов")
			}
			seen[parent] = true
			if err := visit(parent); err != nil {
				return err
			}
		}
		visited[id] = 2
		return nil
	}
	for id := range nodes {
		if err := visit(id); err != nil {
			return err
		}
	}
	// Every branch must eventually join the end, preventing silently unfinished branches.
	ancestors := map[string]bool{}
	var mark func(string)
	mark = func(id string) {
		if ancestors[id] {
			return
		}
		ancestors[id] = true
		for _, parent := range nodes[id].After {
			mark(parent)
		}
	}
	for id, n := range nodes {
		if n.Kind == "end" {
			mark(id)
		}
	}
	if len(ancestors) != len(nodes) {
		return errors.New("Все ветви должны вести к завершению")
	}
	return nil
}
