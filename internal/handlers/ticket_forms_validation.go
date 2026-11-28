package handlers

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"
)

type CustomField struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Options  []string `json:"options,omitempty"`
	ShowWhen string   `json:"show_when,omitempty"`
	Equals   string   `json:"equals,omitempty"`
}
type TaxonomyItem struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	ParentID string `json:"parent_id,omitempty"`
	State    string `json:"state,omitempty"`
}
type NamedTicketForm struct {
	ID     string        `json:"id"`
	Label  string        `json:"label"`
	Fields []CustomField `json:"fields"`
}
type TicketFormDefinition struct {
	Forms      []NamedTicketForm `json:"forms,omitempty"`
	Categories []TaxonomyItem    `json:"categories,omitempty"`
	Types      []TaxonomyItem    `json:"types,omitempty"`
	Tags       []TaxonomyItem    `json:"tags,omitempty"`
	Statuses   []TaxonomyItem    `json:"statuses,omitempty"`
	Priorities []TaxonomyItem    `json:"priorities,omitempty"`
	Revision   int64             `json:"revision"`
	Fields     []CustomField     `json:"fields"`
}

func validForm(f TicketFormDefinition) error {
	definitions := map[string]CustomField{}
	all := append([]CustomField{}, f.Fields...)
	for _, variant := range f.Forms {
		all = append(all, variant.Fields...)
	}
	for _, field := range all {
		if previous, exists := definitions[field.ID]; exists && (previous.Type != field.Type || !reflect.DeepEqual(previous.Options, field.Options)) {
			return errors.New("Одинаковый код поля в формах должен иметь один тип и варианты")
		}
		definitions[field.ID] = field
	}
	if len(f.Forms) > 30 {
		return errors.New("Не более 30 форм")
	}
	seenForms := map[string]bool{}
	for _, variant := range f.Forms {
		if variant.ID == "" || len(variant.ID) > 64 || strings.TrimSpace(variant.Label) == "" || len([]rune(variant.Label)) > 160 || seenForms[variant.ID] {
			return errors.New("Некорректная форма")
		}
		seenForms[variant.ID] = true
		if err := validForm(TicketFormDefinition{Fields: variant.Fields}); err != nil {
			return err
		}
	}

	for kind, list := range map[string][]TaxonomyItem{"categories": f.Categories, "types": f.Types, "tags": f.Tags, "statuses": f.Statuses, "priorities": f.Priorities} {
		if len(list) > 500 {
			return errors.New("Не более 500 элементов справочника")
		}
		byID := map[string]TaxonomyItem{}
		for _, item := range list {
			if item.ID == "" || len(item.ID) > 100 || strings.TrimSpace(item.Label) == "" || len([]rune(item.Label)) > 200 {
				return errors.New("Некорректный элемент справочника")
			}
			if _, exists := byID[item.ID]; exists {
				return errors.New("Код справочника повторяется")
			}
			byID[item.ID] = item
			if kind == "statuses" && !hasValue(item.State, "new", "in_progress", "waiting", "closed") {
				return errors.New("Некорректная группа статуса")
			}
			if kind == "priorities" && !hasValue(item.State, "low", "medium", "high") {
				return errors.New("Некорректный уровень приоритета")
			}
		}
		for _, item := range list {
			seen := map[string]bool{item.ID: true}
			parent := item.ParentID
			for parent != "" {
				next, exists := byID[parent]
				if !exists || seen[parent] {
					return errors.New("Родитель не найден или образуется цикл")
				}
				seen[parent] = true
				parent = next.ParentID
			}
		}
	}

	if len(f.Fields) > 100 {
		return errors.New("Допустимо не более 100 полей")
	}
	seen := map[string]bool{}
	parents := map[string]CustomField{}
	for _, field := range f.Fields {
		if field.ID == "" || len(field.ID) > 64 || seen[field.ID] || strings.TrimSpace(field.Label) == "" || len([]rune(field.Label)) > 160 || !hasValue(field.Type, "text", "number", "date", "boolean", "select", "multi", "member", "unit", "attachment") {
			return errors.New("Некорректное поле")
		}
		if field.ShowWhen != "" && !seen[field.ShowWhen] {
			return errors.New("Условие должно ссылаться на предыдущее поле")
		}
		if field.ShowWhen != "" {
			parent := parents[field.ShowWhen]
			if hasValue(parent.Type, "multi", "attachment") {
				return errors.New("Условие должно ссылаться на одиночное значение")
			}
			if parent.Type == "boolean" && !hasValue(field.Equals, "true", "false") {
				return errors.New("Для логического условия укажите true или false")
			}
			if parent.Type == "select" && !hasValue(field.Equals, parent.Options...) {
				return errors.New("Значение условия отсутствует в списке")
			}
			if parent.Type == "number" {
				n, err := strconv.ParseFloat(field.Equals, 64)
				if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
					return errors.New("Некорректное числовое условие")
				}
			}
			if parent.Type == "date" {
				if _, err := time.Parse("2006-01-02", field.Equals); err != nil {
					return errors.New("Некорректная дата условия")
				}
			}
		}
		parents[field.ID] = field
		seen[field.ID] = true
		if (field.Type == "select" || field.Type == "multi") && (len(field.Options) == 0 || len(field.Options) > 100) {
			return errors.New("Укажите от 1 до 100 вариантов")
		}
		options := map[string]bool{}
		for _, o := range field.Options {
			if o == "" || len([]rune(o)) > 200 || options[o] {
				return errors.New("Некорректные варианты")
			}
			options[o] = true
		}
	}
	return nil
}
func validateFormValues(f TicketFormDefinition, values map[string]any) error {
	known := map[string]bool{}
	for _, field := range f.Fields {
		known[field.ID] = true
	}
	for key := range values {
		if !known[key] {
			return fmt.Errorf("Неизвестное поле: %s", key)
		}
	}
	for _, field := range f.Fields {
		value, present := values[field.ID]
		visible := field.ShowWhen == "" || fmt.Sprint(values[field.ShowWhen]) == field.Equals
		if !visible {
			if present {
				return fmt.Errorf("Скрытое поле передано: %s", field.ID)
			}
			continue
		}
		empty := !present || value == nil
		if text, ok := value.(string); ok && text == "" {
			empty = true
		}
		if list, ok := value.([]any); ok && len(list) == 0 {
			empty = true
		}
		if empty {
			if field.Required {
				return fmt.Errorf("Заполните поле: %s", field.Label)
			}
			continue
		}
		valid := false
		switch field.Type {
		case "text", "member", "unit", "attachment":
			s, ok := value.(string)
			valid = ok && len([]rune(s)) <= 4000
		case "number":
			n, ok := value.(float64)
			valid = ok && !math.IsNaN(n) && !math.IsInf(n, 0)
		case "boolean":
			_, valid = value.(bool)
		case "date":
			s, ok := value.(string)
			_, err := time.Parse("2006-01-02", s)
			valid = ok && err == nil
		case "select":
			s, ok := value.(string)
			valid = ok && hasValue(s, field.Options...)
		case "multi":
			list, ok := value.([]any)
			valid = ok && len(list) <= 100
			seen := map[string]bool{}
			for _, v := range list {
				s, ok := v.(string)
				if !ok || !hasValue(s, field.Options...) || seen[s] {
					valid = false
				}
				seen[s] = true
			}
		}
		if !valid {
			return fmt.Errorf("Некорректное значение: %s", field.Label)
		}
	}
	return nil
}

func taxonomyContains(items []TaxonomyItem, id string) bool {
	if id == "" {
		return true
	}
	for _, item := range items {
		if item.ID == id {
			return true
		}
	}
	return false
}
