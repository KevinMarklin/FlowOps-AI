package knowledge

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFindRestrictsDocumentByDepartmentAndTopic(t *testing.T) {
	dir := t.TempDir()
	documents := map[string]string{
		"it_vpn.md":              "# VPN\nОшибка 809 и настройка L2TP/IPsec.",
		"hr_certificates.md":     "# Справки\nЗаказ справки 2-НДФЛ.",
		"accounting_payments.md": "# Выплаты\nРасчетный листок и зарплата.",
		"office_equipment.md":    "# АХО\nЗамена картриджа в принтере.",
	}
	for name, content := range documents {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	base, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	doc := base.Find("Срочно! Не работает корпоративный VPN, ошибка 809.", "IT")
	if doc == nil || filepath.Base(doc.Path) != "it_vpn.md" {
		t.Fatalf("expected it_vpn.md, got %#v", doc)
	}
	if base.Find("Не работает корпоративный VPN, ошибка 809.", "HR") != nil {
		t.Fatal("expected no HR document for an IT ticket")
	}
	if base.Find("Упал Kubernetes после деплоя.", "Unassigned") != nil {
		t.Fatal("expected no document for an unsupported topic")
	}
}

func TestAddGeneralDocumentAndFindForUnassignedTicket(t *testing.T) {
	dir := t.TempDir()
	base, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := base.Add("", "Локальная ИИ", []string{"локальная ии", "нейросеть"}, "Перезапустите локальную модель и проверьте доступность GPU.")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Department != "General" {
		t.Fatalf("expected General department, got %q", doc.Department)
	}
	if _, err := os.Stat(filepath.Join(dir, doc.Filename)); err != nil {
		t.Fatal(err)
	}
	found := base.FindRelevant("Не запускается локальная ИИ", "Unassigned")
	if found == nil || found.Filename != doc.Filename {
		t.Fatalf("expected %s, got %#v", doc.Filename, found)
	}
}

func TestAddUnassignedDocumentNormalizesToGeneral(t *testing.T) {
	base, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	doc, err := base.Add("Unassigned", "Общее решение", nil, "Общая инструкция.")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Department != "General" {
		t.Fatalf("expected General department, got %q", doc.Department)
	}
}

func TestFindRelevantDoesNotUseGenericWordsForVPN(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "it_vpn.md"), []byte("# VPN\nОшибка сертификата для VPN."), 0600); err != nil {
		t.Fatal(err)
	}
	base, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if document := base.FindRelevant("Не открывается мобильный банк на Android, ошибка сертификата SSL_PINNING_ERR", "Unassigned"); document != nil {
		t.Fatalf("unexpected unrelated document: %#v", document)
	}
}

func TestResetRemovesUserDocumentsAndKeepsBuiltins(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "it_vpn.md"), []byte("# VPN\nVPN 809"), 0600); err != nil {
		t.Fatal(err)
	}
	base, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	userDocument, err := base.Add("Unassigned", "Временное решение", []string{"SSL_PINNING_ERR"}, "Проверьте сертификат.")
	if err != nil {
		t.Fatal(err)
	}
	if err := base.Reset(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, userDocument.Filename)); !os.IsNotExist(err) {
		t.Fatalf("user document still exists: %v", err)
	}
	if len(base.Documents()) != 1 || base.Documents()[0].Filename != "it_vpn.md" {
		t.Fatalf("unexpected documents after reset: %#v", base.Documents())
	}
}

func TestRussianTitleUsesReadableTransliteratedFilename(t *testing.T) {
	base, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	doc, err := base.Add("IT", "Проблема с локальной ИИ", nil, "Решение проблемы.")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Filename != "it_problema_s_lokalnoy_ii.md" {
		t.Fatalf("unexpected filename: %s", doc.Filename)
	}
}

func TestExpiredDocumentIsPurgedFromMemoryAndDisk(t *testing.T) {
	dir := t.TempDir()
	base, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(30 * time.Millisecond)
	doc, err := base.AddWithOptions("IT", "Временная авария NL-1", []string{"NL-1"}, "Сбой устранен.", "incident", "active", &expires)
	if err != nil {
		t.Fatal(err)
	}
	if base.FindRelevant("Проблема NL-1", "IT") == nil {
		t.Fatal("expected active incident")
	}
	time.Sleep(50 * time.Millisecond)
	if err := base.PurgeExpired(); err != nil {
		t.Fatal(err)
	}
	if base.FindRelevant("Проблема NL-1", "IT") != nil {
		t.Fatal("expired document still found")
	}
	if _, err := os.Stat(filepath.Join(dir, doc.Filename)); !os.IsNotExist(err) {
		t.Fatalf("expired file still exists: %v", err)
	}
}
