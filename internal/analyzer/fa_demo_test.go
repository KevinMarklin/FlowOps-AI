package analyzer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flowops-ai/internal/intake"
	"flowops-ai/internal/knowledge"
)

func TestUniversityDemoScenariosReturnCorrectBundledGuide(t *testing.T) {
	t.Setenv("FLOWOPS_AI_PROVIDERS", "")
	source := filepath.Join("..", "..", "knowledge_base", "default")
	dir := t.TempDir()
	entries, err := os.ReadDir(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(source, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, entry.Name()), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	base, err := knowledge.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := New(base)
	for _, tc := range []struct{ text, department, file string }{
		{"Как получить справку об обучении по месту требования в МФЦ Финуниверситета?", "HR", "fa_study_certificate.md"},
		{"Как заказать справку для военкомата в МФЦ Финуниверситета?", "HR", "fa_military_certificate.md"},
		{"Как подключиться к сети кампуса FA-WiFi?", "IT", "fa_wifi.md"},
		{"Как восстановить пароль от ЛК или Образовательного портала Финуниверситета?", "IT", "fa_portal_password.md"},
		{"Не могу войти в личный кабинет Финуниверситета", "IT", "fa_portal_password.md"},
		{"Как оформить командировку сотруднику Финуниверситета?", "Accounting", "fa_business_trip.md"},
		{"Как оформить авансовый отчёт после командировки в Финуниверситете?", "Accounting", "fa_advance_report.md"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			r := a.Analyze(context.Background(), tc.text)
			if r.Department != tc.department || r.RAGAnswer == nil || !strings.Contains(*r.RAGAnswer, tc.file) || strings.Contains(*r.RAGAnswer, "department:") {
				t.Fatalf("wrong guide: %+v", r)
			}
		})
	}
	if err := base.Reset(); err != nil {
		t.Fatal(err)
	} // Reset must retain all shipped guides.
	count := 0
	for _, doc := range base.Documents() {
		if strings.HasPrefix(doc.Filename, "fa_") {
			count++
			if doc.Content == "" {
				t.Fatal("empty bundled guide")
			}
		}
	}
	if count != 6 {
		t.Fatalf("reset removed bundled guides: %d", count)
	}
}

func TestUniversityIntakeRoutesEquipmentAndAccessWithoutCloud(t *testing.T) {
	t.Setenv("FLOWOPS_AI_PROVIDERS", "")
	base, err := knowledge.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := New(base)
	for _, message := range []string{
		intake.Compose("Через 10 минут занятие, проектор не показывает изображение", intake.Details{Location: "Корпус А, аудитория 313", Tried: "Пока ничего"}),
		intake.Compose("Не получается войти в личный кабинет", intake.Details{Symptom: "Страница не загружается", Tried: "Перезапуск не помог"}),
	} {
		r := a.Analyze(context.Background(), message)
		if r.Department != "IT" || r.RAGAnswer != nil {
			t.Fatalf("wrong routing or invented instruction: %+v", r)
		}
	}
}
