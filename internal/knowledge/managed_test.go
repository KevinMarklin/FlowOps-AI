package knowledge

import (
	"testing"
)

func TestManagedPublicationSnapshotsAndLegacyWrites(t *testing.T) {
	base, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	base.SetManagedLoader(func(tenant string) ([]Document, error) {
		calls++
		if calls == 1 {
			return []Document{{Filename: "kb-managed-test.md", Title: "Published", Content: "Approved"}}, nil
		}
		return []Document{}, nil
	})
	first, err := base.TenantBase("sample")
	if err != nil {
		t.Fatal(err)
	}
	second, err := base.TenantBase("sample")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Documents()) != 1 || len(second.Documents()) != 0 {
		t.Fatal("request snapshots overwrite each other")
	}
	_, err = second.Add("IT", "Принтер", []string{"принтер"}, "Проверьте подключение принтера и сообщите специалисту о результате.")
	if err != nil {
		t.Fatal(err)
	}
	third, err := base.TenantBase("sample")
	if err != nil {
		t.Fatal(err)
	}
	if len(third.Documents()) != 1 {
		t.Fatal("legacy write lost")
	}
}
