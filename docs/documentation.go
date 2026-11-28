// Package handbook serves versioned, public product documentation.
package handbook

import (
	"bytes"
	"embed"
	"html/template"
	"net/http"
	"os"
	"path/filepath"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

//go:embed manual/*.md
var content embed.FS

type Chapter struct{ Slug, Title, Group string }

var Chapters = []Chapter{
	{"overview", "Начало работы", "Основы"}, {"access", "Вход, роли и профиль", "Основы"},
	{"requests", "Обращения сотрудника", "Работа с обращениями"}, {"tickets", "Очередь и карточка тикета", "Работа с обращениями"}, {"tasks", "Задачи и согласования", "Работа с обращениями"}, {"notifications", "Уведомления SLA", "Работа с обращениями"},
	{"analytics", "Аналитика и дашборды", "Анализ"}, {"process-analytics", "Аналитика процессов", "Анализ"}, {"optimization", "Центр оптимизации", "Анализ"}, {"reports", "Отчёты", "Анализ"},
	{"organization", "Организация и SLA", "Настройки"}, {"structure", "Структура организации", "Настройки"}, {"governance", "Права, формы и автоматизация", "Настройки"}, {"knowledge", "База знаний", "Настройки"}, {"connections", "Каналы связи", "Настройки"}, {"communication", "Отделы и общение", "Настройки"}, {"import", "Импорт регламентов", "Настройки"}, {"preview", "Проверка ответов", "Настройки"}, {"widget", "Виджет сайта", "Настройки"}, {"api-keys", "API-ключи", "Настройки"}, {"api-check", "Проверка API", "Настройки"}, {"one-c", "Подключение 1С", "Настройки"}, {"workflows", "Бизнес-процессы", "Настройки"}, {"assets", "Объекты и QR-коды", "Настройки"}, {"administration", "Администрирование", "Настройки"},
	{"architecture", "Архитектура и данные", "Разработчикам"}, {"api", "Справочник API", "Разработчикам"}, {"deployment", "Запуск и сопровождение", "Разработчикам"}, {"troubleshooting", "Решение проблем", "Справка"}, {"data", "Данные и конфиденциальность", "Справка"}, {"license", "Лицензия и зависимости", "Справка"},
}

var markdown = goldmark.New(goldmark.WithExtensions(extension.Table, extension.Strikethrough), goldmark.WithParserOptions(parser.WithAutoHeadingID()))

type Heading struct {
	ID, Title string
	Level     int
}
type NavGroup struct {
	Title    string
	Active   bool
	Chapters []Chapter
}

func renderChapter(raw []byte) (template.HTML, []Heading, error) {
	document := markdown.Parser().Parse(text.NewReader(raw))
	headings := []Heading{}
	ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if heading, ok := node.(*ast.Heading); ok && entering && (heading.Level == 2 || heading.Level == 3) {
			if value, found := heading.AttributeString("id"); found {
				var id string
				switch value := value.(type) {
				case []byte:
					id = string(value)
				case string:
					id = value
				}
				if id != "" {
					headings = append(headings, Heading{ID: id, Title: string(heading.Text(raw)), Level: heading.Level})
				}
			}
		}
		return ast.WalkContinue, nil
	})
	var body bytes.Buffer
	if err := markdown.Renderer().Render(&body, raw, document); err != nil {
		return "", nil, err
	}
	return template.HTML(body.String()), headings, nil
}
func renderMarkdown(raw []byte) (template.HTML, error) {
	body, _, err := renderChapter(raw)
	return body, err
}
func navigationGroups(active string) []NavGroup {
	groups := []NavGroup{}
	for _, chapter := range Chapters {
		if len(groups) == 0 || groups[len(groups)-1].Title != chapter.Group {
			groups = append(groups, NavGroup{Title: chapter.Group})
		}
		group := &groups[len(groups)-1]
		group.Chapters = append(group.Chapters, chapter)
		if chapter.Slug == active {
			group.Active = true
		}
	}
	return groups
}

type pageData struct {
	Groups         []NavGroup
	Headings       []Heading
	Chapter        Chapter
	Chapters       []Chapter
	Body           template.HTML
	License        string
	Previous, Next *Chapter
}

// Register exposes only a fixed catalogue of repository documents, never a
// filesystem directory or tenant data. Markdown raw HTML remains disabled.
func Register(mux *http.ServeMux, licensePath string) {
	render := func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")
		if slug == "" {
			slug = "overview"
		}
		if r.URL.Path == "/license" {
			slug = "license"
		}
		index := -1
		for i, c := range Chapters {
			if c.Slug == slug {
				index = i
				break
			}
		}
		if index < 0 {
			http.NotFound(w, r)
			return
		}
		raw, err := content.ReadFile("manual/" + slug + ".md")
		if err != nil {
			http.Error(w, "Документация недоступна", 503)
			return
		}
		body, headings, err := renderChapter(raw)
		if err != nil {
			http.Error(w, "Документация недоступна", 503)
			return
		}
		data := pageData{Chapter: Chapters[index], Chapters: Chapters, Body: body, Headings: headings, Groups: navigationGroups(slug)}
		if index > 0 {
			data.Previous = &Chapters[index-1]
		}
		if index+1 < len(Chapters) {
			data.Next = &Chapters[index+1]
		}
		if slug == "license" {
			license, err := os.ReadFile(licensePath)
			if err != nil {
				http.Error(w, "Текст лицензии недоступен", 503)
				return
			}
			data.License = string(license)
			data.Headings = append(data.Headings, Heading{ID: "license-text", Title: "Полный текст Apache License 2.0", Level: 2})
		}
		var output bytes.Buffer
		if page.Execute(&output, data) != nil {
			http.Error(w, "Документация недоступна", 503)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Write(output.Bytes())
	}
	mux.HandleFunc("GET /documentation", render)
	mux.HandleFunc("GET /documentation/{$}", render)
	mux.HandleFunc("GET /documentation/{slug}", render)
	mux.HandleFunc("GET /license", render)
	mux.HandleFunc("GET /license.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeFile(w, r, licensePath)
	})
	mux.HandleFunc("GET /third-party-notices.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeFile(w, r, filepath.Join(filepath.Dir(licensePath), "THIRD_PARTY_NOTICES.md"))
	})
}

var page = template.Must(template.New("documentation").Parse(`<!doctype html>
<html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="theme-color" content="#102a43"><title>{{.Chapter.Title}} — Документация FlowOps AI</title><link rel="icon" href="/assets/flowops-mark.png"><script src="/theme.js?v=1"></script><link rel="stylesheet" href="/workspace.css?v=21"><link rel="stylesheet" href="/documentation.css?v=1"></head>
<body class="documentation-page"><a class="docs-skip" href="#doc-content">К содержимому</a><header class="docs-header"><a href="/" class="docs-brand"><img src="/assets/flowops-mark.png" alt="" width="36" height="36"><strong>FlowOps AI</strong></a><a href="/documentation">Документация</a><a href="/" class="docs-return">Открыть приложение</a></header>
<div class="docs-layout"><nav class="docs-nav" aria-label="Разделы документации"><div class="docs-contents"><h2 class="docs-contents-title">Содержание</h2>{{range .Groups}}<details class="docs-nav-group" {{if .Active}}open{{end}}><summary>{{.Title}}</summary><ul>{{range .Chapters}}<li><a href="/documentation/{{.Slug}}" {{if eq .Slug $.Chapter.Slug}}aria-current="page"{{end}}>{{.Title}}</a>{{if eq .Slug $.Chapter.Slug}}{{if $.Headings}}<ul class="docs-subsections" aria-label="Подразделы главы">{{range $.Headings}}<li class="docs-heading-level-{{.Level}}"><a href="/documentation/{{$.Chapter.Slug}}#{{.ID}}">{{.Title}}</a></li>{{end}}</ul>{{end}}{{end}}</li>{{end}}</ul></details>{{end}}</div></nav>
<main id="doc-content" class="docs-article" tabindex="-1"><p class="docs-meta"><a href="/documentation">Руководство</a> / {{.Chapter.Group}} / {{.Chapter.Title}}</p>{{.Body}}{{if .License}}<h2 id="license-text">Полный текст Apache License 2.0</h2><p><a href="/license.txt" download="LICENSE">Скачать LICENSE</a></p><pre class="license-text">{{.License}}</pre>{{end}}<nav class="docs-pagination" aria-label="Главы руководства">{{if .Previous}}<a href="/documentation/{{.Previous.Slug}}">← {{.Previous.Title}}</a>{{end}}{{if .Next}}<a href="/documentation/{{.Next.Slug}}">{{.Next.Title}} →</a>{{end}}</nav></main></div>
<footer class="site-footer"><nav aria-label="Справочные ссылки"><a href="/documentation">Документация</a><a href="/license">Apache 2.0</a><a href="/documentation/data">Обработка данных</a></nav><p>© 2026 Авторы FlowOps AI. Проект распространяется по Apache License 2.0.</p><a href="https://github.com/KevinMarklin/FlowOps-AI" target="_blank" rel="noopener noreferrer">GitHub</a></footer></body></html>`))
