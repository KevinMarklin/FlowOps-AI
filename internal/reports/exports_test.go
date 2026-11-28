package reports

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReportFormatsPreserveCyrillicAndNeverCreateFormulas(t *testing.T) {
	table := Table{Title: "Проверка аналитики", Subtitle: "2026-11-01 — 2026-11-28 · Europe/Moscow", Headers: []string{"Показатель", "Значение", "Единица", "Тип", "Выборка", "Методика", "Источник"}, Notes: []string{"Расчёт не гарантирует сокращения расходов."}}
	for i := 0; i < 25; i++ {
		table.Rows = append(table.Rows, []string{"Среднее время решения", "Нет данных", "seconds", "measured", "0", "Время первого записанного ответа без импортированных старых сообщений", "ticket_messages"})
	}
	table.Rows = append(table.Rows, []string{"=HYPERLINK(\"x\")", "123"})
	xlsx, err := XLSX(table)
	if err != nil {
		t.Fatal(err)
	}
	r, err := zip.NewReader(bytes.NewReader(xlsx), int64(len(xlsx)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range r.File {
		rc, _ := f.Open()
		data, _ := io.ReadAll(rc)
		rc.Close()
		decoder := xml.NewDecoder(bytes.NewReader(data))
		for {
			_, e := decoder.Token()
			if e == io.EOF {
				break
			}
			if e != nil {
				t.Fatal(f.Name, e)
			}
		}
		if f.Name == "xl/worksheets/sheet1.xml" && strings.Contains(string(data), "<f>") {
			t.Fatal("spreadsheet formula injected")
		}
	}
	pdf, err := PDF(table)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatal("not a PDF")
	}
	if dir := os.Getenv("FLOWOPS_REPORT_TEST_DIR"); dir != "" {
		if err = os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, "analytics.pdf"), pdf, 0600)
		os.WriteFile(filepath.Join(dir, "analytics.xlsx"), xlsx, 0600)
	}
}
