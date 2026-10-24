package knowledge

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Document struct {
	Path          string
	Filename      string
	Department    string
	Title         string
	Keywords      []string
	Content       string
	Kind          string
	IncidentState string
	ExpiresAt     *time.Time
}

type Base struct {
	mu        sync.RWMutex
	directory string
	documents []Document
}

var builtinDocuments = map[string]bool{
	"it_vpn.md": true, "hr_certificates.md": true,
	"accounting_payments.md": true, "office_equipment.md": true,
}

func Load(dir string) (*Base, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read knowledge base: %w", err)
	}
	base := &Base{directory: dir}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "____") && strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
			continue
		}
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			continue
		}
		document, err := loadDocument(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		base.documents = append(base.documents, document)
	}
	sort.Slice(base.documents, func(i, j int) bool { return base.documents[i].Path < base.documents[j].Path })
	return base, nil
}

func (b *Base) Documents() []Document {
	_ = b.PurgeExpired()
	b.mu.RLock()
	defer b.mu.RUnlock()
	result := make([]Document, len(b.documents))
	copy(result, b.documents)
	for i := range result {
		result[i].Keywords = append([]string(nil), result[i].Keywords...)
	}
	return result
}

func (b *Base) Add(department, title string, keywords []string, content string) (Document, error) {
	return b.AddWithOptions(department, title, keywords, content, "permanent", "active", nil)
}

func (b *Base) AddWithOptions(department, title string, keywords []string, content, kind, state string, expiresAt *time.Time) (Document, error) {
	department = NormalizeDepartment(department)
	title = strings.TrimSpace(title)
	content = strings.TrimSpace(content)
	if title == "" || content == "" {
		return Document{}, fmt.Errorf("title and content are required")
	}
	cleanKeywords := make([]string, 0, len(keywords))
	for _, keyword := range keywords {
		if keyword = strings.TrimSpace(keyword); keyword != "" {
			cleanKeywords = append(cleanKeywords, keyword)
		}
	}
	if kind != "incident" {
		kind = "permanent"
	}
	if state != "resolved" {
		state = "active"
	}
	filename := uniqueFilename(b.directory, department, title)
	path := filepath.Join(b.directory, filename)
	expires := ""
	if expiresAt != nil {
		expires = expiresAt.UTC().Format(time.RFC3339)
	}
	data := fmt.Sprintf("---\ndepartment: %s\nkeywords: %s\nkind: %s\nincident_state: %s\nexpires_at: %s\n---\n# %s\n\n%s\n", department, strings.Join(cleanKeywords, ", "), kind, state, expires, title, content)
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		return Document{}, fmt.Errorf("save knowledge document: %w", err)
	}
	slog.Info("knowledge document added", "filename", filename, "department", department, "title", title)
	document := Document{Path: filepath.ToSlash(path), Filename: filename, Department: department, Title: title, Keywords: cleanKeywords, Content: "# " + title + "\n\n" + content, Kind: kind, IncidentState: state, ExpiresAt: expiresAt}
	b.mu.Lock()
	b.documents = append(b.documents, document)
	sort.Slice(b.documents, func(i, j int) bool { return b.documents[i].Path < b.documents[j].Path })
	b.mu.Unlock()
	return document, nil
}

// Reset removes demo-created documents from disk and the in-memory index.
func (b *Base) Reset() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	entries, err := os.ReadDir(b.directory)
	if err != nil {
		return fmt.Errorf("read knowledge base for reset: %w", err)
	}
	kept := make([]Document, 0, len(b.documents))
	for _, document := range b.documents {
		if builtinDocuments[strings.ToLower(document.Filename)] {
			kept = append(kept, document)
		}
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "____") && strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			_ = os.Remove(filepath.Join(b.directory, entry.Name()))
			continue
		}
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") || builtinDocuments[strings.ToLower(entry.Name())] {
			continue
		}
		if err := os.Remove(filepath.Join(b.directory, entry.Name())); err != nil {
			return fmt.Errorf("remove knowledge document %s: %w", entry.Name(), err)
		}
	}
	b.documents = kept
	return nil
}

func (b *Base) PurgeExpired() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	kept := b.documents[:0]
	for _, doc := range b.documents {
		if doc.ExpiresAt != nil && now.After(*doc.ExpiresAt) {
			if err := os.Remove(doc.Path); err != nil && !os.IsNotExist(err) {
				return err
			}
			slog.Info("expired knowledge document auto-deleted", "filename", doc.Filename, "title", doc.Title)
			continue
		}
		kept = append(kept, doc)
	}
	b.documents = kept
	return nil
}

func (b *Base) Delete(filename string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, doc := range b.documents {
		if doc.Filename == filename {
			if err := os.Remove(doc.Path); err != nil && !os.IsNotExist(err) {
				return err
			}
			b.documents = append(b.documents[:i], b.documents[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("knowledge document not found")
}

func (b *Base) FindRelevant(text, department string) *Document {
	_ = b.PurgeExpired()
	query := strings.ToLower(text)
	isUnassigned := strings.EqualFold(strings.TrimSpace(department), "unassigned") || strings.TrimSpace(department) == ""
	department = NormalizeDepartment(department)
	b.mu.RLock()
	defer b.mu.RUnlock()
	bestIndex, bestScore := -1, 0
	for i, document := range b.documents {
		if !isUnassigned && document.Department != "General" && document.Department != "" && document.Department != department {
			continue
		}
		if !matchesBuiltinTopic(document.Filename, query) {
			continue
		}
		score := 0
		for _, term := range append(append([]string{}, document.Keywords...), strings.Fields(strings.ToLower(document.Title))...) {
			term = strings.Trim(term, ".,!?():;\"'")
			if len(term) >= 3 && !isStopWord(term) && strings.Contains(query, strings.ToLower(term)) {
				score++
			}
		}
		for _, term := range strings.Fields(strings.ToLower(document.Content)) {
			term = strings.Trim(term, ".,!?():;\"'")
			if len(term) >= 5 && !isStopWord(term) && strings.Contains(query, term) {
				score++
				break
			}
		}
		if score > bestScore {
			bestIndex, bestScore = i, score
		}
	}
	if bestIndex < 0 {
		slog.Info("knowledge search returned no match", "department", department, "query", text)
		return nil
	}
	copy := b.documents[bestIndex]
	copy.Keywords = append([]string(nil), copy.Keywords...)
	return &copy
}

func matchesBuiltinTopic(filename, query string) bool {
	markers := map[string][]string{
		"it_vpn.md":              {"vpn", "впн", "l2tp", "ipsec", "809"},
		"hr_certificates.md":     {"2-ндфл", "справк", "кадр", "отпуск"},
		"accounting_payments.md": {"зарплат", "аванс", "бухгалтер", "расчетн", "выплат"},
		"office_equipment.md":    {"принтер", "картридж", "пропуск", "канцеляр", "офис"},
	}
	required, ok := markers[strings.ToLower(filename)]
	if !ok {
		return true
	}
	for _, marker := range required {
		if strings.Contains(query, marker) {
			return true
		}
	}
	return false
}

func isStopWord(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "ошибка", "ошибки", "ошибкой", "не", "работает", "проблема", "проблему", "срочно", "помогите", "сертификат":
		return true
	default:
		return false
	}
}

func (b *Base) Find(text, category string) *Document { return b.FindRelevant(text, category) }

func loadDocument(path string) (Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Document{}, fmt.Errorf("read knowledge document %s: %w", path, err)
	}
	content := string(data)
	filename := filepath.Base(path)
	document := Document{Path: filepath.ToSlash(path), Filename: filename, Department: departmentFromFilename(filename), Title: titleFromMarkdown(content, filename), Content: content, Kind: "permanent", IncidentState: "active"}
	if strings.HasPrefix(content, "---\n") {
		parts := strings.SplitN(content, "\n---\n", 2)
		if len(parts) == 2 {
			for _, line := range strings.Split(strings.TrimPrefix(parts[0], "---\n"), "\n") {
				key, value, found := strings.Cut(line, ":")
				if !found {
					continue
				}
				switch strings.TrimSpace(key) {
				case "department":
					document.Department = NormalizeDepartment(value)
				case "keywords":
					document.Keywords = splitKeywords(value)
				case "kind":
					document.Kind = strings.TrimSpace(value)
				case "incident_state":
					document.IncidentState = strings.TrimSpace(value)
				case "expires_at":
					if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value)); err == nil && strings.TrimSpace(value) != "" {
						document.ExpiresAt = &parsed
					}
				}
			}
			document.Content = strings.TrimSpace(parts[1])
			document.Title = titleFromMarkdown(document.Content, filename)
		}
	}
	if document.Kind == "" {
		document.Kind = "permanent"
	}
	if document.IncidentState == "" {
		document.IncidentState = "active"
	}
	return document, nil
}

func splitKeywords(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}
func departmentFromFilename(filename string) string {
	lower := strings.ToLower(filename)
	switch {
	case strings.Contains(lower, "it_"):
		return "IT"
	case strings.Contains(lower, "hr_"):
		return "HR"
	case strings.Contains(lower, "accounting_"):
		return "Accounting"
	case strings.Contains(lower, "office_"):
		return "Facilities"
	default:
		return "General"
	}
}
func uniqueFilename(dir, department, title string) string {
	base := transliterate(strings.ToLower(strings.Join(strings.Fields(department+" "+title), "_")))
	var clean strings.Builder
	for _, r := range base {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
			clean.WriteRune(r)
		} else if r == '-' {
			clean.WriteRune('_')
		}
	}
	name := strings.Trim(strings.Join(strings.FieldsFunc(clean.String(), func(r rune) bool { return r == '_' }), "_"), "_")
	if name == "" {
		name = fmt.Sprintf("rule_%d", time.Now().UnixNano())
	}
	name += ".md"
	for index := 2; ; index++ {
		if _, err := os.Stat(filepath.Join(dir, name)); os.IsNotExist(err) {
			return name
		}
		name = fmt.Sprintf("%s_%d.md", strings.TrimSuffix(name, filepath.Ext(name)), index)
	}
}

func transliterate(value string) string {
	var result strings.Builder
	table := map[rune]string{'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e", 'ж': "zh", 'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u", 'ф': "f", 'х': "h", 'ц': "c", 'ч': "ch", 'ш': "sh", 'щ': "sch", 'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya"}
	for _, r := range value {
		if v, ok := table[r]; ok {
			result.WriteString(v)
		} else {
			result.WriteRune(r)
		}
	}
	return result.String()
}
func validDepartment(value string) bool {
	return value == "IT" || value == "HR" || value == "Accounting" || value == "Facilities" || value == "General" || value == "Unassigned"
}

func NormalizeDepartment(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "billing", "биллинг", "финансы и биллинг", "оплата":
		return "Billing"
	case "it", "ит", "ит-поддержка":
		return "IT"
	case "hr", "hr и кадры", "кадры":
		return "HR"
	case "accounting", "бухгалтерия":
		return "Accounting"
	case "facilities", "ахо и офис", "ахо":
		return "Facilities"
	case "general", "общее", "общее / без отдела", "не назначен", "unassigned", "":
		return "General"
	default:
		return "General"
	}
}
func titleFromMarkdown(content, fallback string) string {
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return fallback
}
