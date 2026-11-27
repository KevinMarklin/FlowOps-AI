package analyzer

import (
	"context"
	"flowops-ai/internal/knowledge"
	"testing"
)

func TestSpecificSupportTopicsOutrankGenericWords(t *testing.T) {
	t.Setenv("FLOWOPS_AI_PROVIDERS", "")
	base, err := knowledge.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := New(base)
	for _, c := range []struct{ text, dept string }{
		{"Закончился картридж принтера.", "Facilities"},
		{"Принтер сообщает об ошибке бумаги.", "Facilities"},
		{"Нужна справка о зачислении зарплаты на карту.", "Accounting"},
		{"Повторно списана оплата тарифа.", "Billing"},
	} {
		if got := a.Analyze(context.Background(), c.text); got.Department != c.dept {
			t.Errorf("got %s, want %s", got.Department, c.dept)
		}
	}
}
