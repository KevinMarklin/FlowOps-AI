package settings_test

import (
	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/settings"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOrganizationStartersLoadWithTheirDepartments(t *testing.T) {
	for _, kind := range []string{"education", "bakery", "bank", "other", "financial_university", "it_company", "manufacturing", "retail", "medical", "logistics", "blank"} {
		t.Run(kind, func(t *testing.T) {
			template, ok := settings.Organization(kind)
			if !ok {
				t.Fatal("missing template")
			}
			dir := filepath.Join(t.TempDir(), "new-workspace")
			if err := template.Seed(dir); err != nil {
				t.Fatal(err)
			}
			base, err := knowledge.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(base.Documents()) != len(template.Documents) {
				t.Fatal("starter documents did not load")
			}
			for _, doc := range template.Documents {
				raw, err := os.ReadFile(filepath.Join(dir, doc.Slug+".md"))
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(raw), "Требует адаптации") {
					t.Fatal("missing template notice")
				}
				if knowledge.NormalizeDepartment(doc.Department) != doc.Department {
					t.Fatalf("lost department %s", doc.Department)
				}
			}
			if err := template.Seed(dir); err == nil {
				t.Fatal("must refuse an existing directory")
			}
		})
	}
	if _, ok := settings.Organization("invalid"); ok {
		t.Fatal("accepted unknown organization")
	}
	template, ok := settings.Organization("")
	if !ok || template.Type != "other" {
		t.Fatal("legacy registration default changed")
	}
}
