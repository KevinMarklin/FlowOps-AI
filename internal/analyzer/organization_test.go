package analyzer

import (
	"flowops-ai/internal/settings"
	"testing"
)

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
