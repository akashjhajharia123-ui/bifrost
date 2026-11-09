package configstore

import (
	"context"
	"errors"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testModelRoutingRule returns a valid global rule that sends every request
// to routerID.
func testModelRoutingRule(id, name, routerID string, priority int) *tables.TableModelRoutingRule {
	return &tables.TableModelRoutingRule{
		ID:            id,
		Name:          name,
		ModelRouterID: routerID,
		Scope:         "global",
		Priority:      priority,
	}
}

// testRuleRouterID is the router setupModelRoutingRuleStore saves for rules to name.
const testRuleRouterID = "general"

// setupModelRoutingRuleStore is a store holding one router, testRuleRouterID,
// since a rule can only name a router that exists.
func setupModelRoutingRuleStore(t *testing.T) *RDBConfigStore {
	t.Helper()
	store := setupRDBTestStore(t)
	require.NoError(t, store.CreateModelRouter(context.Background(), testModelRouter(testRuleRouterID, "General")))
	return store
}

// TestModelRoutingRuleStoreCRUD covers a rule's life: created with its builder
// state, read back, updated, and deleted.
func TestModelRoutingRuleStoreCRUD(t *testing.T) {
	store := setupModelRoutingRuleStore(t)
	ctx := context.Background()

	rule := testModelRoutingRule("rule-1", " Support desk ", testRuleRouterID, 0)
	rule.ParsedQuery = map[string]any{"combinator": "and", "rules": []any{}}
	require.NoError(t, store.CreateModelRoutingRule(ctx, rule))

	got, err := store.GetModelRoutingRule(ctx, "rule-1")
	require.NoError(t, err)
	assert.Equal(t, "Support desk", got.Name)
	assert.Equal(t, map[string]any{"combinator": "and", "rules": []any{}}, got.ParsedQuery, "the builder state round-trips")
	assert.True(t, got.EnabledValue())

	update := testModelRoutingRule("rule-1", "Support desk", testRuleRouterID, 0)
	update.Enabled = bifrost.Ptr(false)
	update.CelExpression = `model == "engineering-assistant"`
	require.NoError(t, store.UpdateModelRoutingRule(ctx, update))
	got, err = store.GetModelRoutingRule(ctx, "rule-1")
	require.NoError(t, err)
	assert.False(t, got.EnabledValue())
	assert.Equal(t, `model == "engineering-assistant"`, got.CelExpression)
	created := got.CreatedAt

	// An omitted enabled keeps the stored value, and created_at never moves.
	update.Enabled = nil
	require.NoError(t, store.UpdateModelRoutingRule(ctx, update))
	got, err = store.GetModelRoutingRule(ctx, "rule-1")
	require.NoError(t, err)
	assert.False(t, got.EnabledValue())
	assert.True(t, created.Equal(got.CreatedAt))

	require.NoError(t, store.DeleteModelRoutingRule(ctx, "rule-1"))
	_, err = store.GetModelRoutingRule(ctx, "rule-1")
	require.ErrorIs(t, err, ErrNotFound)
	require.ErrorIs(t, store.DeleteModelRoutingRule(ctx, "rule-1"), ErrNotFound)
	require.ErrorIs(t, store.UpdateModelRoutingRule(ctx, update), ErrNotFound)
}

// TestModelRoutingRuleValidation pins what a rule is checked for before it is
// stored.
func TestModelRoutingRuleValidation(t *testing.T) {
	store := setupModelRoutingRuleStore(t)
	ctx := context.Background()

	cases := map[string]func(r *tables.TableModelRoutingRule){
		"missing name":           func(r *tables.TableModelRoutingRule) { r.Name = " " },
		"unknown scope":          func(r *tables.TableModelRoutingRule) { r.Scope = "org" },
		"scope without scope_id": func(r *tables.TableModelRoutingRule) { r.Scope = "team" },
		"missing router":         func(r *tables.TableModelRoutingRule) { r.ModelRouterID = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			rule := testModelRoutingRule("rule-1", "Support desk", testRuleRouterID, 0)
			mutate(rule)
			require.Error(t, store.CreateModelRoutingRule(ctx, rule))
		})
	}

	t.Run("global scope_id is dropped", func(t *testing.T) {
		rule := testModelRoutingRule("rule-2", "Normalized", testRuleRouterID, 1)
		rule.ScopeID = bifrost.Ptr("ignored")
		require.NoError(t, store.CreateModelRoutingRule(ctx, rule))
		got, err := store.GetModelRoutingRule(ctx, "rule-2")
		require.NoError(t, err)
		assert.Nil(t, got.ScopeID)
	})
}

// TestModelRoutingRuleReferences pins the cross-checks a write makes: the
// router must exist, and the priority must be free within the rule's scope
// among model routing rules only.
func TestModelRoutingRuleReferences(t *testing.T) {
	ctx := context.Background()

	t.Run("router must exist", func(t *testing.T) {
		store := setupRDBTestStore(t)
		require.ErrorIs(t, store.CreateModelRoutingRule(ctx, testModelRoutingRule("rule-1", "One", "support", 0)), ErrUnknownModelRouter)
		require.ErrorIs(t, store.CreateModelRoutingRule(ctx, testModelRoutingRule("rule-2", "Two", LegacyComplexityRouterID, 1)), ErrUnknownModelRouter,
			"no router exists without being saved")
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", "Support")))
		require.NoError(t, store.CreateModelRoutingRule(ctx, testModelRoutingRule("rule-1", "One", "support", 0)))
	})

	t.Run("priority is unique per scope", func(t *testing.T) {
		store := setupModelRoutingRuleStore(t)
		require.NoError(t, store.CreateModelRoutingRule(ctx, testModelRoutingRule("rule-1", "One", testRuleRouterID, 0)))
		err := store.CreateModelRoutingRule(ctx, testModelRoutingRule("rule-2", "Two", testRuleRouterID, 0))
		require.ErrorIs(t, err, ErrModelRoutingRulePriorityTaken)
		require.ErrorIs(t, err, ErrAlreadyExists)

		team := testModelRoutingRule("rule-3", "Team", testRuleRouterID, 0)
		team.Scope, team.ScopeID = "team", bifrost.Ptr("team-1")
		require.NoError(t, store.CreateModelRoutingRule(ctx, team), "another scope has its own priorities")
	})

	t.Run("priorities are separate from routing rules", func(t *testing.T) {
		store := setupModelRoutingRuleStore(t)
		require.NoError(t, store.CreateRoutingRule(ctx, &tables.TableRoutingRule{
			ID: "routing-1", Name: "Routing", Scope: "global", Priority: 0, CelExpression: "true",
			Targets: []tables.TableRoutingTarget{{Model: bifrost.Ptr("gpt-4o"), Weight: 1}},
		}))
		require.NoError(t, store.CreateModelRoutingRule(ctx, testModelRoutingRule("rule-1", "Routing", testRuleRouterID, 0)),
			"a routing rule and a model routing rule may share a scope, a name and a priority")
	})
}

// TestDeleteModelRouterInUse pins that a router model routing rules name
// cannot be deleted, and that the error names those rules.
func TestDeleteModelRouterInUse(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", "Support")))
	require.NoError(t, store.CreateModelRoutingRule(ctx, testModelRoutingRule("rule-1", "Support desk", "support", 0)))
	require.NoError(t, store.CreateModelRoutingRule(ctx, testModelRoutingRule("rule-2", "After hours", "support", 1)))

	err := store.DeleteModelRouter(ctx, "support")
	require.ErrorIs(t, err, ErrModelRouterInUse)
	var inUse *ModelRouterInUseError
	require.True(t, errors.As(err, &inUse))
	assert.Equal(t, []string{"After hours", "Support desk"}, inUse.RuleNames)
	_, err = store.GetModelRouter(ctx, "support")
	require.NoError(t, err, "a refused delete leaves the router in place")

	require.NoError(t, store.DeleteModelRoutingRule(ctx, "rule-1"))
	require.NoError(t, store.DeleteModelRoutingRule(ctx, "rule-2"))
	require.NoError(t, store.DeleteModelRouter(ctx, "support"))
}

// TestGenerateModelRoutingRuleHash pins that a rule's hash moves with any
// field the file can change.
func TestGenerateModelRoutingRuleHash(t *testing.T) {
	base := testModelRoutingRule("rule-1", "Support desk", testRuleRouterID, 0)
	hash, err := GenerateModelRoutingRuleHash(*base)
	require.NoError(t, err)

	changes := map[string]func(r *tables.TableModelRoutingRule){
		"router":    func(r *tables.TableModelRoutingRule) { r.ModelRouterID = "support" },
		"condition": func(r *tables.TableModelRoutingRule) { r.CelExpression = `model == "gpt-4o"` },
		"priority":  func(r *tables.TableModelRoutingRule) { r.Priority = 5 },
		"enabled":   func(r *tables.TableModelRoutingRule) { r.Enabled = bifrost.Ptr(false) },
	}
	for name, mutate := range changes {
		t.Run(name, func(t *testing.T) {
			rule := testModelRoutingRule("rule-1", "Support desk", testRuleRouterID, 0)
			mutate(rule)
			changed, err := GenerateModelRoutingRuleHash(*rule)
			require.NoError(t, err)
			assert.NotEqual(t, hash, changed)
		})
	}
}

// TestMigrationAddModelRoutingRulesTables pins that the migration creates the
// rules and router targets tables and runs once.
func TestMigrationAddModelRoutingRulesTables(t *testing.T) {
	ctx := context.Background()
	db := setupModelRouterMigrationDB(t)
	require.NoError(t, migrationAddModelRoutingRulesTables(ctx, db, testMigrationLogger))
	require.True(t, db.Migrator().HasTable(&tables.TableModelRoutingRule{}))
	require.True(t, db.Migrator().HasTable(&tables.TableModelRouterTarget{}))
	require.NoError(t, migrationAddModelRoutingRulesTables(ctx, db, testMigrationLogger))
}

// TestMigrationAddModelRoutingRuleQueryColumn pins that a table created
// before the query column existed gets it, and that a table that already has
// it is left as it is.
func TestMigrationAddModelRoutingRuleQueryColumn(t *testing.T) {
	ctx := context.Background()
	db := setupModelRouterMigrationDB(t)
	require.NoError(t, migrationAddModelRoutingRulesTables(ctx, db, testMigrationLogger))
	require.NoError(t, db.Migrator().DropColumn(&tables.TableModelRoutingRule{}, "Query"))

	require.NoError(t, migrationAddModelRoutingRuleQueryColumn(ctx, db, testMigrationLogger))
	assert.True(t, db.Migrator().HasColumn(&tables.TableModelRoutingRule{}, "Query"))
	require.NoError(t, migrationAddModelRoutingRuleQueryColumn(ctx, db, testMigrationLogger))
}
