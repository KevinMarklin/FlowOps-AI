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
	"unicode"

	"flowops-ai/internal/security"
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
	mu              sync.RWMutex
	directory       string
	tenantRoot      string
	tenants         map[string]*Base
	documents       []Document
	auditTTL        func(string)
	managedLoader   func(string) ([]Document, error)
	managedOnly     bool
	fileParent      *Base
	managedSnapshot []Document
}

func (b *Base) SetAuditTTLCallback(callback func(string)) {
	b.mu.Lock()
	b.auditTTL = callback
	b.mu.Unlock()
}

var builtinDocuments = map[string]bool{
	"fa_study_certificate.md":    true,
	"fa_military_certificate.md": true,
	"fa_wifi.md":                 true,
	"fa_portal_password.md":      true,
	"fa_business_trip.md":        true,
	"fa_advance_report.md":       true,

	"it_vpn.md": true, "hr_certificates.md": true,
	"accounting_payments.md": true, "office_equipment.md": true,
}

func Load(dir string) (*Base, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read knowledge base: %w", err)
	}
	base := &Base{directory: dir, tenantRoot: filepath.Dir(dir), tenants: make(map[string]*Base)}
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
	base.tenants[filepath.Base(filepath.Clean(dir))] = base
	return base, nil
}

// TenantBase returns the isolated file-backed KB for a tenant. Tenant IDs are
// constrained before being used as path components.
func (b *Base) SetManagedLoader(loader func(string) ([]Document, error)) {
	b.mu.Lock()
	b.managedLoader = loader
	b.mu.Unlock()
}
func (b *Base) TenantBase(tenant string) (*Base, error) {
	target, err := b.fileTenantBase(tenant)
	if err != nil {
		return nil, err
	}
	b.mu.RLock()
	loader := b.managedLoader
	only := b.managedOnly
	b.mu.RUnlock()
	if loader != nil {
		if tenant == "" {
			tenant = "default"
		}
		documents, err := loader(tenant)
		if err != nil {
			return nil, err
		}
		// Each request keeps its own approved-version snapshot. Concurrent RAG loads
		// must never overwrite another request's approved/unpublished document set.
		combined := documents
		if !only {
			combined = append(target.Documents(), documents...)
		}
		return &Base{directory: target.directory, tenantRoot: target.tenantRoot, fileParent: target, managedSnapshot: documents, documents: combined, managedOnly: only}, nil
	}

	return target, nil
}
func (b *Base) refreshFileSnapshot() {
	if b.fileParent == nil {
		return
	}
	documents := b.managedSnapshot
	if !b.managedOnly {
		documents = append(b.fileParent.Documents(), documents...)
	}
	b.mu.Lock()
	b.documents = documents
	b.mu.Unlock()
}

func (b *Base) fileTenantBase(tenant string) (*Base, error) {
	if tenant == "" {
		tenant = "default"
	}
	if tenant == "default" && filepath.Base(filepath.Clean(b.directory)) != "default" {
		return b, nil
	}
	for _, r := range tenant {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return nil, fmt.Errorf("invalid tenant id")
		}
	}
	b.mu.RLock()
	existing := b.tenants[tenant]
	b.mu.RUnlock()
	if existing != nil {
		return existing, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if existing = b.tenants[tenant]; existing != nil {
		return existing, nil
	}
	dir := filepath.Join(b.tenantRoot, tenant)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	loaded, err := Load(dir)
	if err != nil {
		return nil, err
	}
	loaded.tenantRoot = b.tenantRoot
	loaded.tenants = b.tenants
	b.tenants[tenant] = loaded
	return loaded, nil
}

// MigrateRootMarkdown moves legacy root-level articles into default tenant.
func MigrateRootMarkdown(root string) error {
	if err := os.MkdirAll(filepath.Join(root, "default"), 0700); err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			continue
		}
		source, target := filepath.Join(root, entry.Name()), filepath.Join(root, "default", entry.Name())
		if _, statErr := os.Stat(target); statErr == nil {
			continue
		}
		if err := os.Rename(source, target); err != nil {
			return fmt.Errorf("move legacy knowledge article %s: %w", entry.Name(), err)
		}
	}
	return nil
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

// ReplaceWithOptions validates first, writes a temporary file, then replaces the
// original. Invalid updates and write failures must not erase an existing rule.
func (b *Base) ReplaceWithOptions(filename, department, title string, keywords []string, content, kind, state string, expiresAt *time.Time) (Document, error) {
	if b.fileParent != nil {
		d, e := b.fileParent.ReplaceWithOptions(filename, department, title, keywords, content, kind, state, expiresAt)
		b.refreshFileSnapshot()
		return d, e
	}
	if err := security.ValidateKnowledge(title, keywords, content); err != nil {
		return Document{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, old := range b.documents {
		if old.Filename != filename {
			continue
		}
		department = NormalizeDepartment(department)
		if kind != "incident" {
			kind = "permanent"
		}
		if state != "resolved" {
			state = "active"
		}
		expires := ""
		if expiresAt != nil {
			expires = expiresAt.UTC().Format(time.RFC3339)
		}
		body := "# " + strings.TrimSpace(title) + "\n\n" + strings.TrimSpace(content)
		data := fmt.Sprintf("---\ndepartment: %s\nkeywords: %s\nkind: %s\nincident_state: %s\nexpires_at: %s\n---\n%s\n", department, strings.Join(keywords, ", "), kind, state, expires, body)
		tmp, err := os.CreateTemp(b.directory, ".knowledge-*.tmp")
		if err != nil {
			return Document{}, fmt.Errorf("create knowledge update: %w", err)
		}
		tmpName := tmp.Name()
		defer os.Remove(tmpName)
		if _, err := tmp.WriteString(data); err != nil {
			_ = tmp.Close()
			return Document{}, err
		}
		if err := tmp.Close(); err != nil {
			return Document{}, err
		}
		if err := os.Rename(tmpName, old.Path); err != nil {
			return Document{}, fmt.Errorf("replace knowledge document: %w", err)
		}
		doc := Document{Path: old.Path, Filename: old.Filename, Department: department, Title: strings.TrimSpace(title), Keywords: append([]string(nil), keywords...), Content: body, Kind: kind, IncidentState: state, ExpiresAt: expiresAt}
		b.documents[i] = doc
		doc.Keywords = append([]string(nil), doc.Keywords...)
		return doc, nil
	}
	return Document{}, fmt.Errorf("knowledge document not found")
}

func (b *Base) AddWithOptions(department, title string, keywords []string, content, kind, state string, expiresAt *time.Time) (Document, error) {
	if b.fileParent != nil {
		d, e := b.fileParent.AddWithOptions(department, title, keywords, content, kind, state, expiresAt)
		b.refreshFileSnapshot()
		return d, e
	}
	if err := security.ValidateKnowledge(title, keywords, content); err != nil {
		return Document{}, err
	}
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
	slog.Info("knowledge document added", "filename", filename, "department", department)
	document := Document{Path: filepath.ToSlash(path), Filename: filename, Department: department, Title: title, Keywords: cleanKeywords, Content: "# " + title + "\n\n" + content, Kind: kind, IncidentState: state, ExpiresAt: expiresAt}
	b.mu.Lock()
	b.documents = append(b.documents, document)
	sort.Slice(b.documents, func(i, j int) bool { return b.documents[i].Path < b.documents[j].Path })
	b.mu.Unlock()
	return document, nil
}

// Reset removes demo-created documents from disk and the in-memory index.
func (b *Base) Reset() error {
	if b.fileParent != nil {
		e := b.fileParent.Reset()
		b.refreshFileSnapshot()
		return e
	}
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
	if b.fileParent != nil {
		e := b.fileParent.PurgeExpired()
		b.refreshFileSnapshot()
		return e
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	kept := b.documents[:0]
	purged := make([]string, 0)
	for _, doc := range b.documents {
		if doc.ExpiresAt != nil && now.After(*doc.ExpiresAt) {
			if err := os.Remove(doc.Path); err != nil && !os.IsNotExist(err) {
				return err
			}
			slog.Info("expired knowledge document auto-deleted", "filename", doc.Filename)
			purged = append(purged, doc.Filename)
			continue
		}
		kept = append(kept, doc)
	}
	b.documents = kept
	callback := b.auditTTL
	for range purged {
		if callback != nil {
			callback("")
		}
	}
	return nil
}

func (b *Base) Delete(filename string) error {
	if b.fileParent != nil {
		e := b.fileParent.Delete(filename)
		b.refreshFileSnapshot()
		return e
	}
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
	query, _, _ = strings.Cut(query, "\nуточнения пропущены:")
	queryWords := strings.FieldsFunc(query, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	isUnassigned := strings.EqualFold(strings.TrimSpace(department), "unassigned") || strings.TrimSpace(department) == ""
	department = NormalizeDepartment(department)
	b.mu.RLock()
	defer b.mu.RUnlock()
	bestIndex, bestScore := -1, 0
	for i, document := range b.documents {
		if security.ValidateKnowledge(document.Title, document.Keywords, document.Content) != nil {
			continue
		}
		if !isUnassigned && document.Department != "General" && document.Department != "" && document.Department != department {
			continue
		}
		if !matchesBuiltinTopic(document.Filename, query) {
			continue
		}
		score := 0
		for _, term := range append(append([]string{}, document.Keywords...), strings.Fields(strings.ToLower(document.Title))...) {
			term = strings.Trim(term, ".,!?():;\"'")
			if len([]rune(term)) >= 3 && !isStopWord(term) && matchesTerm(query, queryWords, strings.ToLower(term)) {
				score++
			}
		}
		if score > bestScore {
			bestIndex, bestScore = i, score
		}
	}
	if bestIndex < 0 {
		return nil
	}
	copy := b.documents[bestIndex]
	copy.Keywords = append([]string(nil), copy.Keywords...)
	return &copy
}

func matchesTerm(query string, words []string, term string) bool {
	if strings.Contains(query, term) {
		return true
	}
	// Inflections should not make «справку» invisible to a «справка» rule.
	// Phrase keywords still require the whole phrase, not one generic word.
	if strings.ContainsAny(term, " -_/") {
		return false
	}
	stem := russianStem(term)
	if stem == term && len([]rune(term)) < 5 {
		return false
	}
	for _, word := range words {
		if russianStem(word) == stem {
			return true
		}
	}
	return false
}

func russianStem(word string) string {
	word = strings.ReplaceAll(word, "ё", "е")
	if len([]rune(word)) < 5 {
		return word
	}
	for _, r := range word {
		if r < 'а' || r > 'я' {
			return word
		}
	}
	for _, suffix := range []string{"иями", "ями", "ами", "ого", "ему", "ому", "ыми", "ими", "ая", "яя", "ое", "ее", "ые", "ие", "ый", "ий", "ой", "ам", "ям", "ах", "ях", "ов", "ев", "ом", "ем", "ию", "ии", "ия", "ью", "я", "а", "у", "ы", "и", "е", "о"} {
		if strings.HasSuffix(word, suffix) {
			stem := strings.TrimSuffix(word, suffix)
			if len([]rune(stem)) >= 4 {
				return stem
			}
		}
	}
	return word
}

func matchesBuiltinTopic(filename, query string) bool {
	switch filename {
	case "fa_study_certificate.md":
		return strings.Contains(query, "справ") && !strings.Contains(query, "военком") && (strings.Contains(query, "обуч") || strings.Contains(query, "мфц") || strings.Contains(query, "месту требования"))
	case "fa_military_certificate.md":
		return strings.Contains(query, "военком") || strings.Contains(query, "воинск")
	case "fa_wifi.md":
		return strings.Contains(query, "wi-fi") || strings.Contains(query, "wifi") || strings.Contains(query, "вайфай") || strings.Contains(query, "вай-фай")
	case "fa_portal_password.md":
		return (strings.Contains(query, "парол") || strings.Contains(query, "доступ") || strings.Contains(query, "войти") || strings.Contains(query, "вход")) && (strings.Contains(query, "лк") || strings.Contains(query, "кабинет") || strings.Contains(query, "портал"))
	case "fa_business_trip.md":
		return strings.Contains(query, "командиров") && !strings.Contains(query, "авансов")
	case "fa_advance_report.md":
		return strings.Contains(query, "авансов") || strings.Contains(query, "подотч")
	}

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
	case "как", "что", "для", "при", "или", "после", "нужно", "получить", "инструкция", "регламент", "информация", "специалист", "помощь", "поддержка", "обращение", "ошибка", "ошибки", "ошибкой", "не", "работает", "проблема", "проблему", "срочно", "помогите", "сертификат":
		return true
	default:
		return false
	}
}

func (b *Base) Find(text, category string) *Document { return b.FindRelevant(text, category) }

// RuleID provides a stable numeric diagnostic identifier without logging rule
// text, title, or other potentially sensitive knowledge-base content.
func RuleID(filename string) uint32 {
	var id uint32 = 2166136261
	for i := 0; i < len(filename); i++ {
		id ^= uint32(filename[i])
		id *= 16777619
	}
	return id%999999 + 1
}

func loadDocument(path string) (Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Document{}, fmt.Errorf("read knowledge document %s: %w", path, err)
	}
	content := strings.ReplaceAll(string(data), "\r\n", "\n")
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

// ValidDepartmentName preserves organization-defined codes without allowing
// path components, markup or control characters into metadata/front matter.
func ValidDepartmentName(value string) bool {
	value = strings.TrimSpace(value)
	if len([]rune(value)) == 0 || len([]rune(value)) > 100 {
		return false
	}
	for _, r := range value {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != ' ' && r != '_' && r != '-' {
			return false
		}
	}
	return true
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
	case "academic", "учебный отдел":
		return "Academic"
	case "production", "производство":
		return "Production"
	case "supply", "поставки":
		return "Supply"
	case "security", "информационная безопасность":
		return "Security"
	case "general", "общее", "общее / без отдела", "не назначен", "unassigned", "":
		return "General"
	default:
		normalized := strings.TrimSpace(value)
		if ValidDepartmentName(normalized) {
			return normalized
		}
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

func (b *Base) UseManagedOnly() { b.mu.Lock(); b.managedOnly = true; b.mu.Unlock() }
func (b *Base) LegacyDocuments(tenant string) ([]Document, error) {
	base, err := b.fileTenantBase(tenant)
	if err != nil {
		return nil, err
	}
	return base.Documents(), nil
}
