package routing

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/kvstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/maximhq/bifrost/plugins/routing/rules"
)

// TestComplexityStateKeysScopeModelRouters pins that each model router keeps
// its own session and continuation state, while no router and the router the
// complexity config was moved into keep the keys written before routers had
// names, so state in flight survives the upgrade.
func TestComplexityStateKeysScopeModelRouters(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeySessionID, "session-1")

	require.Equal(t, bifrost.SessionStateKey(ctx, complexitySessionKind), complexitySessionKeyFor(ctx, configstore.LegacyComplexityRouterID))
	require.Equal(t, bifrost.SessionStateKey(ctx, complexityTurnTierKind), complexityTurnTierKeyFor(ctx, configstore.LegacyComplexityRouterID))
	require.Equal(t, complexitySessionKey(ctx), complexitySessionKeyFor(ctx, ""), "no router keeps the old keys")

	support, billing := complexitySessionKeyFor(ctx, "support"), complexitySessionKeyFor(ctx, "billing")
	assert.NotEqual(t, complexitySessionKey(ctx), support)
	assert.NotEqual(t, support, billing)
	assert.NotEqual(t, complexityTurnTierKey(ctx), complexityTurnTierKeyFor(ctx, "support"))
}

// TestRoutingPluginServesStoredModelRouters pins the boot path: a plugin
// built over a config store serves the routers and model routing rules it
// holds, and serves the one that answers complexity_tier on its own
// classifiers, under that router's id, name and targets.
func TestRoutingPluginServesStoredModelRouters(t *testing.T) {
	ctx := context.Background()
	logger := rules.NewMockLogger()
	store, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: t.TempDir() + "/config.db"},
	}, logger)
	require.NoError(t, err)
	t.Cleanup(func() { store.Close(ctx) })

	keywords := complexity.EditableKeywordConfig{
		SimpleKeywords: []string{"papaya amber"}, MediumKeywords: []string{"cedar cobalt"}, ComplexKeywords: []string{"obsidian comet"},
	}
	require.NoError(t, store.CreateModelRouter(ctx, &configstore.ModelRouter{
		ID: "general", Name: "General", AnswersComplexityTier: true,
		ComplexityAnalyzerConfig: complexity.AnalyzerConfig{Classifier: complexity.ClassifierDecision, Keywords: keywords},
	}))
	require.NoError(t, store.CreateModelRouter(ctx, &configstore.ModelRouter{
		ID: "support", Name: "Support",
		ComplexityAnalyzerConfig: complexity.AnalyzerConfig{Classifier: complexity.ClassifierDecision, Keywords: keywords},
	}))
	require.NoError(t, store.SetModelRouterTargets(ctx, "support", []configstoreTables.TableModelRouterTarget{
		{Tier: complexity.TierSimple, Model: bifrost.Ptr("gpt-4o-mini"), Weight: 1},
	}))
	require.NoError(t, store.SetModelRouterTargets(ctx, "general", []configstoreTables.TableModelRouterTarget{
		{Tier: complexity.TierComplex, Model: bifrost.Ptr("gpt-4o"), Weight: 1},
	}))
	require.NoError(t, store.CreateModelRoutingRule(ctx, &configstoreTables.TableModelRoutingRule{
		ID: "support-desk", Name: "Support desk", ModelRouterID: "support", Scope: "global",
	}))

	ruleStore, err := rules.NewLocalStore(ctx, logger, store)
	require.NoError(t, err)
	plugin, err := InitFromStore(ctx, nil, logger, store, ruleStore, NewMockGovernance())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })

	rt, ok := plugin.modelRouter("support")
	require.True(t, ok)
	assert.Equal(t, "Support", rt.displayName())
	assert.Equal(t, complexity.ClassifierDecision, rt.config.Load().Classifier)
	assert.Equal(t, "general", plugin.complexityTierRouterID())
	assert.Equal(t, "General", plugin.complexityTierRouter().displayName())
	assert.Equal(t, complexity.ClassifierDecision, plugin.complexityConfig.Load().Classifier, "its config is the plugin's own")
	_, extra := plugin.extraModelRouters()["general"]
	assert.False(t, extra, "it gets no second runtime")
	assert.True(t, plugin.modelRules.HasRules())
	require.Len(t, rt.loadTargets(), 1, "a router is served with its targets")
	assert.Equal(t, "gpt-4o-mini", *rt.loadTargets()[0].Model)
	require.Len(t, plugin.complexityTierRouter().loadTargets(), 1, "so is the complexity_tier router")
	assert.Equal(t, "gpt-4o", *plugin.complexityTierRouter().loadTargets()[0].Model)

	// Replacing a router's targets leaves its classifiers alone.
	config := rt.config.Load()
	require.NoError(t, plugin.SetModelRouterTargets("support", nil))
	assert.Empty(t, rt.loadTargets())
	assert.Same(t, config, rt.config.Load())
	assert.ErrorIs(t, plugin.SetModelRouterTargets("missing", nil), configstore.ErrNotFound)
}

// TestPersistComplexityTurnTierKeepsEveryRouter pins that a turn in which a
// routing rule's complexity_tier asked one router and a model routing rule
// asked another saves both tiers, so a tool continuation resumes each router on the
// tier it gave, not on the last router's.
func TestPersistComplexityTurnTierKeepsEveryRouter(t *testing.T) {
	store, err := kvstore.New(kvstore.Config{CleanupInterval: time.Hour})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	plugin := &RoutingPlugin{turnTierStore: &complexityTurnTierStore{store: store}, logger: rules.NewMockLogger()}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeySessionID, "session-1")
	cacheRouterTier(ctx, configstore.LegacyComplexityRouterID, &complexity.ComplexityResult{Tier: complexity.TierComplex})
	cacheRouterTier(ctx, "support", &complexity.ComplexityResult{Tier: complexity.TierSimple})
	cacheRouterTier(ctx, "billing", nil)

	plugin.persistComplexityTurnTier(ctx)

	for routerID, want := range map[string]string{configstore.LegacyComplexityRouterID: complexity.TierComplex, "support": complexity.TierSimple} {
		tier, found, err := plugin.turnTierStore.load(complexityTurnTierKeyFor(ctx, routerID))
		require.NoError(t, err)
		require.True(t, found, routerID)
		assert.Equal(t, want, tier, routerID)
	}
	_, found, err := plugin.turnTierStore.load(complexityTurnTierKeyFor(ctx, "billing"))
	require.NoError(t, err)
	assert.False(t, found, "a router that produced no tier saves nothing")
}
