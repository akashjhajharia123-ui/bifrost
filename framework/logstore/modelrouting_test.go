package logstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestModelRoutingColumnsFilter pins that a log records the model router and
// model routing rule that routed it, and that both filter the log list from
// the raw table rather than the hourly view, which does not carry them.
func TestModelRoutingColumnsFilter(t *testing.T) {
	store, _ := newFanoutTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	row := func(id, router, routerName, rule, ruleName string) {
		opt := func(v string) *string {
			if v == "" {
				return nil
			}
			return &v
		}
		cost := 1.0
		require.NoError(t, store.Create(ctx, &Log{
			ID: id, Timestamp: base, CreatedAt: base, Object: "chat_completion", Provider: "openai", Model: "m",
			Status: "success", Cost: &cost, TotalTokens: 10,
			ModelRouterID: opt(router), ModelRouterName: opt(routerName),
			ModelRoutingRuleID: opt(rule), ModelRoutingRuleName: opt(ruleName),
		}))
	}
	row("a", "support", "Support", "rule-desk", "Support desk")
	row("b", "support", "Support", "rule-desk", "Support desk")
	// A routing rule's complexity_tier is classified by the router that
	// answers it, with no model routing rule involved.
	row("c", "general", "General", "", "")
	row("d", "", "", "", "")

	start, end := base.Add(-time.Hour), base.Add(time.Hour)
	filters := SearchFilters{StartTime: &start, EndTime: &end}
	search := func(f SearchFilters) []string {
		res, err := store.SearchLogs(ctx, f, PaginationOptions{Limit: 10})
		require.NoError(t, err)
		ids := make([]string, 0, len(res.Logs))
		for _, l := range res.Logs {
			ids = append(ids, l.ID)
		}
		return ids
	}

	byRouter := filters
	byRouter.ModelRouterIDs = []string{"support"}
	assert.ElementsMatch(t, []string{"a", "b"}, search(byRouter))
	byRule := filters
	byRule.ModelRoutingRuleIDs = []string{"rule-desk"}
	assert.ElementsMatch(t, []string{"a", "b"}, search(byRule))

	res, err := store.SearchLogs(ctx, byRouter, PaginationOptions{Limit: 1})
	require.NoError(t, err)
	require.Len(t, res.Logs, 1)
	got := res.Logs[0]
	require.NotNil(t, got.ModelRouterName)
	assert.Equal(t, "Support", *got.ModelRouterName)
	require.NotNil(t, got.ModelRoutingRuleName)
	assert.Equal(t, "Support desk", *got.ModelRoutingRuleName)

	assert.False(t, canUseMatViewFilters(byRouter), "a router filter must not be served from the hourly view")
	assert.False(t, canUseMatViewFilters(byRule), "a rule filter must not be served from the hourly view")

	// The Model Routers page ranks requests by router.
	assert.True(t, ValidRankingDimensions[RankingDimensionModelRouter], "the rankings endpoint accepts model_router")
	rankings, err := store.GetDimensionRankings(ctx, filters, RankingDimensionModelRouter)
	require.NoError(t, err)
	counts := make(map[string]int64, len(rankings.Rankings))
	names := make(map[string]string, len(rankings.Rankings))
	for _, entry := range rankings.Rankings {
		counts[entry.ID], names[entry.ID] = entry.TotalRequests, entry.Name
	}
	assert.Equal(t, map[string]int64{"support": 2, "general": 1}, counts, "a request no router classified is not ranked")
	assert.Equal(t, "Support", names["support"])
}

// TestModelRoutingColumnsMigration_NonRollbackable pins that rolling
// logs_add_model_routing_columns back is refused while any log records a
// router or rule: that attribution cannot be recaptured, so the drop would
// delete data rather than reverse a schema change.
func TestModelRoutingColumnsMigration_NonRollbackable(t *testing.T) {
	ctx := context.Background()
	store, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "model-routing.db")}, testLogger{})
	require.NoError(t, err)
	db := store.db
	now := time.Now().UTC()
	require.NoError(t, db.Create(&Log{ID: "log-1", Timestamp: now, CreatedAt: now, Object: "chat_completion", Provider: "openai", Model: "m", Status: "success"}).Error)
	require.NoError(t, db.Model(&Log{}).Where("id = ?", "log-1").Update("model_routing_rule_name", "Support desk").Error)

	err = modelRoutingColumnsMigration(ctx, testLogger{}).Rollback(db)
	require.Error(t, err, "rollback must refuse while a log records a model routing rule")
	assert.Contains(t, err.Error(), "non-rollbackable")
	for _, column := range modelRoutingColumns {
		assert.True(t, db.Migrator().HasColumn(&Log{}, column), "logs.%s survives a refused rollback", column)
	}

	require.NoError(t, db.Model(&Log{}).Where("id = ?", "log-1").Update("model_routing_rule_name", nil).Error)
	require.NoError(t, modelRoutingColumnsMigration(ctx, testLogger{}).Rollback(db), "with nothing recorded the rollback is a genuine reversal")
	assert.False(t, db.Migrator().HasColumn(&Log{}, "model_router_id"))
}
