package analyzer

import (
	"context"
	"flowops-ai/internal/security"
	"flowops-ai/internal/settings"
	"github.com/jackc/pgx/v5/pgxpool"
)

func (a *Analyzer) WithAIPolicy(db *pgxpool.Pool) *Analyzer { a.policyDB = db; return a }
func (a *Analyzer) aiPolicy(ctx context.Context) (settings.AIPolicy, error) {
	if a.policyDB == nil {
		return settings.AIPolicy{}, nil
	}
	cfg, err := settings.LoadPlatform(ctx, a.policyDB, security.TenantID(ctx))
	return cfg.AI, err
}
func (a *Analyzer) permitGeneration(ctx context.Context) bool {
	p, err := a.aiPolicy(ctx)
	if err != nil || p.Mode == "manual" {
		return false
	}
	if a.policyDB == nil || p.DailyLimit == 0 {
		return true
	}
	var n int
	err = a.policyDB.QueryRow(ctx, `INSERT INTO organization_ai_usage(tenant_id,day,calls) VALUES($1,(NOW() AT TIME ZONE 'UTC')::date,1) ON CONFLICT(tenant_id,day) DO UPDATE SET calls=organization_ai_usage.calls+1 WHERE organization_ai_usage.calls<$2 RETURNING calls`, security.TenantID(ctx), p.DailyLimit).Scan(&n)
	return err == nil
}
func (a *Analyzer) applyAIPolicy(ctx context.Context, r Result) Result {
	p, err := a.aiPolicy(ctx)
	if err != nil || p.Mode == "manual" {
		r.Department = "Unassigned"
		r.Priority = "medium"
		r.RAGAnswer = nil
		return r
	}
	if p.Mode == "assistant" || settings.Disabled(p.Classification) {
		r.Department = "Unassigned"
		r.Priority = "medium"
	}
	if settings.Disabled(p.Routing) {
		r.Department = "Unassigned"
	}
	if settings.Disabled(p.Prioritization) {
		r.Priority = "medium"
	}
	if p.Mode == "assistant" || settings.Disabled(p.Answers) {
		r.RAGAnswer = nil
	}
	if r.RAGAnswer != nil && len(p.Sources) > 0 {
		permitted := false
		if base, loadErr := a.TenantKnowledge(ctx); loadErr == nil {
			if document := base.FindRelevant(r.ExtractedSummary, r.Department); document != nil {
				for _, source := range p.Sources {
					if source == document.Filename {
						permitted = true
						break
					}
				}
			}
		}
		if !permitted {
			r.RAGAnswer = nil
		} else if p.Mode == "automatic" && settings.Disabled(p.RequireConfirmation) {
			r.AutoResolve = true
		}
	}

	return r
}

// acquireGovernedSlot reserves daily usage only after local capacity is available.
func (a *Analyzer) acquireGovernedSlot(ctx context.Context) bool {
	if !a.acquireModelSlot() {
		return false
	}
	if !a.permitGeneration(ctx) {
		<-a.llmSlots
		return false
	}
	return true
}
