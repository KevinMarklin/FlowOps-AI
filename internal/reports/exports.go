package reports

import (
	"archive/zip"
	"bytes"
	"codeberg.org/go-pdf/fpdf"
	_ "embed"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

//go:embed fonts/golos-400.ttf
var reportFont []byte

type Table struct {
	Title    string
	Subtitle string
	Headers  []string
	Rows     [][]string
	Notes    []string
}

func xmlText(s string) string { var b bytes.Buffer; xml.EscapeText(&b, []byte(s)); return b.String() }
func column(n int) string {
	s := ""
	for n > 0 {
		n--
		s = string(rune('A'+n%26)) + s
		n /= 26
	}
	return s
}

// XLSX uses inline strings, never formulas or externally referenced content.
func XLSX(t Table) ([]byte, error) {
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	entries := map[string]string{
		"[Content_Types].xml":        `<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>`,
		"_rels/.rels":                `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`,
		"xl/workbook.xml":            `<?xml version="1.0" encoding="UTF-8"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Analytics" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`}
	rows := [][]string{{t.Title}, {t.Subtitle}, {}, t.Headers}
	rows = append(rows, t.Rows...)
	rows = append(rows, []string{})
	for _, note := range t.Notes {
		rows = append(rows, []string{note})
	}
	var sheet strings.Builder
	sheet.WriteString(`<?xml version="1.0" encoding="UTF-8"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetViews><sheetView workbookViewId="0"><pane ySplit="4" topLeftCell="A5" activePane="bottomLeft" state="frozen"/></sheetView></sheetViews><cols><col min="1" max="1" width="36" customWidth="1"/><col min="2" max="4" width="20" customWidth="1"/><col min="5" max="8" width="70" customWidth="1"/></cols><sheetData>`)
	for i, row := range rows {
		sheet.WriteString(`<row r="` + strconv.Itoa(i+1) + `">`)
		for j, value := range row {
			if i >= 4 && (j == 1 || j == 4) {
				if _, err := strconv.ParseFloat(value, 64); err == nil && value != "NaN" && value != "+Inf" && value != "-Inf" {
					sheet.WriteString(`<c r="` + column(j+1) + strconv.Itoa(i+1) + `"><v>` + xmlText(value) + `</v></c>`)
					continue
				}
			}
			sheet.WriteString(`<c r="` + column(j+1) + strconv.Itoa(i+1) + `" t="inlineStr"><is><t xml:space="preserve">` + xmlText(value) + `</t></is></c>`)
		}
		sheet.WriteString(`</row>`)
	}
	sheet.WriteString(`</sheetData></worksheet>`)
	entries["xl/worksheets/sheet1.xml"] = sheet.String()
	for name, content := range entries {
		f, err := z.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err = io.WriteString(f, content); err != nil {
			return nil, err
		}
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func PDF(t Table) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(18, 18, 18)
	pdf.SetAutoPageBreak(true, 20)
	pdf.AddUTF8FontFromBytes("Golos", "", reportFont)
	pdf.SetTitle(t.Title, true)
	pdf.SetAuthor("FlowOps AI", true)
	pdf.AliasNbPages("")
	pdf.SetFooterFunc(func() {
		pdf.SetY(-14)
		pdf.SetFont("Golos", "", 9)
		pdf.SetTextColor(82, 109, 130)
		pdf.CellFormat(0, 5, fmt.Sprintf("FlowOps AI · %d / {nb}", pdf.PageNo()), "", 0, "R", false, 0, "")
	})
	pdf.AddPage()
	pdf.SetFont("Golos", "", 20)
	pdf.SetTextColor(16, 42, 67)
	pdf.MultiCell(0, 9, t.Title, "", "L", false)
	pdf.Ln(4)
	pdf.SetFont("Golos", "", 10)
	pdf.MultiCell(0, 5, t.Subtitle, "", "L", false)
	pdf.Ln(6)
	for _, row := range t.Rows {
		if len(row) < 2 {
			continue
		}
		if pdf.GetY() > 240 {
			pdf.AddPage()
		}
		pdf.SetFont("Golos", "", 11)
		pdf.SetTextColor(11, 83, 103)
		pdf.MultiCell(0, 6, row[0]+": "+row[1], "", "L", false)
		pdf.SetFont("Golos", "", 10)
		pdf.SetTextColor(16, 42, 67)
		for i := 2; i < len(row); i++ {
			if row[i] != "" {
				label := ""
				if i < len(t.Headers) {
					label = t.Headers[i] + ": "
				}
				pdf.MultiCell(0, 5, label+row[i], "", "L", false)
			}
		}
		pdf.Ln(5)
	}
	if len(t.Notes) > 0 {
		pdf.AddPage()
		pdf.SetFont("Golos", "", 16)
		pdf.MultiCell(0, 8, "Методические ограничения", "", "L", false)
		pdf.Ln(5)
		pdf.SetFont("Golos", "", 10)
		for _, note := range t.Notes {
			pdf.MultiCell(0, 5, note, "", "L", false)
			pdf.Ln(4)
		}
	}
	var b bytes.Buffer
	if err := pdf.Output(&b); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
