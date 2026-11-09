package complexity_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/routing"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/maximhq/bifrost/plugins/routing/rules"
)

// Texts the decision fake answers with a fixed tier.
const (
	complexText = "design a sharded cache with a write-ahead log"
	mediumText  = "refactor this module to remove the global state"
	simpleText  = "what does this regex match"
)

// modelRoutingDecision answers complexText, mediumText and simpleText with
// their tiers.
func modelRoutingDecision() *decisionRecorder {
	return &decisionRecorder{tiers: map[string]string{
		complexText: complexity.TierComplex,
		mediumText:  complexity.TierMedium,
		simpleText:  complexity.TierSimple,
	}}
}

// decisionModelRouter is a router that classifies with the decision model and
// sends each tier to the given targets.
func decisionModelRouter(id, name string, targets ...configstoreTables.TableModelRouterTarget) configstore.ModelRouter {
	return configstore.ModelRouter{ID: id, Name: name, ComplexityAnalyzerConfig: *decisionAnalyzerTestConfig(), Targets: targets}
}

// tierTarget is one target of a model router.
func tierTarget(tier, provider, model string) configstoreTables.TableModelRouterTarget {
	target := configstoreTables.TableModelRouterTarget{Tier: tier, Weight: 1}
	if provider != "" {
		target.Provider = schemas.Ptr(provider)
	}
	if model != "" {
		target.Model = schemas.Ptr(model)
	}
	return target
}

// tierRule is an enabled global model routing rule.
func tierRule(id, routerID string, priority int, cel string) configstoreTables.TableModelRoutingRule {
	return configstoreTables.TableModelRoutingRule{
		ID: id, Name: id, ModelRouterID: routerID, Scope: "global", Priority: priority, CelExpression: cel,
	}
}

// supportRouter sends SIMPLE to gpt-4o-mini and COMPLEX to claude-opus,
// leaving MEDIUM on the request's model.
func supportRouter(name string) configstore.ModelRouter {
	return decisionModelRouter("support", name,
		tierTarget(complexity.TierSimple, "openai", "gpt-4o-mini"),
		tierTarget(complexity.TierComplex, "anthropic", "claude-opus"))
}

// supportDeskRule sends every request to the support router.
func supportDeskRule() configstoreTables.TableModelRoutingRule {
	return tierRule("support-desk", "support", 0, "")
}

// tierRouterID is the router tierRouterRule makes answer complexity_tier.
const tierRouterID = "general"

// tierRouterRule makes a router with the targets answer complexity_tier, and
// sends every request to it.
func tierRouterRule(t *testing.T, plugin *routing.RoutingPlugin, targets ...configstoreTables.TableModelRouterTarget) {
	t.Helper()
	router := decisionModelRouter(tierRouterID, "General", targets...)
	router.AnswersComplexityTier = true
	require.NoError(t, plugin.ReloadModelRouter(&router))
	rule := tierRule("rule", tierRouterID, 0, "")
	require.NoError(t, plugin.ReloadModelRoutingRule(&rule))
}

// modelRoutingTestPlugin builds a routing plugin whose routers all classify
// with the decision fake, complexity_tier included before any router answers
// it, serving the given routing rules, routers, and model routing rules.
func modelRoutingTestPlugin(
	t *testing.T,
	governance *routing.MockGovernance,
	decision *decisionRecorder,
	routingRules []configstoreTables.TableRoutingRule,
	routers []configstore.ModelRouter,
	modelRules []configstoreTables.TableModelRoutingRule,
) *routing.RoutingPlugin {
	t.Helper()
	ctx := context.Background()
	logger := rules.NewMockLogger()
	ruleStore, err := rules.NewLocalStore(ctx, logger, nil)
	require.NoError(t, err)
	for i := range routingRules {
		require.NoError(t, ruleStore.UpsertRule(ctx, &routingRules[i]))
	}
	plugin, err := routing.InitFromStore(ctx, nil, logger, nil, ruleStore, governance)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })
	plugin.SetDecisionRequestExecutor(decision.execute)
	require.NoError(t, plugin.ReloadComplexityAnalyzerConfig(decisionAnalyzerTestConfig()))
	for i := range routers {
		require.NoError(t, plugin.ReloadModelRouter(&routers[i]))
	}
	for i := range modelRules {
		require.NoError(t, plugin.ReloadModelRoutingRule(&modelRules[i]))
	}
	return plugin
}

// runPreRequest runs the routing hook on a chat request for text.
func runPreRequest(t *testing.T, plugin *routing.RoutingPlugin, ctx *schemas.BifrostContext, text string) *schemas.BifrostRequest {
	t.Helper()
	req := llmComplexityChatRequest(text)
	require.NoError(t, plugin.PreRequestHook(ctx, req))
	return req
}

// newRequestContext returns an empty request context.
func newRequestContext() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}

// TestModelRoutingRuleRoutesByTier proves the step end to end: the matched
// rule's router classifies the request, the router's target for that tier replaces the
// provider and model before the virtual key allowlist and load balancing see
// them, and the request records the router, the rule and the tier.
func TestModelRoutingRuleRoutesByTier(t *testing.T) {
	governance := routing.NewMockGovernance()
	decision := modelRoutingDecision()
	plugin := modelRoutingTestPlugin(t, governance, decision, nil,
		[]configstore.ModelRouter{supportRouter("Support")},
		[]configstoreTables.TableModelRoutingRule{supportDeskRule()})

	ctx := newRequestContext()
	req := runPreRequest(t, plugin, ctx, complexText)

	provider, model, _ := req.GetRequestFields()
	assert.Equal(t, schemas.ModelProvider("anthropic"), provider)
	assert.Equal(t, "claude-opus", model)
	assert.Equal(t, []string{"claude-opus"}, governance.AllowlistModels, "the allowlist sees the model the router chose")
	assert.Equal(t, []string{"claude-opus"}, governance.LoadBalancedModels)

	assert.Equal(t, complexity.TierComplex, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))
	assert.Equal(t, complexity.MechanismDecision, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
	assert.Equal(t, "support", ctx.Value(schemas.BifrostContextKeyModelRouterID))
	assert.Equal(t, "Support", ctx.Value(schemas.BifrostContextKeyModelRouterName))
	assert.Equal(t, "support-desk", ctx.Value(schemas.BifrostContextKeyModelRoutingRuleID))
	assert.Equal(t, "support-desk", ctx.Value(schemas.BifrostContextKeyModelRoutingRuleName))
	engines, _ := ctx.Value(schemas.BifrostContextKeyRoutingEnginesUsed).([]string)
	assert.Contains(t, engines, schemas.RoutingEngineModelRouter)
	assert.Contains(t, routingLogs(ctx), "Model routing rule 'support-desk' → model router 'Support' tier=COMPLEX → provider=anthropic, model=claude-opus")
}

// TestModelRoutingRulesShareTheirRoutersTargets pins that the router, not the
// rule, owns the model each tier gets: two rules on one router route alike,
// one of them matching the model name an app asks for.
func TestModelRoutingRulesShareTheirRoutersTargets(t *testing.T) {
	byName := tierRule("by-name", "support", 1, `model == "support-assistant"`)
	plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), modelRoutingDecision(), nil,
		[]configstore.ModelRouter{supportRouter("Support")},
		[]configstoreTables.TableModelRoutingRule{byName, tierRule("by-team", "support", 2, `team_name == "Support"`)})

	ctx := newRequestContext()
	req := llmComplexityChatRequest(simpleText)
	req.ChatRequest.Model = "support-assistant"
	require.NoError(t, plugin.PreRequestHook(ctx, req))
	provider, model, _ := req.GetRequestFields()
	assert.Equal(t, schemas.ModelProvider("openai"), provider)
	assert.Equal(t, "gpt-4o-mini", model, "the router's SIMPLE model replaces the name the app asked for")
	assert.Equal(t, "by-name", ctx.Value(schemas.BifrostContextKeyModelRoutingRuleID))

	// New targets reach every rule on the router without touching the rules.
	require.NoError(t, plugin.SetModelRouterTargets("support", []configstoreTables.TableModelRouterTarget{
		tierTarget(complexity.TierSimple, "anthropic", "claude-haiku"),
	}))
	req = llmComplexityChatRequest(simpleText)
	req.ChatRequest.Model = "support-assistant"
	require.NoError(t, plugin.PreRequestHook(newRequestContext(), req))
	_, model, _ = req.GetRequestFields()
	assert.Equal(t, "claude-haiku", model)
}

// TestModelRoutingRuleKeepsModel pins the cases that leave the request on the
// model the routing rules chose: a tier the rule has no target for, no rule at
// all, and a rule naming a router that is not loaded.
func TestModelRoutingRuleKeepsModel(t *testing.T) {
	t.Run("tier without a model", func(t *testing.T) {
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), modelRoutingDecision(), nil,
			[]configstore.ModelRouter{supportRouter("Support")},
			[]configstoreTables.TableModelRoutingRule{supportDeskRule()})
		ctx := newRequestContext()
		req := runPreRequest(t, plugin, ctx, mediumText)
		_, model, _ := req.GetRequestFields()
		assert.Equal(t, "gpt-4o", model)
		assert.Equal(t, complexity.TierMedium, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityTier), "the tier is still recorded")
		assert.Equal(t, "support-desk", ctx.Value(schemas.BifrostContextKeyModelRoutingRuleID))
		assert.Contains(t, routingLogs(ctx), "has no model for tier=MEDIUM")
	})

	t.Run("no model routing rules", func(t *testing.T) {
		decision := modelRoutingDecision()
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), decision, nil,
			[]configstore.ModelRouter{supportRouter("Support")}, nil)
		ctx := newRequestContext()
		req := runPreRequest(t, plugin, ctx, complexText)
		_, model, _ := req.GetRequestFields()
		assert.Equal(t, "gpt-4o", model)
		assert.Zero(t, decision.calls(), "nothing asks a router to classify")
		assert.Nil(t, ctx.Value(schemas.BifrostContextKeyModelRouterID))
	})

	t.Run("router not loaded", func(t *testing.T) {
		decision := modelRoutingDecision()
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), decision, nil, nil,
			[]configstoreTables.TableModelRoutingRule{supportDeskRule()})
		ctx := newRequestContext()
		req := runPreRequest(t, plugin, ctx, complexText)
		_, model, _ := req.GetRequestFields()
		assert.Equal(t, "gpt-4o", model)
		assert.Zero(t, decision.calls())
		assert.Contains(t, routingLogs(ctx), `names model router "support", which is not loaded`)
	})
}

// TestModelRoutingRuleKeyPin pins the agreed pin rule: moving to another
// provider clears a key pinned for the old one, a target may pin its own, and
// staying on the provider keeps the pin.
func TestModelRoutingRuleKeyPin(t *testing.T) {
	run := func(t *testing.T, target configstoreTables.TableModelRouterTarget) *schemas.BifrostContext {
		t.Helper()
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), modelRoutingDecision(), nil, nil, nil)
		tierRouterRule(t, plugin, target)
		ctx := newRequestContext()
		ctx.SetValue(schemas.BifrostContextKeyRoutingPinnedAPIKeyID, "openai-key")
		runPreRequest(t, plugin, ctx, complexText)
		return ctx
	}

	ctx := run(t, tierTarget(complexity.TierComplex, "anthropic", "claude-opus"))
	assert.Nil(t, ctx.Value(schemas.BifrostContextKeyRoutingPinnedAPIKeyID), "an openai key cannot serve anthropic")

	pinned := tierTarget(complexity.TierComplex, "anthropic", "claude-opus")
	pinned.KeyID = schemas.Ptr("anthropic-key")
	ctx = run(t, pinned)
	assert.Equal(t, "anthropic-key", ctx.Value(schemas.BifrostContextKeyRoutingPinnedAPIKeyID))

	ctx = run(t, tierTarget(complexity.TierComplex, "", "gpt-4o-mini"))
	assert.Equal(t, "openai-key", ctx.Value(schemas.BifrostContextKeyRoutingPinnedAPIKeyID), "the same provider keeps its pin")
}

// TestModelRoutingRuleFallbacks pins the agreed fallback rule: a target's
// fallbacks replace the request's chain, and a target without any keeps it.
// A fallback without a model keeps the incoming one, as for routing rules.
func TestModelRoutingRuleFallbacks(t *testing.T) {
	run := func(t *testing.T, target configstoreTables.TableModelRouterTarget) []schemas.Fallback {
		t.Helper()
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), modelRoutingDecision(), nil, nil, nil)
		tierRouterRule(t, plugin, target)
		req := llmComplexityChatRequest(complexText)
		req.ChatRequest.Fallbacks = []schemas.Fallback{{Provider: schemas.OpenAI, Model: "gpt-4o-mini"}}
		require.NoError(t, plugin.PreRequestHook(newRequestContext(), req))
		_, _, fallbacks := req.GetRequestFields()
		return fallbacks
	}

	withFallbacks := tierTarget(complexity.TierComplex, "anthropic", "claude-opus")
	withFallbacks.ParsedFallbacks = []configstoreTables.RoutingFallback{{Fallback: schemas.Fallback{Provider: schemas.Anthropic, Model: "claude-sonnet"}}}
	assert.Equal(t, []schemas.Fallback{{Provider: schemas.Anthropic, Model: "claude-sonnet"}}, run(t, withFallbacks))
	assert.Equal(t, []schemas.Fallback{{Provider: schemas.OpenAI, Model: "gpt-4o-mini"}},
		run(t, tierTarget(complexity.TierComplex, "anthropic", "claude-opus")))

	modelless := tierTarget(complexity.TierComplex, "anthropic", "claude-opus")
	modelless.ParsedFallbacks = []configstoreTables.RoutingFallback{{Fallback: schemas.Fallback{Provider: schemas.Azure}}}
	assert.Equal(t, []schemas.Fallback{{Provider: schemas.Azure, Model: "gpt-4o"}}, run(t, modelless))
}

// TestModelRoutingRuleRunsAfterRoutingRules pins the order of the two steps: a
// model routing rule's condition sees the model a routing rule chose.
func TestModelRoutingRuleRunsAfterRoutingRules(t *testing.T) {
	routeToMini := configstoreTables.TableRoutingRule{
		ID: "to-mini", Name: "to-mini", CelExpression: `model == "gpt-4o"`, Enabled: schemas.Ptr(true), Scope: "global",
		Targets: []configstoreTables.TableRoutingTarget{{Model: schemas.Ptr("gpt-4o-mini"), Weight: 1}},
	}
	onMini := tierRule("on-mini", "general", 0, `model == "gpt-4o-mini"`)
	toOpus := []configstore.ModelRouter{decisionModelRouter("general", "General", tierTarget(complexity.TierComplex, "anthropic", "claude-opus"))}

	plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), modelRoutingDecision(),
		[]configstoreTables.TableRoutingRule{routeToMini}, toOpus, []configstoreTables.TableModelRoutingRule{onMini})
	req := runPreRequest(t, plugin, newRequestContext(), complexText)
	_, model, _ := req.GetRequestFields()
	assert.Equal(t, "claude-opus", model)

	plugin = modelRoutingTestPlugin(t, routing.NewMockGovernance(), modelRoutingDecision(), nil, toOpus,
		[]configstoreTables.TableModelRoutingRule{onMini})
	req = runPreRequest(t, plugin, newRequestContext(), complexText)
	_, model, _ = req.GetRequestFields()
	assert.Equal(t, "gpt-4o", model, "without the routing rule the condition does not hold")
}

// TestModelRoutingClassifiesOncePerRouter pins that a router asked by both a
// routing rule's complexity_tier and a model routing rule classifies once, and
// that the request records the last router asked.
func TestModelRoutingClassifiesOncePerRouter(t *testing.T) {
	complexToMini := configstoreTables.TableRoutingRule{
		ID: "complex-to-mini", Name: "complex-to-mini", CelExpression: `complexity_tier == "COMPLEX"`, Enabled: schemas.Ptr(true), Scope: "global",
		Targets: []configstoreTables.TableRoutingTarget{{Model: schemas.Ptr("gpt-4o-mini"), Weight: 1}},
	}

	t.Run("same router", func(t *testing.T) {
		decision := modelRoutingDecision()
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), decision,
			[]configstoreTables.TableRoutingRule{complexToMini}, nil, nil)
		tierRouterRule(t, plugin, tierTarget(complexity.TierComplex, "anthropic", "claude-opus"))
		ctx := newRequestContext()
		req := runPreRequest(t, plugin, ctx, complexText)
		_, model, _ := req.GetRequestFields()
		assert.Equal(t, "claude-opus", model)
		assert.Equal(t, 1, decision.calls(), "the complexity_tier router answers both steps once")
		assert.Equal(t, tierRouterID, ctx.Value(schemas.BifrostContextKeyModelRouterID))
		assert.Equal(t, complexity.MechanismDecision, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
	})

	t.Run("another router", func(t *testing.T) {
		decision := modelRoutingDecision()
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), decision,
			[]configstoreTables.TableRoutingRule{complexToMini},
			[]configstore.ModelRouter{supportRouter("Support")},
			[]configstoreTables.TableModelRoutingRule{supportDeskRule()})
		ctx := newRequestContext()
		runPreRequest(t, plugin, ctx, complexText)
		assert.Equal(t, 2, decision.calls(), "each router classifies once")
		assert.Equal(t, "support", ctx.Value(schemas.BifrostContextKeyModelRouterID), "the request records the last router asked")
	})
}

// TestModelRoutingSkipsLargePayload pins that a large payload, whose body is
// not parsed, has no text for a router and skips the step.
func TestModelRoutingSkipsLargePayload(t *testing.T) {
	decision := modelRoutingDecision()
	plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), decision, nil, nil, nil)
	tierRouterRule(t, plugin, tierTarget(complexity.TierSimple, "anthropic", "claude-haiku"))
	ctx := newRequestContext()
	metadata := &schemas.LargePayloadMetadata{Model: "gpt-4o"}
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadMetadata, metadata)
	require.NoError(t, plugin.PreRequestHook(ctx, &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{}}))
	assert.Equal(t, "gpt-4o", metadata.Model)
	assert.Zero(t, decision.calls())
	assert.Nil(t, ctx.Value(schemas.BifrostContextKeyModelRoutingRuleID))
}

// TestModelRouterReloadAndRemove pins the plugin's router lifecycle: a reload
// renames a served router and a removed router stops being served.
func TestModelRouterReloadAndRemove(t *testing.T) {
	plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), modelRoutingDecision(), nil,
		[]configstore.ModelRouter{supportRouter("Support")},
		[]configstoreTables.TableModelRoutingRule{supportDeskRule()})

	renamed := supportRouter("Customer support")
	require.NoError(t, plugin.ReloadModelRouter(&renamed))
	ctx := newRequestContext()
	runPreRequest(t, plugin, ctx, complexText)
	assert.Equal(t, "Customer support", ctx.Value(schemas.BifrostContextKeyModelRouterName))

	_, ok := plugin.ModelRouterSemanticStatus("support")
	assert.True(t, ok)
	require.NoError(t, plugin.RemoveModelRouter("support"))
	_, ok = plugin.ModelRouterSemanticStatus("support")
	assert.False(t, ok)
	ctx = newRequestContext()
	req := runPreRequest(t, plugin, ctx, complexText)
	_, model, _ := req.GetRequestFields()
	assert.Equal(t, "gpt-4o", model, "a rule naming a removed router keeps the model")

	require.NoError(t, plugin.RemoveModelRouter("missing"), "removing a router not served is a no-op")

	plugin.RemoveModelRoutingRule("support-desk")
	ctx = newRequestContext()
	runPreRequest(t, plugin, ctx, complexText)
	assert.Nil(t, ctx.Value(schemas.BifrostContextKeyModelRoutingRuleID), "a removed rule no longer matches")
}

// TestComplexityTierFollowsTheFlaggedRouter pins that complexity_tier is
// answered by whichever router has the flag: a router that takes it keeps
// serving its model routing rules, the router that loses it moves to a
// runtime of its own and keeps serving its rules too, and with no router
// flagged, or the flagged one removed, complexity_tier runs on the built-in
// classifier and no router is recorded for it.
func TestComplexityTierFollowsTheFlaggedRouter(t *testing.T) {
	complexToMini := configstoreTables.TableRoutingRule{
		ID: "complex-to-mini", Name: "complex-to-mini", CelExpression: `complexity_tier == "COMPLEX"`, Enabled: schemas.Ptr(true), Scope: "global",
		Targets: []configstoreTables.TableRoutingTarget{{Model: schemas.Ptr("gpt-4o-mini"), Weight: 1}},
	}
	decision := modelRoutingDecision()
	plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), decision,
		[]configstoreTables.TableRoutingRule{complexToMini}, []configstore.ModelRouter{supportRouter("Support")}, nil)
	// tierAsked runs a request and returns the router recorded for it and
	// the model the complexity_tier rule left it on.
	tierAsked := func(t *testing.T) (any, string) {
		t.Helper()
		ctx := newRequestContext()
		req := runPreRequest(t, plugin, ctx, complexText)
		_, model, _ := req.GetRequestFields()
		return ctx.Value(schemas.BifrostContextKeyModelRouterID), model
	}

	asked, _ := tierAsked(t)
	assert.Nil(t, asked, "no router answers complexity_tier yet")

	support := supportRouter("Support")
	support.AnswersComplexityTier = true
	require.NoError(t, plugin.ReloadModelRouter(&support))
	asked, model := tierAsked(t)
	assert.Equal(t, "support", asked)
	assert.Equal(t, "gpt-4o-mini", model, "the router's tier drives the complexity_tier rule")
	_, ok := plugin.ModelRouterSemanticStatus("support")
	assert.True(t, ok, "the router is still served by its id")

	billing := decisionModelRouter("billing", "Billing")
	billing.AnswersComplexityTier = true
	require.NoError(t, plugin.ReloadModelRouter(&billing))
	asked, model = tierAsked(t)
	assert.Equal(t, "billing", asked, "the flag moved")
	assert.Equal(t, "gpt-4o-mini", model)
	rule := supportDeskRule()
	require.NoError(t, plugin.ReloadModelRoutingRule(&rule))
	ctx := newRequestContext()
	req := runPreRequest(t, plugin, ctx, simpleText)
	_, model, _ = req.GetRequestFields()
	assert.Equal(t, "gpt-4o-mini", model, "the router that lost the flag keeps its targets")
	assert.Equal(t, "Support", ctx.Value(schemas.BifrostContextKeyModelRouterName), "and its name")
	plugin.RemoveModelRoutingRule("support-desk")

	billing.AnswersComplexityTier = false
	require.NoError(t, plugin.ReloadModelRouter(&billing))
	asked, _ = tierAsked(t)
	assert.Nil(t, asked, "turning the flag off leaves complexity_tier on the built-in classifier")
	_, ok = plugin.ModelRouterSemanticStatus("billing")
	assert.True(t, ok, "the router is still served")

	billing.AnswersComplexityTier = true
	require.NoError(t, plugin.ReloadModelRouter(&billing))
	require.NoError(t, plugin.RemoveModelRouter("billing"))
	asked, _ = tierAsked(t)
	assert.Nil(t, asked, "removing the flagged router leaves the built-in classifier")
	_, ok = plugin.ModelRouterSemanticStatus("billing")
	assert.False(t, ok)
}
