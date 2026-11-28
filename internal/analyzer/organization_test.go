package analyzer

import (
	"context"
	"flowops-ai/internal/intake"
	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/settings"
	"path/filepath"
	"strings"
	"testing"
)

func TestOrganizationPortalWithSkippedIntakeUsesPortalGuide(t *testing.T) {
	t.Setenv("FLOWOPS_AI_PROVIDERS", "")
	template, _ := settings.Organization("education")
	dir := filepath.Join(t.TempDir(), "organization")
	if err := template.Seed(dir); err != nil {
		t.Fatal(err)
	}
	base, err := knowledge.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Existing workspaces retain their original saved descriptions.
	template.Builder.Departments = []settings.Department{{Name: "IT", Description: "Учебные сервисы, сеть и оборудование"}, {Name: "Academic", Description: "Учебный отдел и справки"}, {Name: "HR", Description: "Кадровые вопросы"}}
	a := New(base).WithBuilderConfig(func(context.Context) (settings.BuilderConfig, error) { return template.Builder, nil })
	result := a.Analyze(context.Background(), intake.Compose("Не могу войти в учебный портал", intake.Details{Skip: true}))
	if result.Department != "IT" || result.RAGAnswer == nil || !strings.Contains(*result.RAGAnswer, "Доступ к учебному порталу") {
		t.Fatalf("wrong answer: %+v, %s", result, stringAnswer(result.RAGAnswer))
	}
}

func stringAnswer(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func TestOrganizationExamplesUseConfiguredDepartments(t *testing.T) {
	a := &Analyzer{}
	for kind, expected := range map[string][]string{
		"education": {"IT", "IT", "Academic"},
		"bakery":    {"Production", "IT", "Supply"},
		"bank":      {"IT", "Security", "HR"},
	} {
		template, _ := settings.Organization(kind)
		for i, example := range template.Examples {
			got := a.analyzeHeuristicallyForBuilder(example, template.Builder)
			if got.Department != expected[i] {
				t.Errorf("%s: %s -> %s, want %s", kind, example, got.Department, expected[i])
			}
		}
	}
}
