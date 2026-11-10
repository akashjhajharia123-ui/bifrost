package configstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// testModelRouter returns a valid router with a semantic block.
func testModelRouter(id, name string) *ModelRouter {
	return &ModelRouter{
		ID:                       id,
		Name:                     name,
		Description:              "Requests from the " + name + " team",
		ComplexityAnalyzerConfig: *testSemanticAnalyzerConfig(),
	}
}

// fillEveryField sets every field reachable from v to a distinct non-zero
// value: pointers are allocated, slices and maps get one filled element. It
// fails on a field it cannot set, so an unexported field cannot slip past the
// round-trip test unchecked.
func fillEveryField(t *testing.T, v reflect.Value, path string, seq *int) {
	t.Helper()
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		fillEveryField(t, v.Elem(), path, seq)
	case reflect.Struct:
		if v.Type() == reflect.TypeFor[time.Time]() {
			*seq++
			v.Set(reflect.ValueOf(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(*seq) * time.Hour)))
			return
		}
		for i := 0; i < v.NumField(); i++ {
			field := v.Field(i)
			name := path + "." + v.Type().Field(i).Name
			if !field.CanSet() {
				t.Fatalf("%s is unexported; the round-trip test cannot prove it survives a save", name)
			}
			fillEveryField(t, field, name, seq)
		}
	case reflect.String:
		*seq++
		v.SetString(fmt.Sprintf("value-%d", *seq))
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int64:
		*seq++
		v.SetInt(int64(*seq))
	case reflect.Float64:
		*seq++
		v.SetFloat(float64(*seq) / 1000)
	case reflect.Slice:
		slice := reflect.MakeSlice(v.Type(), 1, 1)
		fillEveryField(t, slice.Index(0), path+"[0]", seq)
		v.Set(slice)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		key := reflect.New(v.Type().Key()).Elem()
		fillEveryField(t, key, path+"{key}", seq)
		value := reflect.New(v.Type().Elem()).Elem()
		fillEveryField(t, value, path+"{value}", seq)
		m.SetMapIndex(key, value)
		v.Set(m)
	default:
		t.Fatalf("%s has kind %s, which fillEveryField does not know how to fill", path, v.Kind())
	}
}

// TestModelRouterTableRoundTripCoversEveryField pins that every field of a
// router, its config included, has a column. A field added to the config
// without one fails here instead of being silently dropped on save. Targets
// are the exception: they have a table of their own, which
// TestModelRouterTargetsStore covers.
func TestModelRouterTableRoundTripCoversEveryField(t *testing.T) {
	var router ModelRouter
	seq := 0
	v := reflect.ValueOf(&router).Elem()
	for i := 0; i < v.NumField(); i++ {
		if name := v.Type().Field(i).Name; name != "Targets" {
			fillEveryField(t, v.Field(i), "ModelRouter."+name, &seq)
		}
	}

	row, err := modelRouterToTable(&router)
	require.NoError(t, err)
	back, err := modelRouterFromTable(row)
	require.NoError(t, err)
	assert.Equal(t, router, *back)
}

// TestModelRouterTableKeepsAbsentAndZeroBlocksApart pins the NULL-anchor rule:
// an absent block (semantic off) and a present block holding only zero values
// must not read back as each other.
func TestModelRouterTableKeepsAbsentAndZeroBlocksApart(t *testing.T) {
	t.Run("absent blocks stay absent", func(t *testing.T) {
		router := ModelRouter{ID: "absent", Name: "Absent", ComplexityAnalyzerConfig: ComplexityAnalyzerConfig{Classifier: ComplexityClassifierSemantic}}
		row, err := modelRouterToTable(&router)
		require.NoError(t, err)
		assert.Nil(t, row.SemanticProvider)
		assert.Nil(t, row.DecisionProvider)
		assert.Nil(t, row.DecisionCriteriaJSON)
		assert.Nil(t, row.LLMProvider)
		assert.Nil(t, row.SessionEnabled)
		assert.Nil(t, row.SuggestionsEnabled)
		assert.Nil(t, row.SuggestionsScheduleEvery)
		assert.Nil(t, row.SuggestionsTierClassifier)

		back, err := modelRouterFromTable(row)
		require.NoError(t, err)
		assert.Equal(t, router, *back)
	})

	t.Run("zero-valued blocks stay present", func(t *testing.T) {
		router := ModelRouter{ID: "zero", Name: "Zero", ComplexityAnalyzerConfig: ComplexityAnalyzerConfig{
			Classifier:  ComplexityClassifierSemantic,
			Semantic:    &ComplexitySemanticConfig{},
			Decision:    &ComplexityDecisionConfig{PreviousMessageCount: bifrost.Ptr(0)},
			LLM:         &ComplexityLLMConfig{},
			Session:     &ComplexitySessionConfig{},
			Suggestions: &ComplexitySuggestionsConfig{Schedule: &ComplexitySuggestionsSchedule{}},
		}}
		row, err := modelRouterToTable(&router)
		require.NoError(t, err)
		back, err := modelRouterFromTable(row)
		require.NoError(t, err)
		assert.Equal(t, router, *back)
		require.NotNil(t, back.Decision.PreviousMessageCount, "an explicit 0 history must not read back as unset")
		assert.Equal(t, 0, *back.Decision.PreviousMessageCount)
	})

	t.Run("unset decision history stays unset", func(t *testing.T) {
		router := ModelRouter{ID: "unset", Name: "Unset", ComplexityAnalyzerConfig: ComplexityAnalyzerConfig{
			Classifier: ComplexityClassifierDecision,
			Decision:   &ComplexityDecisionConfig{Provider: "openai", Model: "gpt-4o-mini"},
		}}
		row, err := modelRouterToTable(&router)
		require.NoError(t, err)
		assert.Nil(t, row.DecisionPreviousMessageCount)
		back, err := modelRouterFromTable(row)
		require.NoError(t, err)
		assert.Nil(t, back.Decision.PreviousMessageCount)
	})
}

// TestModelRouterStoreKeepsZeroSettings guards the gorm default pitfall through
// a real save: a declared column default would replace these zeros on insert.
func TestModelRouterStoreKeepsZeroSettings(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	router := testModelRouter("support", "Support")
	router.Semantic.MinSimilarity = 0
	router.Semantic.CountTowardBudgets = false
	router.Decision = &ComplexityDecisionConfig{PreviousMessageCount: bifrost.Ptr(0), MinConfidence: 0}
	router.Session = &ComplexitySessionConfig{Enabled: false}
	require.NoError(t, store.CreateModelRouter(ctx, router))

	var row tables.TableModelRouter
	require.NoError(t, store.DB().First(&row, "id = ?", "support").Error)
	require.NotNil(t, row.SessionEnabled)
	assert.False(t, *row.SessionEnabled)
	require.NotNil(t, row.DecisionPreviousMessageCount)
	assert.Equal(t, 0, *row.DecisionPreviousMessageCount)
	require.NotNil(t, row.SemanticCountTowardBudgets)
	assert.False(t, *row.SemanticCountTowardBudgets)

	got, err := store.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	require.NotNil(t, got.Session)
	assert.False(t, got.Session.Enabled)
	require.NotNil(t, got.Decision.PreviousMessageCount)
	assert.Equal(t, 0, *got.Decision.PreviousMessageCount)
	assert.Zero(t, got.Semantic.MinSimilarity)
}

// TestModelRouterStoreCRUD covers creating, listing, renaming and deleting
// routers, and the identity rules every save enforces.
func TestModelRouterStoreCRUD(t *testing.T) {
	ctx := context.Background()

	t.Run("create, get and list", func(t *testing.T) {
		store := setupRDBTestStore(t)
		// Created in id order, so a created_at tie (falling back to id) cannot reorder them.
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("billing", "Billing")))
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", " Support ")))
		require.NoError(t, store.UpdateComplexityAnalyzerConfig(ctx, testSemanticAnalyzerConfig()))

		got, err := store.GetModelRouter(ctx, "support")
		require.NoError(t, err)
		assert.Equal(t, "Support", got.Name, "identity is trimmed on save")
		assert.Equal(t, testSemanticAnalyzerConfig().Normalized(), got.ComplexityAnalyzerConfig)

		routers, err := store.GetModelRouters(ctx)
		require.NoError(t, err)
		ids := make([]string, 0, len(routers))
		for _, r := range routers {
			ids = append(ids, r.ID)
		}
		assert.Equal(t, []string{"billing", "support", LegacyComplexityRouterID}, ids, "oldest first, the router a legacy save created included")
	})

	t.Run("taken id or name", func(t *testing.T) {
		store := setupRDBTestStore(t)
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", "Support")))
		require.ErrorIs(t, store.CreateModelRouter(ctx, testModelRouter("support", "Other")), ErrAlreadyExists)
		require.ErrorIs(t, store.CreateModelRouter(ctx, testModelRouter("other", "Support")), ErrAlreadyExists)

		require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("billing", "Billing")))
		rename := testModelRouter("billing", "Support")
		require.ErrorIs(t, store.UpdateModelRouter(ctx, rename), ErrAlreadyExists)
	})

	t.Run("identity validation", func(t *testing.T) {
		store := setupRDBTestStore(t)
		cases := map[string]*ModelRouter{
			"empty id":        testModelRouter("  ", "Support"),
			"id with a space": testModelRouter("support team", "Support"),
			"id with a slash": testModelRouter("support/team", "Support"),
			"id too long":     testModelRouter(fmt.Sprintf("%065d", 0), "Support"),
			"empty name":      testModelRouter("support", " "),
		}
		for name, router := range cases {
			t.Run(name, func(t *testing.T) {
				require.Error(t, store.CreateModelRouter(ctx, router))
			})
		}
		assert.NoError(t, store.CreateModelRouter(ctx, testModelRouter("b2c1a7e0-4c1f-4f7e-9a51-3f0f1d6f8b10", "Support")), "a UUID is a valid id")
		assert.NoError(t, store.CreateModelRouter(ctx, testModelRouter("default", "Default")), "no id or name is reserved")
	})

	t.Run("invalid config is rejected", func(t *testing.T) {
		store := setupRDBTestStore(t)
		router := testModelRouter("support", "Support")
		router.Keywords.SimpleKeywords = nil
		require.ErrorContains(t, store.CreateModelRouter(ctx, router), "keyword lists must be non-empty")
	})

	t.Run("update and delete need an existing router", func(t *testing.T) {
		store := setupRDBTestStore(t)
		require.ErrorIs(t, store.UpdateModelRouter(ctx, testModelRouter("missing", "Missing")), ErrNotFound)
		require.ErrorIs(t, store.DeleteModelRouter(ctx, "missing"), ErrNotFound)
		_, err := store.ResetModelRouter(ctx, "missing", testComplexityAnalyzerConfig())
		require.ErrorIs(t, err, ErrNotFound)

		require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", "Support")))
		require.ErrorIs(t, store.UpdateModelRouter(ctx, testModelRouter("missing", "Support")), ErrNotFound,
			"a missing router is not found even when the name it asks for is taken")
		require.NoError(t, store.DeleteModelRouter(ctx, "support"))
		_, err = store.GetModelRouter(ctx, "support")
		require.ErrorIs(t, err, ErrNotFound)
	})
}

// TestModelRouterComplexityTier pins the router routing rules read
// complexity_tier from: at most one router answers it, a save that sets the
// flag takes it over, the legacy complexity config methods read and write
// that router, and with none complexity_tier runs on the built-in defaults.
func TestModelRouterComplexityTier(t *testing.T) {
	ctx := context.Background()
	tierRouter := func(t *testing.T, store *RDBConfigStore) string {
		t.Helper()
		routers, err := store.GetModelRouters(ctx)
		require.NoError(t, err)
		var flagged []string
		for _, r := range routers {
			if r.AnswersComplexityTier {
				flagged = append(flagged, r.ID)
			}
		}
		require.LessOrEqual(t, len(flagged), 1, "at most one router answers complexity_tier")
		if len(flagged) == 0 {
			return ""
		}
		return flagged[0]
	}

	t.Run("a flagged save takes complexity_tier over", func(t *testing.T) {
		store := setupRDBTestStore(t)
		cfg, err := store.GetComplexityAnalyzerConfig(ctx)
		require.NoError(t, err)
		assert.Nil(t, cfg, "with no router answering, callers use the built-in defaults")

		support := testModelRouter("support", "Support")
		support.AnswersComplexityTier = true
		require.NoError(t, store.CreateModelRouter(ctx, support))
		assert.Equal(t, "support", tierRouter(t, store))
		cfg, err = store.GetComplexityAnalyzerConfig(ctx)
		require.NoError(t, err)
		require.NotNil(t, cfg)
		assert.Equal(t, support.Normalized().Keywords, cfg.Keywords)

		billing := testModelRouter("billing", "Billing")
		billing.Keywords.SimpleKeywords = []string{"invoice please"}
		require.NoError(t, store.CreateModelRouter(ctx, billing))
		assert.Equal(t, "support", tierRouter(t, store), "an unflagged router leaves the flag alone")
		billing.AnswersComplexityTier = true
		require.NoError(t, store.UpdateModelRouter(ctx, billing))
		assert.Equal(t, "billing", tierRouter(t, store))
		legacy, err := readLegacyComplexityAnalyzerConfig(ctx, store.DB())
		require.NoError(t, err)
		require.NotNil(t, legacy)
		assert.Equal(t, []string{"invoice please"}, legacy.Keywords.SimpleKeywords, "the new router is mirrored for a rolled-back Bifrost")

		billing.AnswersComplexityTier = false
		require.NoError(t, store.UpdateModelRouter(ctx, billing))
		assert.Empty(t, tierRouter(t, store))
		cfg, err = store.GetComplexityAnalyzerConfig(ctx)
		require.NoError(t, err)
		assert.Nil(t, cfg)
	})

	t.Run("legacy saves write the flagged router and keep its identity", func(t *testing.T) {
		store := setupRDBTestStore(t)
		support := testModelRouter("support", "Support")
		support.Description = "Customer support"
		support.AnswersComplexityTier = true
		require.NoError(t, store.CreateModelRouter(ctx, support))

		update := testSemanticAnalyzerConfig()
		update.Keywords.SimpleKeywords = []string{"hi there"}
		require.NoError(t, store.UpdateComplexityAnalyzerConfig(ctx, update))
		got, err := store.GetModelRouter(ctx, "support")
		require.NoError(t, err)
		assert.Equal(t, "Support", got.Name)
		assert.Equal(t, "Customer support", got.Description)
		assert.True(t, got.AnswersComplexityTier)
		assert.Equal(t, []string{"hi there"}, got.Keywords.SimpleKeywords)

		defaults := testComplexityAnalyzerConfig()
		defaults.Keywords.SimpleKeywords = []string{"default simple"}
		restored, err := store.ResetComplexityAnalyzerConfig(ctx, defaults)
		require.NoError(t, err)
		assert.Equal(t, []string{"default simple"}, restored.Keywords.SimpleKeywords)
		got, err = store.GetModelRouter(ctx, "support")
		require.NoError(t, err)
		assert.True(t, got.AnswersComplexityTier, "a reset keeps the flag")
		_, err = store.GetModelRouter(ctx, LegacyComplexityRouterID)
		require.ErrorIs(t, err, ErrNotFound, "no second router is created")
	})

	t.Run("a legacy save with no router answering creates one", func(t *testing.T) {
		store := setupRDBTestStore(t)
		require.NoError(t, store.UpdateComplexityAnalyzerConfig(ctx, testSemanticAnalyzerConfig()))
		got, err := store.GetModelRouter(ctx, LegacyComplexityRouterID)
		require.NoError(t, err)
		assert.Equal(t, LegacyComplexityRouterName, got.Name)
		assert.True(t, got.AnswersComplexityTier)

		got.Name = "General"
		require.NoError(t, store.UpdateModelRouter(ctx, got))
		require.NoError(t, store.UpdateComplexityAnalyzerConfig(ctx, testSemanticAnalyzerConfig()))
		got, err = store.GetModelRouter(ctx, LegacyComplexityRouterID)
		require.NoError(t, err)
		assert.Equal(t, "General", got.Name, "a config-only save keeps the name")

		require.NoError(t, store.DeleteModelRouter(ctx, LegacyComplexityRouterID), "it is an ordinary router")
		cfg, err := store.GetComplexityAnalyzerConfig(ctx)
		require.NoError(t, err)
		assert.Nil(t, cfg, "deleting the router that answered leaves the built-in defaults")
	})

	t.Run("a legacy reset with no router answering creates one", func(t *testing.T) {
		store := setupRDBTestStore(t)
		restored, err := store.ResetComplexityAnalyzerConfig(ctx, testComplexityAnalyzerConfig())
		require.NoError(t, err)
		assert.Equal(t, testComplexityAnalyzerConfig().Normalized().Keywords, restored.Keywords)
		assert.Equal(t, LegacyComplexityRouterID, tierRouter(t, store))
	})

	t.Run("a legacy save takes over an unflagged router with the legacy id", func(t *testing.T) {
		store := setupRDBTestStore(t)
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouter(LegacyComplexityRouterID, "Mine")))
		require.NoError(t, store.UpdateComplexityAnalyzerConfig(ctx, testSemanticAnalyzerConfig()))
		got, err := store.GetModelRouter(ctx, LegacyComplexityRouterID)
		require.NoError(t, err)
		assert.Equal(t, "Mine", got.Name)
		assert.True(t, got.AnswersComplexityTier)
	})

	t.Run("a legacy save whose router name is taken fails", func(t *testing.T) {
		store := setupRDBTestStore(t)
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", LegacyComplexityRouterName)))
		require.ErrorIs(t, store.UpdateComplexityAnalyzerConfig(ctx, testSemanticAnalyzerConfig()), ErrAlreadyExists)
		assert.Empty(t, tierRouter(t, store))
	})
}

// TestModelRouterSaveCarriesOverBookkeeping pins that a save that omits the
// config.json hashes, the name hash or the embedding fingerprint (every UI
// save) keeps the stored ones, and that a reset clears only the fingerprint.
func TestModelRouterSaveCarriesOverBookkeeping(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	seed := testModelRouter("support", "Support")
	hashes, err := GenerateComplexityAnalyzerConfigHashes(&seed.ComplexityAnalyzerConfig)
	require.NoError(t, err)
	seed.ConfigHashes = hashes
	seed.EmbeddingFingerprint = "fingerprint-1"
	seed.ConfigHash = GenerateModelRouterHash(*seed)
	require.NoError(t, store.CreateModelRouter(ctx, seed))

	update := testModelRouter("support", "Support")
	update.Semantic.EmbeddingModel = "text-embedding-3-large"
	require.NoError(t, store.UpdateModelRouter(ctx, update))

	got, err := store.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	assert.Equal(t, "text-embedding-3-large", got.Semantic.EmbeddingModel)
	assert.Equal(t, hashes, got.ConfigHashes)
	assert.Equal(t, "fingerprint-1", got.EmbeddingFingerprint)
	assert.Equal(t, seed.ConfigHash, got.ConfigHash)

	defaults := testComplexityAnalyzerConfig()
	defaults.Keywords.SimpleKeywords = []string{"default simple"}
	reset, err := store.ResetModelRouter(ctx, "support", defaults)
	require.NoError(t, err)
	assert.Equal(t, []string{"default simple"}, reset.Keywords.SimpleKeywords)
	assert.Empty(t, reset.EmbeddingFingerprint, "the fingerprint named the phrases the reset replaced")
	assert.Equal(t, "text-embedding-3-large", reset.Semantic.EmbeddingModel, "reset keeps the classifier settings")
	assert.Equal(t, "Support", reset.Name)
	assert.Equal(t, hashes, reset.ConfigHashes)

	got, err = store.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	assert.Equal(t, *reset, *got)
}

// TestModelRouterUnreadableRows covers rows this version cannot run, such as
// after a rollback from a newer build: they read as ErrConfigUnreadable, drop
// out of the list without failing it, and a save or reset recovers them.
func TestModelRouterUnreadableRows(t *testing.T) {
	ctx := context.Background()

	t.Run("router with corrupt phrases", func(t *testing.T) {
		store := setupRDBTestStore(t)
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", "Support")))
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("billing", "Billing")))
		require.NoError(t, store.DB().Model(&tables.TableModelRouter{}).Where("id = ?", "support").
			Update("simple_phrases_json", `["unterminated`).Error)

		_, err := store.GetModelRouter(ctx, "support")
		require.ErrorIs(t, err, ErrConfigUnreadable)

		routers, err := store.GetModelRouters(ctx)
		require.NoError(t, err)
		require.Len(t, routers, 1)
		assert.Equal(t, "billing", routers[0].ID)

		require.NoError(t, store.UpdateModelRouter(ctx, testModelRouter("support", "Support")))
		_, err = store.GetModelRouter(ctx, "support")
		require.NoError(t, err)
	})

	t.Run("complexity_tier router whose phrases claim two tiers", func(t *testing.T) {
		store := setupRDBTestStore(t)
		cfg := testSemanticAnalyzerConfig()
		hashes, err := GenerateComplexityAnalyzerConfigHashes(cfg)
		require.NoError(t, err)
		cfg.ConfigHashes = hashes
		require.NoError(t, store.UpdateComplexityAnalyzerConfig(ctx, cfg))
		require.NoError(t, store.DB().Model(&tables.TableModelRouter{}).Where("id = ?", LegacyComplexityRouterID).
			Updates(map[string]any{"simple_phrases_json": `["shared phrase"]`, "medium_phrases_json": `["shared phrase"]`}).Error)

		_, err = store.GetComplexityAnalyzerConfig(ctx)
		require.ErrorIs(t, err, ErrConfigUnreadable)
		governance, err := store.GetGovernanceConfig(ctx)
		require.NoError(t, err)
		require.NotNil(t, governance)
		assert.Nil(t, governance.ComplexityAnalyzerConfig, "an unreadable complexity_tier router degrades to the built-in defaults")

		defaults := testComplexityAnalyzerConfig()
		restored, err := store.ResetComplexityAnalyzerConfig(ctx, defaults)
		require.NoError(t, err)
		assert.Equal(t, defaults.Normalized().Keywords, restored.Keywords)
		assert.Equal(t, hashes, restored.ConfigHashes, "a reset of an unreadable row keeps its config.json bookkeeping")
		_, err = store.GetComplexityAnalyzerConfig(ctx)
		require.NoError(t, err)
	})
}

// TestModelRouterComplexityTierSaveWritesLegacyRows is the rollback guard:
// saving the router that answers complexity_tier keeps the governance_config
// rows older binaries read in step with it, and saving any other router
// leaves them alone.
func TestModelRouterComplexityTierSaveWritesLegacyRows(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	cfg := testSemanticAnalyzerConfig()
	cfg.Decision = &ComplexityDecisionConfig{MinConfidence: 0.6}
	cfg.EmbeddingFingerprint = "fingerprint-1"
	hashes, err := GenerateComplexityAnalyzerConfigHashes(cfg)
	require.NoError(t, err)
	cfg.ConfigHashes = hashes
	require.NoError(t, store.UpdateComplexityAnalyzerConfig(ctx, cfg))

	stored, err := store.GetComplexityAnalyzerConfig(ctx)
	require.NoError(t, err)
	legacy, err := readLegacyComplexityAnalyzerConfig(ctx, store.DB())
	require.NoError(t, err)
	assert.Equal(t, stored, legacy, "a rolled-back Bifrost reads exactly what this one serves")

	var analyzerRow tables.TableGovernanceConfig
	require.NoError(t, store.DB().First(&analyzerRow, "key = ?", tables.ConfigComplexityAnalyzerConfigKey).Error)
	var old preSemanticAnalyzerRow
	require.NoError(t, json.Unmarshal([]byte(analyzerRow.Value), &old))
	require.NoError(t, old.validate(), "the analyzer row must stay valid for a pre-semantic Bifrost")

	before := readRawComplexitySemanticConfig(t, store.DB())
	require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", "Support")))
	require.NoError(t, store.UpdateModelRouter(ctx, testModelRouter("support", "Support")))
	assert.Equal(t, before, readRawComplexitySemanticConfig(t, store.DB()), "only the complexity_tier router is mirrored")
}

// TestModelRouterPlaceholderNeverCommits pins that the row a legacy save
// inserts to lock on, when no router answers complexity_tier, disappears with
// its transaction.
func TestModelRouterPlaceholderNeverCommits(t *testing.T) {
	store := setupRDBTestStore(t)
	pinSingleConnection(t, store.DB())
	ctx := context.Background()

	abort := errors.New("abort")
	err := store.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		require.NoError(t, store.UpdateComplexityAnalyzerConfig(ctx, testSemanticAnalyzerConfig(), tx))
		return abort
	})
	require.ErrorIs(t, err, abort)

	var count int64
	require.NoError(t, store.DB().Model(&tables.TableModelRouter{}).Count(&count).Error)
	assert.Zero(t, count)
	cfg, err := store.GetComplexityAnalyzerConfig(ctx)
	require.NoError(t, err)
	assert.Nil(t, cfg)
}

// TestGetGovernanceConfigServesComplexityTierRouter pins that the governance
// snapshot still carries the config complexity_tier runs on, as it did before
// routers had names, and that the other routers are not governance.
func TestGetGovernanceConfigServesComplexityTierRouter(t *testing.T) {
	ctx := context.Background()

	t.Run("complexity_tier router config", func(t *testing.T) {
		store := setupRDBTestStore(t)
		require.NoError(t, store.UpdateComplexityAnalyzerConfig(ctx, testSemanticAnalyzerConfig()))
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", "Support")))

		governance, err := store.GetGovernanceConfig(ctx)
		require.NoError(t, err)
		require.NotNil(t, governance.ComplexityAnalyzerConfig)
		assert.Equal(t, testSemanticAnalyzerConfig().Normalized().Keywords, governance.ComplexityAnalyzerConfig.Keywords)
	})

	t.Run("a complexity_tier router alone reports governance as configured", func(t *testing.T) {
		store := setupRDBTestStore(t)
		require.NoError(t, store.UpdateComplexityAnalyzerConfig(ctx, testSemanticAnalyzerConfig()))
		// Without the rollback rows, the router row is all there is.
		require.NoError(t, store.DB().Where("key IN ?", []string{
			tables.ConfigComplexityAnalyzerConfigKey, tables.ConfigComplexitySemanticConfigKey,
		}).Delete(&tables.TableGovernanceConfig{}).Error)

		governance, err := store.GetGovernanceConfig(ctx)
		require.NoError(t, err)
		require.NotNil(t, governance)
		require.NotNil(t, governance.ComplexityAnalyzerConfig)
	})

	t.Run("other routers are not governance", func(t *testing.T) {
		store := setupRDBTestStore(t)
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", "Support")))

		governance, err := store.GetGovernanceConfig(ctx)
		require.NoError(t, err)
		assert.Nil(t, governance)
	})
}

// setupModelRouterMigrationDB is the exemplar migration's test database pinned
// to one connection: :memory: SQLite gives every pooled connection its own
// database, and these tests open transactions after the migration.
func setupModelRouterMigrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupComplexityExemplarMigrationDB(t)
	pinSingleConnection(t, db)
	return db
}

// pinSingleConnection keeps a :memory: SQLite database on one connection.
func pinSingleConnection(t *testing.T, db *gorm.DB) {
	t.Helper()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
}

// migrationTestStore wraps a migration test database in a store for reads.
// Router reads carry the routers' targets, whose table a later migration
// creates, so it is created here.
func migrationTestStore(db *gorm.DB) *RDBConfigStore {
	if err := db.AutoMigrate(&tables.TableModelRouterTarget{}); err != nil {
		panic(err)
	}
	s := &RDBConfigStore{}
	s.db.Store(db)
	return s
}

// TestMigrationAddModelRoutersTable pins that the migration moves exactly
// what this version would have routed with, bookkeeping included, and nothing
// it would have ignored.
// TestMigrationAddModelRouterSuggestionTierColumns pins that a model_routers
// table created before the suggestions tier block gets its columns, that a
// router saved then reads back without a tier block, and that rerunning is a
// no-op.
func TestMigrationAddModelRouterSuggestionTierColumns(t *testing.T) {
	ctx := context.Background()
	db := setupModelRouterMigrationDB(t)
	require.NoError(t, db.AutoMigrate(&tables.TableModelRouter{}))
	for _, column := range modelRouterSuggestionTierColumns {
		require.NoError(t, db.Migrator().DropColumn(&tables.TableModelRouter{}, column))
	}

	require.NoError(t, migrationAddModelRouterSuggestionTierColumns(ctx, db, testMigrationLogger))
	for _, column := range modelRouterSuggestionTierColumns {
		assert.True(t, db.Migrator().HasColumn(&tables.TableModelRouter{}, column), column)
	}
	require.NoError(t, migrationAddModelRouterSuggestionTierColumns(ctx, db, testMigrationLogger))
}

func TestMigrationAddModelRoutersTable(t *testing.T) {
	ctx := context.Background()

	t.Run("moves the stored config into a router that answers complexity_tier", func(t *testing.T) {
		db := setupModelRouterMigrationDB(t)
		cfg := testSessionAnalyzerConfig()
		cfg.TierBoundaries = ComplexityTierBoundaries{SimpleMedium: 0.15, MediumComplex: 0.35}
		cfg.Decision = &ComplexityDecisionConfig{MinConfidence: 0.6}
		cfg.Suggestions = &ComplexitySuggestionsConfig{Enabled: true, SampleRate: 0.25, LookbackDays: 14, Provider: "openai", Model: "gpt-5"}
		hashes, err := GenerateComplexityAnalyzerConfigHashes(cfg)
		require.NoError(t, err)
		cfg.ConfigHashes = hashes
		cfg.EmbeddingFingerprint = "fingerprint-1"
		normalized := cfg.Normalized()
		analyzerRaw, err := encodeComplexityAnalyzerConfig(normalized)
		require.NoError(t, err)
		semanticRaw, err := encodeComplexitySemanticConfigRow(normalized)
		require.NoError(t, err)
		seedComplexityAnalyzerConfig(t, db, string(analyzerRaw))
		seedComplexitySemanticConfig(t, db, string(semanticRaw))
		legacy, err := readLegacyComplexityAnalyzerConfig(ctx, db)
		require.NoError(t, err)

		require.NoError(t, migrationAddModelRoutersTable(ctx, db, testMigrationLogger))

		router, err := migrationTestStore(db).GetModelRouter(ctx, LegacyComplexityRouterID)
		require.NoError(t, err)
		assert.Equal(t, LegacyComplexityRouterName, router.Name)
		assert.True(t, router.AnswersComplexityTier, "routing rules keep reading complexity_tier from the moved config")
		assert.Equal(t, *legacy, router.ComplexityAnalyzerConfig)
		assert.Equal(t, hashes, router.ConfigHashes, "the first boot must not re-apply config.json over dashboard edits")
		assert.Equal(t, "fingerprint-1", router.EmbeddingFingerprint, "the first boot must not re-embed the phrases")
		assert.Equal(t, string(analyzerRaw), readRawComplexityAnalyzerConfig(t, db), "the rows stay for a rolled-back Bifrost")
		assert.Equal(t, string(semanticRaw), readRawComplexitySemanticConfig(t, db))
	})

	t.Run("reads the legacy four-list keyword shape", func(t *testing.T) {
		db := setupModelRouterMigrationDB(t)
		analyzerRaw, err := encodeComplexityAnalyzerConfig(testComplexityAnalyzerConfig().Normalized())
		require.NoError(t, err)
		seedComplexityAnalyzerConfig(t, db, string(analyzerRaw))
		seedComplexitySemanticConfig(t, db, `{"keywords":{"code_keywords":["function"],"technical_keywords":["latency"],"reasoning_keywords":["tradeoffs"],"simple_keywords":["hello"]}}`)

		require.NoError(t, migrationAddModelRoutersTable(ctx, db, testMigrationLogger))

		router, err := migrationTestStore(db).GetModelRouter(ctx, LegacyComplexityRouterID)
		require.NoError(t, err)
		assert.Equal(t, []string{"function", "latency"}, router.Keywords.MediumKeywords)
		assert.Equal(t, []string{"tradeoffs"}, router.Keywords.ComplexKeywords)
		assert.Equal(t, []string{"hello"}, router.Keywords.SimpleKeywords)
	})

	cases := map[string]func(t *testing.T, db *gorm.DB){
		"nothing stored": func(t *testing.T, db *gorm.DB) {},
		"analyzer row without exemplars": func(t *testing.T, db *gorm.DB) {
			raw, err := encodeComplexityAnalyzerConfig(testComplexityAnalyzerConfig().Normalized())
			require.NoError(t, err)
			seedComplexityAnalyzerConfig(t, db, string(raw))
		},
		"unreadable semantic row": func(t *testing.T, db *gorm.DB) {
			raw, err := encodeComplexityAnalyzerConfig(testComplexityAnalyzerConfig().Normalized())
			require.NoError(t, err)
			seedComplexityAnalyzerConfig(t, db, string(raw))
			seedComplexitySemanticConfig(t, db, `{"keywords":`)
		},
	}
	for name, seed := range cases {
		t.Run(name+" creates no router", func(t *testing.T) {
			db := setupModelRouterMigrationDB(t)
			seed(t, db)
			require.NoError(t, migrationAddModelRoutersTable(ctx, db, testMigrationLogger))
			require.True(t, db.Migrator().HasTable(&tables.TableModelRouter{}))
			var count int64
			require.NoError(t, db.Model(&tables.TableModelRouter{}).Count(&count).Error)
			assert.Zero(t, count)
		})
	}

	t.Run("runs once and never overwrites a router", func(t *testing.T) {
		db := setupModelRouterMigrationDB(t)
		raw, err := encodeComplexityAnalyzerConfig(testSemanticAnalyzerConfig().Normalized())
		require.NoError(t, err)
		semanticRaw, err := encodeComplexitySemanticConfigRow(testSemanticAnalyzerConfig().Normalized())
		require.NoError(t, err)
		seedComplexityAnalyzerConfig(t, db, string(raw))
		seedComplexitySemanticConfig(t, db, string(semanticRaw))
		require.NoError(t, migrationAddModelRoutersTable(ctx, db, testMigrationLogger))

		store := migrationTestStore(db)
		router, err := store.GetModelRouter(ctx, LegacyComplexityRouterID)
		require.NoError(t, err)
		router.Name = "General"
		require.NoError(t, store.UpdateModelRouter(ctx, router))

		require.NoError(t, migrationAddModelRoutersTable(ctx, db, testMigrationLogger))
		router, err = store.GetModelRouter(ctx, LegacyComplexityRouterID)
		require.NoError(t, err)
		assert.Equal(t, "General", router.Name)
	})
}
