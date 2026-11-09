package configstore

import (
	"context"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestModelRouterDefaultRouting pins what a router created without routing
// gets: off, global, and after every global router before it, so turning it
// on never puts it ahead of one already serving.
func TestModelRouterDefaultRouting(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("billing", "Billing")))
	billing, err := store.GetModelRouter(ctx, "billing")
	require.NoError(t, err)
	assert.Equal(t, &ModelRouterRouting{Scope: "global", Priority: 0}, billing.Routing)

	support := testModelRouter("support", "Support")
	support.Routing = &ModelRouterRouting{Enabled: true, Priority: 7}
	require.NoError(t, store.CreateModelRouter(ctx, support))
	require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("sales", "Sales")))
	sales, err := store.GetModelRouter(ctx, "sales")
	require.NoError(t, err)
	assert.False(t, sales.Routing.Enabled)
	assert.Equal(t, 8, sales.Routing.Priority, "a new router goes after the last global one")

	clash := testModelRouter("ops", "Ops")
	clash.Routing = &ModelRouterRouting{Priority: 7}
	require.ErrorIs(t, store.CreateModelRouter(ctx, clash), ErrModelRouterPriorityTaken)
}

// TestSetModelRouterRouting pins the routing save: it is validated and
// normalized, refuses a priority another router in the same scope holds, and
// is the only write that changes routing.
func TestSetModelRouterRouting(t *testing.T) {
	ctx := context.Background()
	teamRouting := ModelRouterRouting{
		Enabled:       true,
		Scope:         "team",
		ScopeID:       bifrost.Ptr(" team-1 "),
		CelExpression: ` model == "gpt-4o" `,
		Query:         map[string]any{"combinator": "and"},
		Priority:      3,
	}

	t.Run("saves and keeps it through config saves and resets", func(t *testing.T) {
		store := setupRDBTestStore(t)
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", "Support")))
		require.NoError(t, store.SetModelRouterRouting(ctx, "support", teamRouting))

		want := &ModelRouterRouting{
			Enabled:       true,
			Scope:         "team",
			ScopeID:       bifrost.Ptr("team-1"),
			CelExpression: `model == "gpt-4o"`,
			Query:         map[string]any{"combinator": "and"},
			Priority:      3,
		}
		got, err := store.GetModelRouter(ctx, "support")
		require.NoError(t, err)
		assert.Equal(t, want, got.Routing)

		update := testModelRouter("support", "Support desk")
		update.Routing = &ModelRouterRouting{Scope: "global"}
		require.NoError(t, store.UpdateModelRouter(ctx, update))
		got, err = store.GetModelRouter(ctx, "support")
		require.NoError(t, err)
		assert.Equal(t, want, got.Routing, "a config save leaves routing alone")

		reset, err := store.ResetModelRouter(ctx, "support", testComplexityAnalyzerConfig())
		require.NoError(t, err)
		assert.Equal(t, want, reset.Routing, "a reset leaves routing alone")
	})

	t.Run("priorities are unique within a scope", func(t *testing.T) {
		store := setupRDBTestStore(t)
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", "Support")))
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("billing", "Billing")))
		require.NoError(t, store.SetModelRouterRouting(ctx, "support", teamRouting))

		require.ErrorIs(t, store.SetModelRouterRouting(ctx, "billing", teamRouting), ErrModelRouterPriorityTaken)
		otherTeam := teamRouting
		otherTeam.ScopeID = bifrost.Ptr("team-2")
		require.NoError(t, store.SetModelRouterRouting(ctx, "billing", otherTeam), "another team's routers have their own priorities")
		require.NoError(t, store.SetModelRouterRouting(ctx, "support", teamRouting), "a router keeps its own priority")
	})

	t.Run("validation", func(t *testing.T) {
		store := setupRDBTestStore(t)
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", "Support")))
		cases := map[string]ModelRouterRouting{
			"unknown scope":     {Scope: "org"},
			"team without id":   {Scope: "team"},
			"negative priority": {Scope: "global", Priority: -1},
			"blank team id":     {Scope: "team", ScopeID: bifrost.Ptr("  ")},
		}
		for name, routing := range cases {
			t.Run(name, func(t *testing.T) {
				require.Error(t, store.SetModelRouterRouting(ctx, "support", routing))
			})
		}
		require.ErrorIs(t, store.SetModelRouterRouting(ctx, "missing", ModelRouterRouting{}), ErrNotFound)

		global := ModelRouterRouting{ScopeID: bifrost.Ptr("ignored"), Priority: 2}
		require.NoError(t, store.SetModelRouterRouting(ctx, "support", global))
		got, err := store.GetModelRouter(ctx, "support")
		require.NoError(t, err)
		assert.Equal(t, &ModelRouterRouting{Scope: "global", Priority: 2}, got.Routing, "an empty scope is global, which has no scope id")
	})
}

// TestGenerateModelRouterHashRouting pins that a config.json entry's routing
// is part of its hash only when the entry declares it, and that the
// condition builder state, which is never evaluated, is not.
func TestGenerateModelRouterHashRouting(t *testing.T) {
	base := *testModelRouter("support", "Support")
	withRouting := base
	withRouting.Routing = &ModelRouterRouting{Scope: "global", Priority: 1}
	assert.NotEqual(t, GenerateModelRouterHash(base), GenerateModelRouterHash(withRouting))

	enabled := withRouting
	enabled.Routing = &ModelRouterRouting{Enabled: true, Scope: "global", Priority: 1}
	assert.NotEqual(t, GenerateModelRouterHash(withRouting), GenerateModelRouterHash(enabled))

	withQuery := withRouting
	withQuery.Routing = &ModelRouterRouting{Scope: "global", Priority: 1, Query: map[string]any{"combinator": "and"}}
	assert.Equal(t, GenerateModelRouterHash(withRouting), GenerateModelRouterHash(withQuery))
}
