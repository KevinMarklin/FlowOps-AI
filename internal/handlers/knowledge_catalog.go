package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// WithGovernedWrites retires direct publication. Existing active files retain
// their historical publication state with explicit provenance, once per tenant.
func (h *KnowledgeHandler) WithGovernedWrites() *KnowledgeHandler {
	h.governed = true
	h.catalogImported = make(map[string]bool)
	h.base.UseManagedOnly()
	return h
}
func (h *KnowledgeHandler) importLegacyCatalog(ctx context.Context, tenant string) error {
	h.catalogMu.Lock()
	defer h.catalogMu.Unlock()
	if h.catalogImported[tenant] {
		return nil
	}
	docs, err := h.base.LegacyDocuments(tenant)
	if err != nil {
		return err
	}
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, doc := range docs {
		sum := sha256.Sum256([]byte("legacy\x00" + tenant + "\x00" + doc.Filename))
		id := hex.EncodeToString(sum[:16])
		digest := sha256.Sum256([]byte(doc.Content))
		document := ArticleDocument{Title: doc.Title, Department: doc.Department, Keywords: doc.Keywords, Content: doc.Content, Kind: doc.Kind, ExpiresAt: doc.ExpiresAt, ReviewDue: doc.ExpiresAt, Sources: []KnowledgeSource{{Type: "import", ID: "legacy-file:" + doc.Filename, Digest: hex.EncodeToString(digest[:])}}}
		state := "published"
		var published any = int64(1)
		if doc.IncidentState == "resolved" || (doc.ExpiresAt != nil && doc.ExpiresAt.Before(time.Now())) {
			state = "stale"
			published = nil
		}
		raw, err := json.Marshal(document)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO kb_articles(id,tenant_id,author_id,state,published_revision,document,approved_by) VALUES($1,$2,'legacy-import',$3,$4,$5,'inherited-existing-publication') ON CONFLICT DO NOTHING`, id, tenant, state, published, raw)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		if _, err = tx.Exec(ctx, `INSERT INTO kb_article_versions(tenant_id,article_id,revision,state,document,actor) VALUES($1,$2,1,$3,$4,'legacy-import')`, tenant, id, state, raw); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO audit_logs(tenant_id,event_type,actor,details) VALUES($1,'KB_LEGACY_IMPORTED','system',$2)`, tenant, fmt.Sprintf("%s: inherited existing publication; no new approval claimed", id)); err != nil {
			return err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	h.catalogImported[tenant] = true
	return nil
}
