package handbook

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"flowops-ai/internal/security"
)

func TestPublicDocumentationAndLicense(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux, "../LICENSE")
	handler := security.ProtectOperatorTenants(mux, nil)
	paths := []string{"/documentation", "/documentation/", "/license", "/license.txt", "/third-party-notices.txt"}
	for _, c := range Chapters {
		paths = append(paths, "/documentation/"+c.Slug)
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			if len(w.Body.Bytes()) < 300 {
				t.Fatal("empty document")
			}
			if !strings.HasSuffix(path, ".txt") && (!strings.Contains(w.Body.String(), `id="doc-content"`) || !strings.Contains(w.Header().Get("Content-Type"), "text/html")) {
				t.Fatal("missing document shell")
			}
		})
	}
	for _, path := range []string{"/documentation/.env", "/documentation/unknown", "/documentation/api/extra"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 404 {
			t.Fatal("unexpected route", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/license", nil))
	if !strings.Contains(w.Body.String(), "TERMS AND CONDITIONS FOR USE") || !strings.Contains(w.Body.String(), "Apache License") {
		t.Fatal("license text omitted")
	}
}

func TestManualCatalogueAndLinks(t *testing.T) {
	known := map[string]bool{}
	for _, c := range Chapters {
		if known[c.Slug] {
			t.Fatal("duplicate chapter", c.Slug)
		}
		known[c.Slug] = true
	}
	links := regexp.MustCompile(`\]\(/documentation/([a-z0-9-]+)(?:[)#])`)
	for _, c := range Chapters {
		raw, err := content.ReadFile("manual/" + c.Slug + ".md")
		if err != nil {
			t.Fatal(c.Slug, err)
		}
		if !strings.HasPrefix(string(raw), "# ") {
			t.Fatal("missing title", c.Slug)
		}
		for _, link := range links.FindAllStringSubmatch(string(raw), -1) {
			if !known[link[1]] {
				t.Fatal("broken handbook link", c.Slug, link[1])
			}
		}
	}
	// Every registered API method must appear in the developer reference.
	main, err := os.ReadFile("../cmd/server/main.go")
	if err != nil {
		t.Fatal(err)
	}
	api, _ := content.ReadFile("manual/api.md")
	for _, m := range regexp.MustCompile(`mux.Handle(?:Func)?\("(GET|POST|PUT|PATCH|DELETE|OPTIONS) (/api/[^" ]+)"`).FindAllStringSubmatch(string(main), -1) {
		if !strings.Contains(string(api), "| "+m[1]+" | `"+m[2]+"`") {
			t.Fatal("undocumented endpoint", m[1], m[2])
		}
	}
}

func TestMissingLicenseFailsExplicitly(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux, "missing-license")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/license", nil))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
}

func TestMarkdownDoesNotEnableRawHTMLOrJavascriptLinks(t *testing.T) {
	rendered, err := renderMarkdown([]byte("# Safe\n\n<script>alert('secret')</script>\n\n[click](javascript:alert%281%29)\n\n| A | B |\n|---|---|\n| 1 | 2 |\n"))
	if err != nil {
		t.Fatal(err)
	}
	html := string(rendered)
	if strings.Contains(html, "<script>") || strings.Contains(html, `href="javascript:`) {
		t.Fatal("unsafe markup rendered", html)
	}
	if !strings.Contains(html, "<table>") || !strings.Contains(html, "<h1") {
		t.Fatal("documentation formatting missing", html)
	}
}

func TestPinnedModuleNoticesAreIncluded(t *testing.T) {
	module, err := os.ReadFile("../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	notices, err := os.ReadFile("../THIRD_PARTY_NOTICES.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`(?m)^\s+([a-z0-9./-]+)\s+(v[^\s]+)`).FindAllStringSubmatch(string(module), -1) {
		if !strings.Contains(string(notices), "## "+m[1]+" "+m[2]) {
			t.Fatal("missing pinned license notice", m[1], m[2])
		}
	}
	if !strings.Contains(string(notices), "SIL OPEN FONT LICENSE") {
		t.Fatal("bundled font license missing")
	}
}

func TestDocumentationGroupsAndSubsectionAnchors(t *testing.T) {
	groups := navigationGroups("data")
	if len(groups) != 6 {
		t.Fatal("expected six documentation sections", len(groups))
	}
	count, active := 0, 0
	for _, group := range groups {
		count += len(group.Chapters)
		if group.Active {
			active++
			if group.Title != "Справка" {
				t.Fatal("wrong section expanded", group.Title)
			}
		}
	}
	if count != len(Chapters) || active != 1 {
		t.Fatal("chapters lost or incorrect expansion", count, active)
	}
	for _, chapter := range Chapters {
		raw, _ := content.ReadFile("manual/" + chapter.Slug + ".md")
		body, headings, err := renderChapter(raw)
		if err != nil {
			t.Fatal(err)
		}
		if len(headings) == 0 {
			t.Fatal("missing subsections", chapter.Slug)
		}
		for _, heading := range headings {
			if heading.Title == "" || !strings.Contains(string(body), `id="`+heading.ID+`"`) {
				t.Fatal("broken subsection anchor", chapter.Slug, heading)
			}
		}
	}
	mux := http.NewServeMux()
	Register(mux, "../LICENSE")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/documentation/data", nil))
	if !strings.Contains(w.Body.String(), `class="docs-subsections"`) || !strings.Contains(w.Body.String(), "/documentation/data#") {
		t.Fatal("subsections absent from navigation")
	}
}
