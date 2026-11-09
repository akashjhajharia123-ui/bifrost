package lib

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// bootFromJSON runs the governance and model router loaders against store in
// boot order, parsing raw so section presence comes from the file itself.
func bootFromJSON(t *testing.T, store configstore.ConfigStore, raw string) *Config {
	t.Helper()
	var configData ConfigData
	require.NoError(t, json.Unmarshal([]byte(raw), &configData))
	config := &Config{ConfigStore: store}
	loadGovernanceConfig(context.Background(), config, &configData)
	syncModelRoutersFromFile(context.Background(), config, &configData)
	return config
}

// routerJSON renders one model_routers entry with a phrase per tier.
func routerJSON(id, name, extra string, simple, medium, complex string) string {
	entry := fmt.Sprintf(`{"id":%q,"name":%q,"keywords":{"simple_keywords":[%q],"medium_keywords":[%q],"complex_keywords":[%q]}`, id, name, simple, medium, complex)
	if extra != "" {
		entry += "," + extra
	}
	return entry + "}"
}

// storedModelRouterRow reads a router's raw row, for timestamps the store type does not expose.
func storedModelRouterRow(t *testing.T, store configstore.ConfigStore, id string) tables.TableModelRouter {
	t.Helper()
	var row tables.TableModelRouter
	require.NoError(t, store.ExecuteTransaction(context.Background(), func(tx *gorm.DB) error {
		return tx.First(&row, "id = ?", id).Error
	}))
	return row
}

// requireModelRouterAbsent asserts no router is stored under id.
func requireModelRouterAbsent(t *testing.T, store configstore.ConfigStore, id string) {
	t.Helper()
	_, err := store.GetModelRouter(context.Background(), id)
	require.ErrorIs(t, err, configstore.ErrNotFound)
}

// TestModelRouterConfigLeavesComplexityConfigAlone pins that model routers and
// governance.complexity_analyzer_config are separate: the governance key keeps
// configuring the complexity config routing rules read complexity_tier from,
// creates no router, and is applied the same whether model_routers is present
// or not.
func TestModelRouterConfigLeavesComplexityConfigAlone(t *testing.T) {
	initTestLogger()
	ctx := context.Background()
	governance := `"governance":{"complexity_analyzer_config":{"keywords":{
		"simple_keywords":["governance-simple"],"medium_keywords":["governance-medium"],"complex_keywords":["governance-complex"]}}}`

	t.Run("governance key alone", func(t *testing.T) {
		store := createTestSQLiteConfigStore(t, t.TempDir())
		bootFromJSON(t, store, `{`+governance+`}`)

		cfg, err := store.GetComplexityAnalyzerConfig(ctx)
		require.NoError(t, err)
		require.NotNil(t, cfg)
		assert.Contains(t, cfg.Keywords.SimpleKeywords, "governance-simple")
		routers, err := store.GetModelRouters(ctx)
		require.NoError(t, err)
		assert.Empty(t, routers, "the complexity config creates no router")
	})

	t.Run("both keys", func(t *testing.T) {
		store := createTestSQLiteConfigStore(t, t.TempDir())
		bootFromJSON(t, store, `{`+governance+`,"model_routers":[`+
			routerJSON("support", "Support", "", "support-simple", "support-medium", "support-complex")+`]}`)

		cfg, err := store.GetComplexityAnalyzerConfig(ctx)
		require.NoError(t, err)
		require.NotNil(t, cfg)
		assert.Contains(t, cfg.Keywords.SimpleKeywords, "governance-simple")
		assert.NotContains(t, cfg.Keywords.SimpleKeywords, "support-simple")
		support, err := store.GetModelRouter(ctx, "support")
		require.NoError(t, err)
		assert.Equal(t, []string{"support-simple"}, support.Keywords.SimpleKeywords)
	})
}

// TestModelRouterConfigDeclaresRouters covers the shape: every entry becomes
// its own router holding exactly the phrases the file gives it.
func TestModelRouterConfigDeclaresRouters(t *testing.T) {
	initTestLogger()
	store := createTestSQLiteConfigStore(t, t.TempDir())
	ctx := context.Background()

	config := bootFromJSON(t, store, `{"model_routers":[`+
		routerJSON("general", "General", "", "general-simple", "general-medium", "general-complex")+","+
		routerJSON("support", "Support", `"description":"Support desk traffic","semantic":{"provider":"openai","embedding_model":"text-embedding-3-small"}`,
			"reset my password", "summarize this ticket thread", "draft an escalation plan")+
		`]}`)

	general, err := store.GetModelRouter(ctx, "general")
	require.NoError(t, err)
	assert.Equal(t, "General", general.Name)
	assert.Equal(t, []string{"general-simple"}, general.Keywords.SimpleKeywords, "a new router is not seeded with the built-in phrases")

	support, err := store.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	assert.Equal(t, "Support", support.Name)
	assert.Equal(t, "Support desk traffic", support.Description)
	assert.Equal(t, []string{"reset my password"}, support.Keywords.SimpleKeywords)
	require.NotNil(t, support.Semantic)
	assert.Equal(t, "text-embedding-3-small", support.Semantic.EmbeddingModel)
	assert.False(t, support.ConfigHashes.Empty())

	assert.Empty(t, config.ModelRouters, "with a config store, the store is the source of truth")
	cfg, err := store.GetComplexityAnalyzerConfig(ctx)
	require.NoError(t, err)
	assert.Nil(t, cfg, "declaring routers configures no complexity config")
}

// TestModelRouterConfigSplitModeKeepsDashboardEdits walks one router through
// reboots: an unchanged file writes nothing, a changed section is merged in
// without undoing dashboard edits to other sections or to the name, and a
// changed name is applied.
func TestModelRouterConfigSplitModeKeepsDashboardEdits(t *testing.T) {
	initTestLogger()
	store := createTestSQLiteConfigStore(t, t.TempDir())
	ctx := context.Background()
	semantic := `"semantic":{"provider":"openai","embedding_model":"text-embedding-3-small"}`
	boot := func(name, medium string) {
		bootFromJSON(t, store, `{"model_routers":[`+
			routerJSON("support", name, semantic, "support-simple", medium, "support-complex")+`]}`)
	}

	boot("Support", "support-medium")
	router, err := store.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	router.Name = "Support desk"
	router.Semantic.EmbeddingModel = "text-embedding-3-large"
	router.ConfigHashes = configstore.ComplexityAnalyzerConfigHashes{}
	router.ConfigHash = ""
	require.NoError(t, store.UpdateModelRouter(ctx, router))
	edited := storedModelRouterRow(t, store, "support")

	boot("Support", "support-medium")
	assert.Equal(t, edited.UpdatedAt, storedModelRouterRow(t, store, "support").UpdatedAt, "an unchanged file writes nothing")

	boot("Support", "support-medium-2")
	router, err = store.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"support-medium", "support-medium-2"}, router.Keywords.MediumKeywords, "a changed phrase list merges in")
	assert.Equal(t, "text-embedding-3-large", router.Semantic.EmbeddingModel, "an unchanged section keeps its dashboard edit")
	assert.Equal(t, "Support desk", router.Name, "an unchanged name keeps its dashboard edit")

	boot("Customer support", "support-medium-2")
	router, err = store.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	assert.Equal(t, "Customer support", router.Name)
	assert.Equal(t, "text-embedding-3-large", router.Semantic.EmbeddingModel)
}

// TestModelRouterConfigSourceOfTruth pins config.json as the source of truth:
// dashboard drift is reverted, undeclared routers are deleted while the
// complexity config stays, and an invalid entry does not delete the router it
// was meant to configure.
func TestModelRouterConfigSourceOfTruth(t *testing.T) {
	initTestLogger()
	ctx := context.Background()
	keywords := func(prefix string) *configstore.ModelRouter {
		return &configstore.ModelRouter{ComplexityAnalyzerConfig: configstore.ComplexityAnalyzerConfig{
			Keywords: configstore.ComplexityEditableKeywordConfig{
				SimpleKeywords:  []string{prefix + "-simple"},
				MediumKeywords:  []string{prefix + "-medium"},
				ComplexKeywords: []string{prefix + "-complex"},
			},
		}}
	}
	seed := func(t *testing.T, store configstore.ConfigStore, id, name string) {
		router := keywords(id)
		router.ID, router.Name = id, name
		require.NoError(t, store.CreateModelRouter(ctx, router))
	}

	t.Run("reverts drift and prunes", func(t *testing.T) {
		store := createTestSQLiteConfigStore(t, t.TempDir())
		require.NoError(t, store.UpdateComplexityAnalyzerConfig(ctx, &keywords("default").ComplexityAnalyzerConfig))
		seed(t, store, "support", "Support desk")
		seed(t, store, "retired", "Retired")
		seed(t, store, "typo", "Typo")

		bootFromJSON(t, store, `{"source_of_truth":"config.json","model_routers":[`+
			routerJSON("support", "Support", "", "file-simple", "file-medium", "file-complex")+","+
			`{"id":"typo","name":"Typo","keywords":{"simple_keywords":["only-one-tier"]}}`+
			`]}`)

		support, err := store.GetModelRouter(ctx, "support")
		require.NoError(t, err)
		assert.Equal(t, "Support", support.Name)
		assert.Equal(t, []string{"file-simple"}, support.Keywords.SimpleKeywords)
		requireModelRouterAbsent(t, store, "retired")
		_, err = store.GetModelRouter(ctx, "typo")
		require.NoError(t, err, "an invalid entry must not delete the router it names")
		cfg, err := store.GetComplexityAnalyzerConfig(ctx)
		require.NoError(t, err)
		require.NotNil(t, cfg, "pruning routers leaves the complexity config alone")
		assert.Equal(t, []string{"default-simple"}, cfg.Keywords.SimpleKeywords)
	})

	t.Run("a declared router can take the name of a pruned one", func(t *testing.T) {
		store := createTestSQLiteConfigStore(t, t.TempDir())
		seed(t, store, "retired", "Support")

		bootFromJSON(t, store, `{"source_of_truth":"config.json","model_routers":[`+
			routerJSON("support", "Support", "", "file-simple", "file-medium", "file-complex")+`]}`)

		requireModelRouterAbsent(t, store, "retired")
		support, err := store.GetModelRouter(ctx, "support")
		require.NoError(t, err)
		assert.Equal(t, "Support", support.Name)
	})

	t.Run("an empty list prunes every router", func(t *testing.T) {
		store := createTestSQLiteConfigStore(t, t.TempDir())
		require.NoError(t, store.UpdateComplexityAnalyzerConfig(ctx, &keywords("default").ComplexityAnalyzerConfig))
		seed(t, store, "support", "Support")

		bootFromJSON(t, store, `{"source_of_truth":"config.json","model_routers":[]}`)

		requireModelRouterAbsent(t, store, "support")
		cfg, err := store.GetComplexityAnalyzerConfig(ctx)
		require.NoError(t, err)
		assert.NotNil(t, cfg, "the complexity config is not a router and is not pruned")
	})

	t.Run("no model_routers section prunes nothing", func(t *testing.T) {
		store := createTestSQLiteConfigStore(t, t.TempDir())
		seed(t, store, "support", "Support")

		bootFromJSON(t, store, `{"source_of_truth":"config.json","governance":{"budgets":[]}}`)

		_, err := store.GetModelRouter(ctx, "support")
		require.NoError(t, err)
	})
}

// TestModelRouterConfigSkipsBadEntries pins that one bad entry is skipped
// without holding back the rest: a repeated id keeps its first declaration,
// and an entry that fails validation is dropped on its own.
func TestModelRouterConfigSkipsBadEntries(t *testing.T) {
	initTestLogger()
	store := createTestSQLiteConfigStore(t, t.TempDir())
	ctx := context.Background()

	bootFromJSON(t, store, `{"model_routers":[`+
		routerJSON("support", "Support", `"description":"first"`, "a-simple", "a-medium", "a-complex")+","+
		routerJSON("support", "Support again", `"description":"second"`, "b-simple", "b-medium", "b-complex")+","+
		routerJSON("bad id!", "Bad", "", "c-simple", "c-medium", "c-complex")+","+
		routerJSON("general", "General", "", "d-simple", "d-medium", "d-complex")+
		`]}`)

	support, err := store.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	assert.Equal(t, "first", support.Description)
	requireModelRouterAbsent(t, store, "bad id!")
	_, err = store.GetModelRouter(ctx, "general")
	require.NoError(t, err, "the entries after a bad one still apply")
}

// TestModelRouterConfigReplacesUnreadableRouter pins that a router whose stored
// row this version cannot read is replaced by its config.json declaration.
func TestModelRouterConfigReplacesUnreadableRouter(t *testing.T) {
	initTestLogger()
	store := createTestSQLiteConfigStore(t, t.TempDir())
	ctx := context.Background()
	declare := `{"model_routers":[` + routerJSON("support", "Support", "", "s", "m", "c") + `]}`

	bootFromJSON(t, store, declare)
	require.NoError(t, store.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		return tx.Model(&tables.TableModelRouter{}).Where("id = ?", "support").Update("simple_phrases_json", `["broken`).Error
	}))
	_, err := store.GetModelRouter(ctx, "support")
	require.ErrorIs(t, err, configstore.ErrConfigUnreadable)

	bootFromJSON(t, store, declare)
	router, err := store.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	assert.Equal(t, []string{"s"}, router.Keywords.SimpleKeywords)
}

// TestModelRouterConfigWithoutStore pins that a gateway with no config store
// still gets its declared routers, in memory.
func TestModelRouterConfigWithoutStore(t *testing.T) {
	initTestLogger()
	var configData ConfigData
	require.NoError(t, json.Unmarshal([]byte(`{"model_routers":[`+
		routerJSON("support", "Support", "", "Simple One", "medium-one", "complex-one")+`]}`), &configData))
	config := &Config{}

	syncModelRoutersFromFile(context.Background(), config, &configData)

	require.Len(t, config.ModelRouters, 1)
	router := config.ModelRouters[0]
	assert.Equal(t, "support", router.ID)
	assert.Equal(t, []string{"simple one"}, router.Keywords.SimpleKeywords, "in-memory routers are normalized like stored ones")
}

// TestModelRouterConfigSchema pins where model routers live in config.json: a
// top-level list, accepted beside the governance complexity config, whose ids
// are ones the store accepts. The schema rejects the list anywhere else, and a
// suggestions block on a router, since only the complexity config has one.
func TestModelRouterConfigSchema(t *testing.T) {
	schema := loadLocalSchema(t)
	semantic := `"semantic":{"provider":"openai","embedding_model":"text-embedding-3-small"}`

	valid := `{"governance":{"complexity_analyzer_config":{"keywords":{"simple_keywords":["a"],"medium_keywords":["b"],"complex_keywords":["c"]}}},` +
		`"model_routers":[` +
		routerJSON("default", "General", "", "s", "m", "c") + "," +
		routerJSON("support-desk_2", "Support", `"description":"Support desk traffic",`+semantic+`,"session":{"enabled":true},`+
			`"targets":[{"tier":"COMPLEX","provider":"anthropic","model":"claude-opus","weight":0.5,"fallbacks":["openai/gpt-4o"]},{"tier":"COMPLEX","provider":"anthropic","model":"claude-sonnet","weight":0.5}],`+
			`"routing":{"enabled":true,"scope":"team","scope_id":"team-1","cel_expression":"model == \"support-assistant\"","query":{"combinator":"and","rules":[]},"priority":2}`, "s", "m", "c") + "," +
		routerJSON("llm-router", "LLM", `"classifier":"llm","llm":{"provider":"openai","model":"gpt-4o-mini"},"session":{"enabled":true}`, "s", "m", "c") +
		`]}`
	require.NoError(t, ValidateConfigSchema([]byte(valid), schema))

	invalid := map[string]string{
		"list under governance":    `{"governance":{"model_routers":[` + routerJSON("support", "Support", "", "s", "m", "c") + `]}}`,
		"id with a space":          `{"model_routers":[` + routerJSON("support desk", "Support", "", "s", "m", "c") + `]}`,
		"missing name":             `{"model_routers":[{"id":"support","keywords":{"simple_keywords":["s"],"medium_keywords":["m"],"complex_keywords":["c"]}}]}`,
		"unknown field":            `{"model_routers":[` + routerJSON("support", "Support", `"phrases":["s"]`, "s", "m", "c") + `]}`,
		"session without semantic": `{"model_routers":[` + routerJSON("support", "Support", `"session":{"enabled":true}`, "s", "m", "c") + `]}`,
		"suggestions block":        `{"model_routers":[` + routerJSON("support", "Support", `"suggestions":{"enabled":false}`, "s", "m", "c") + `]}`,
		"llm without an llm block": `{"model_routers":[` + routerJSON("support", "Support", `"classifier":"llm"`, "s", "m", "c") + `]}`,
		"unknown target tier":      `{"model_routers":[` + routerJSON("support", "Support", `"targets":[{"tier":"HARD","model":"m","weight":1}]`, "s", "m", "c") + `]}`,
		"target key w/o provider":  `{"model_routers":[` + routerJSON("support", "Support", `"targets":[{"tier":"SIMPLE","model":"m","key_id":"k","weight":1}]`, "s", "m", "c") + `]}`,
		"target first-token limit": `{"model_routers":[` + routerJSON("support", "Support", `"targets":[{"tier":"SIMPLE","model":"m","weight":1,"ttft_timeout_ms":500}]`, "s", "m", "c") + `]}`,
		"routing user scope":       `{"model_routers":[` + routerJSON("support", "Support", `"routing":{"scope":"user","scope_id":"u"}`, "s", "m", "c") + `]}`,
		"routing unknown field":    `{"model_routers":[` + routerJSON("support", "Support", `"routing":{"scope":"global","chain_rule":true}`, "s", "m", "c") + `]}`,
		"routing team without id":  `{"model_routers":[` + routerJSON("support", "Support", `"routing":{"scope":"team"}`, "s", "m", "c") + `]}`,
	}
	for name, config := range invalid {
		t.Run(name, func(t *testing.T) {
			require.Error(t, ValidateConfigSchema([]byte(config), schema))
		})
	}
}

// TestModelRouterConfigTargets walks a router's targets through reboots: the
// file sets them, an entry without targets leaves dashboard targets alone, a
// dashboard edit survives an unchanged file, a changed entry re-applies them,
// source-of-truth mode reverts drift, and invalid targets skip the router.
func TestModelRouterConfigTargets(t *testing.T) {
	initTestLogger()
	store := createTestSQLiteConfigStore(t, t.TempDir())
	ctx := context.Background()
	mini := `"targets":[{"tier":"SIMPLE","provider":"openai","model":"gpt-4o-mini","weight":1}]`
	boot := func(sourceOfTruth bool, name, extra string) {
		prefix := `{`
		if sourceOfTruth {
			prefix = `{"source_of_truth":"config.json",`
		}
		bootFromJSON(t, store, prefix+`"model_routers":[`+routerJSON("support", name, extra, "s", "m", "c")+`]}`)
	}
	models := func() []string {
		router, err := store.GetModelRouter(ctx, "support")
		require.NoError(t, err)
		out := make([]string, 0, len(router.Targets))
		for _, target := range router.Targets {
			out = append(out, *target.Model)
		}
		return out
	}
	dashboardSets := func(model string) {
		require.NoError(t, store.SetModelRouterTargets(ctx, "support", []tables.TableModelRouterTarget{
			{Tier: "SIMPLE", Model: &model, Weight: 1},
		}))
	}

	boot(false, "Support", mini)
	assert.Equal(t, []string{"gpt-4o-mini"}, models())

	dashboardSets("claude-haiku")
	boot(false, "Support", mini)
	assert.Equal(t, []string{"claude-haiku"}, models(), "a dashboard edit survives an unchanged file")

	boot(false, "Support desk", mini)
	assert.Equal(t, []string{"gpt-4o-mini"}, models(), "a changed entry re-applies its targets")

	dashboardSets("claude-haiku")
	boot(true, "Support desk", mini)
	assert.Equal(t, []string{"gpt-4o-mini"}, models(), "source of truth reverts drift")

	dashboardSets("claude-haiku")
	boot(true, "Support desk", "")
	assert.Equal(t, []string{"claude-haiku"}, models(), "an entry without targets leaves them alone")

	boot(false, "Support desk", `"targets":[{"tier":"SIMPLE","model":"m","weight":0.5}]`)
	assert.Equal(t, []string{"claude-haiku"}, models(), "invalid targets are not applied")
}

// TestModelRouterConfigRouting walks a router's routing through reboots on
// the same terms as its targets, and pins what keeps routing from applying: a
// condition that uses complexity_tier skips the whole entry, and a scope
// whose entity does not exist, or a user scope, leaves the stored routing.
func TestModelRouterConfigRouting(t *testing.T) {
	initTestLogger()
	store := createTestSQLiteConfigStore(t, t.TempDir())
	ctx := context.Background()
	on := `"routing":{"enabled":true,"scope":"global","cel_expression":"model == \"support-assistant\"","priority":3}`
	boot := func(sourceOfTruth bool, name, extra string) {
		prefix := `{`
		if sourceOfTruth {
			prefix = `{"source_of_truth":"config.json",`
		}
		bootFromJSON(t, store, prefix+`"model_routers":[`+routerJSON("support", name, extra, "s", "m", "c")+`]}`)
	}
	routing := func() configstore.ModelRouterRouting {
		router, err := store.GetModelRouter(ctx, "support")
		require.NoError(t, err)
		require.NotNil(t, router.Routing)
		return *router.Routing
	}
	dashboardTurnsOff := func() {
		current := routing()
		current.Enabled = false
		require.NoError(t, store.SetModelRouterRouting(ctx, "support", current))
	}

	boot(false, "Support", on)
	assert.Equal(t, configstore.ModelRouterRouting{Enabled: true, Scope: "global", CelExpression: `model == "support-assistant"`, Priority: 3}, routing())

	dashboardTurnsOff()
	boot(false, "Support", on)
	assert.False(t, routing().Enabled, "a dashboard edit survives an unchanged file")

	boot(false, "Support desk", on)
	assert.True(t, routing().Enabled, "a changed entry re-applies its routing")

	dashboardTurnsOff()
	boot(true, "Support desk", on)
	assert.True(t, routing().Enabled, "source of truth reverts drift")

	dashboardTurnsOff()
	boot(true, "Support desk", "")
	assert.False(t, routing().Enabled, "an entry without routing leaves it alone")

	boot(true, "Support desk", `"routing":{"enabled":true,"scope":"team","scope_id":"no-such-team","priority":3}`)
	assert.Equal(t, "global", routing().Scope, "a scope whose entity does not exist is not applied")

	boot(true, "Customer support", `"routing":{"enabled":true,"scope":"global","cel_expression":"complexity_tier == \"SIMPLE\""}`)
	router, err := store.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	assert.Equal(t, "Support desk", router.Name, "a condition that uses complexity_tier skips the entry")
}

// TestModelRouterConfigLoadConfigEndToEnd boots a real config.json through
// LoadConfig, schema validation included, and checks that the routers it
// declares reach the store and configure no complexity config.
func TestModelRouterConfigLoadConfigEndToEnd(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)
	keywords := func(prefix string) configstore.ComplexityAnalyzerConfig {
		return configstore.ComplexityAnalyzerConfig{
			TierBoundaries: configstore.DefaultComplexityTierBoundaries(),
			Keywords: configstore.ComplexityEditableKeywordConfig{
				SimpleKeywords:  []string{prefix + "-simple"},
				MediumKeywords:  []string{prefix + "-medium"},
				ComplexKeywords: []string{prefix + "-complex"},
			},
		}
	}
	configData := makeConfigDataWithProvidersAndDir(map[string]configstore.ProviderConfig{
		"openai": makeProviderConfigWithNetwork("openai-key-1", "sk-test-123", "https://api.openai.com"),
	}, tempDir)
	configData.ModelRouters = []configstore.ModelRouter{
		{ID: "general", Name: "General", ComplexityAnalyzerConfig: keywords("general")},
		{ID: "support", Name: "Support", Description: "Support desk traffic", ComplexityAnalyzerConfig: keywords("support")},
	}
	createConfigFile(t, tempDir, configData)

	ctx := context.Background()
	config, err := LoadConfig(ctx, tempDir)
	require.NoError(t, err)
	defer config.Close(ctx)

	general, err := config.ConfigStore.GetModelRouter(ctx, "general")
	require.NoError(t, err)
	assert.Equal(t, "General", general.Name)
	complexityConfig, err := config.ConfigStore.GetComplexityAnalyzerConfig(ctx)
	require.NoError(t, err)
	assert.Nil(t, complexityConfig)
	support, err := config.ConfigStore.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	assert.Equal(t, "Support desk traffic", support.Description)
	assert.Equal(t, []string{"support-simple"}, support.Keywords.SimpleKeywords)
}
