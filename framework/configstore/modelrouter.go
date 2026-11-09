package configstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	// LegacyComplexityRouterID is the id of the router the complexity config
	// is moved into from before routers had names, and of the router a save
	// through the legacy complexity config API creates when no router answers
	// complexity_tier. Phrase suggestions recorded before routers had names
	// are keyed under it too. It is an ordinary router otherwise.
	LegacyComplexityRouterID = "default"
	// LegacyComplexityRouterName is that router's name when it is created.
	LegacyComplexityRouterName = "Complexity router"
	// MaxModelRouterIDLength matches the router_id columns of the phrase
	// suggestion tables, which key their rows by router.
	MaxModelRouterIDLength = 64
	// MaxModelRouterNameLength matches the name column.
	MaxModelRouterNameLength = 255
)

// ModelRouter is one named complexity classifier: its identity plus the same
// configuration the single complexity router has always had. The config is
// embedded so a router reads, in config.json and over the API, as that config
// with an id, a name and a description beside it.
type ModelRouter struct {
	ID                    string `json:"id"`
	Name                  string `json:"name"`
	Description           string `json:"description,omitempty"`
	AnswersComplexityTier bool   `json:"answers_complexity_tier,omitempty"` // routing rules read complexity_tier from it; at most one router
	ComplexityAnalyzerConfig
	// Targets are the model each tier gets, read with the router. Create and
	// update leave them alone; SetModelRouterTargets writes them. A config.json
	// entry that omits them leaves the stored targets as they are.
	Targets []tables.TableModelRouterTarget `json:"targets,omitempty"`
	// ConfigHash is the hash of the config.json entry's name, description and
	// targets last applied. The config sections carry their own hashes in
	// ConfigHashes, so UI edits to sections the file did not change survive a
	// reboot.
	ConfigHash string `json:"-"`
}

// normalizeModelRouterIdentity trims the identity fields in place.
func normalizeModelRouterIdentity(r *ModelRouter) {
	r.ID = strings.TrimSpace(r.ID)
	r.Name = strings.TrimSpace(r.Name)
	r.Description = strings.TrimSpace(r.Description)
}

// ValidateModelRouterIdentity trims a router's id, name and description in
// place and checks them, for callers that need the verdict before a save; the
// store's create and update run the same check.
func ValidateModelRouterIdentity(r *ModelRouter) error {
	normalizeModelRouterIdentity(r)
	return validateModelRouterIdentity(r)
}

// validateModelRouterIdentity checks a trimmed ID and name. IDs are limited to
// letters, digits, '-' and '_' because other stores key data by them (job
// partitions, suggestion rows), and a UUID fits.
func validateModelRouterIdentity(r *ModelRouter) error {
	if r.ID == "" {
		return fmt.Errorf("model router id is required")
	}
	if len(r.ID) > MaxModelRouterIDLength {
		return fmt.Errorf("model router id must be at most %d characters, got %d", MaxModelRouterIDLength, len(r.ID))
	}
	for _, ch := range r.ID {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
			return fmt.Errorf("model router id %q may contain only letters, digits, '-' and '_'", r.ID)
		}
	}
	if r.Name == "" {
		return fmt.Errorf("model router %q requires a name", r.ID)
	}
	if n := utf8.RuneCountInString(r.Name); n > MaxModelRouterNameLength {
		return fmt.Errorf("model router name must be at most %d characters, got %d", MaxModelRouterNameLength, n)
	}
	return nil
}

// GenerateModelRouterHash hashes the fields of a config.json router entry that
// the per-section config hashes do not cover: its name, its description,
// whether it answers complexity_tier and, when the entry declares them, its
// targets.
func GenerateModelRouterHash(r ModelRouter) string {
	hash := sha256.New()
	writeHashField(hash, "name", strings.TrimSpace(r.Name))
	writeHashField(hash, "description", strings.TrimSpace(r.Description))
	if r.AnswersComplexityTier {
		writeHashField(hash, "answers_complexity_tier", "true")
	}
	if r.Targets != nil {
		writeModelRouterTargetsHash(hash, r.Targets)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// valueOrZero dereferences a nullable column, reading NULL as the zero value.
func valueOrZero[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

// marshalModelRouterPhrases encodes one tier's phrase list for its column.
func marshalModelRouterPhrases(phrases []string) (string, error) {
	if len(phrases) == 0 {
		return "", nil
	}
	data, err := json.Marshal(phrases)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// unmarshalModelRouterPhrases decodes one tier's phrase column; empty is no phrases.
func unmarshalModelRouterPhrases(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var phrases []string
	if err := json.Unmarshal([]byte(raw), &phrases); err != nil {
		return nil, err
	}
	return phrases, nil
}

// modelRouterToTable lays a router out as its row, one column per setting. It
// does not normalize: what it is given is what the row holds.
func modelRouterToTable(r *ModelRouter) (*tables.TableModelRouter, error) {
	cfg := &r.ComplexityAnalyzerConfig
	row := &tables.TableModelRouter{
		ID:                    r.ID,
		Name:                  r.Name,
		Description:           r.Description,
		AnswersComplexityTier: r.AnswersComplexityTier,
		Classifier:            cfg.Classifier,
		SimpleMediumBoundary:  cfg.TierBoundaries.SimpleMedium,
		MediumComplexBoundary: cfg.TierBoundaries.MediumComplex,
		ConfigHash:            r.ConfigHash,
		EmbeddingFingerprint:  cfg.EmbeddingFingerprint,
	}
	var err error
	if row.SimplePhrasesJSON, err = marshalModelRouterPhrases(cfg.Keywords.SimpleKeywords); err != nil {
		return nil, fmt.Errorf("encode simple phrases: %w", err)
	}
	if row.MediumPhrasesJSON, err = marshalModelRouterPhrases(cfg.Keywords.MediumKeywords); err != nil {
		return nil, fmt.Errorf("encode medium phrases: %w", err)
	}
	if row.ComplexPhrasesJSON, err = marshalModelRouterPhrases(cfg.Keywords.ComplexKeywords); err != nil {
		return nil, fmt.Errorf("encode complex phrases: %w", err)
	}
	if !cfg.ConfigHashes.Empty() {
		hashes, err := json.Marshal(cfg.ConfigHashes)
		if err != nil {
			return nil, fmt.Errorf("encode config hashes: %w", err)
		}
		row.ConfigHashesJSON = string(hashes)
	}
	if s := cfg.Semantic; s != nil {
		row.SemanticProvider = bifrost.Ptr(string(s.Provider))
		row.SemanticEmbeddingModel = bifrost.Ptr(s.EmbeddingModel)
		row.SemanticTimeoutNS = bifrost.Ptr(int64(s.Timeout))
		row.SemanticMinSimilarity = bifrost.Ptr(s.MinSimilarity)
		row.SemanticMessageHistoryCount = bifrost.Ptr(s.MessageHistoryCount)
		row.SemanticCountTowardBudgets = bifrost.Ptr(s.CountTowardBudgets)
		row.SemanticVectorStore = bifrost.Ptr(s.VectorStore)
		row.SemanticFallback = bifrost.Ptr(s.Fallback)
	}
	if d := cfg.Decision; d != nil {
		row.DecisionProvider = bifrost.Ptr(string(d.Provider))
		row.DecisionModel = bifrost.Ptr(d.Model)
		if d.PreviousMessageCount != nil {
			row.DecisionPreviousMessageCount = bifrost.Ptr(*d.PreviousMessageCount)
		}
		row.DecisionTimeoutNS = bifrost.Ptr(int64(d.Timeout))
		row.DecisionMinConfidence = bifrost.Ptr(d.MinConfidence)
		if len(d.Criteria) > 0 {
			criteria, err := json.Marshal(d.Criteria)
			if err != nil {
				return nil, fmt.Errorf("encode decision criteria: %w", err)
			}
			row.DecisionCriteriaJSON = bifrost.Ptr(string(criteria))
		}
	}
	if l := cfg.LLM; l != nil {
		row.LLMProvider = bifrost.Ptr(string(l.Provider))
		row.LLMModel = bifrost.Ptr(l.Model)
		row.LLMTimeoutNS = bifrost.Ptr(int64(l.Timeout))
		row.LLMPrompt = bifrost.Ptr(l.Prompt)
		row.LLMMessageHistoryCount = bifrost.Ptr(l.MessageHistoryCount)
		row.LLMCountTowardBudgets = bifrost.Ptr(l.CountTowardBudgets)
	}
	if cfg.Session != nil {
		row.SessionEnabled = bifrost.Ptr(cfg.Session.Enabled)
	}
	if g := cfg.Suggestions; g != nil {
		row.SuggestionsEnabled = bifrost.Ptr(g.Enabled)
		row.SuggestionsSampleRate = bifrost.Ptr(g.SampleRate)
		row.SuggestionsLookbackDays = bifrost.Ptr(g.LookbackDays)
		row.SuggestionsMaxClusteredRequests = bifrost.Ptr(g.MaxClusteredRequests)
		row.SuggestionsMinClusterRequests = bifrost.Ptr(g.MinClusterRequests)
		row.SuggestionsProvider = bifrost.Ptr(string(g.Provider))
		row.SuggestionsModel = bifrost.Ptr(g.Model)
		row.SuggestionsInstructions = bifrost.Ptr(g.Instructions)
		if sc := g.Schedule; sc != nil {
			row.SuggestionsScheduleEvery = bifrost.Ptr(sc.Every)
			if sc.StartAt != nil {
				row.SuggestionsScheduleStartAt = bifrost.Ptr(sc.StartAt.UTC())
			}
		}
	}
	return row, nil
}

// modelRouterFromTable reads a row back into a router exactly as stored, with
// no normalization or validation. A column it cannot decode is reported as
// ErrConfigUnreadable.
func modelRouterFromTable(row *tables.TableModelRouter) (*ModelRouter, error) {
	r := &ModelRouter{
		ID:                    row.ID,
		Name:                  row.Name,
		Description:           row.Description,
		AnswersComplexityTier: row.AnswersComplexityTier,
		ConfigHash:            row.ConfigHash,
	}
	cfg := &r.ComplexityAnalyzerConfig
	cfg.Classifier = row.Classifier
	cfg.TierBoundaries = ComplexityTierBoundaries{SimpleMedium: row.SimpleMediumBoundary, MediumComplex: row.MediumComplexBoundary}
	cfg.EmbeddingFingerprint = row.EmbeddingFingerprint
	var err error
	if cfg.Keywords.SimpleKeywords, err = unmarshalModelRouterPhrases(row.SimplePhrasesJSON); err != nil {
		return nil, fmt.Errorf("%w: model router %q simple phrases: %w", ErrConfigUnreadable, row.ID, err)
	}
	if cfg.Keywords.MediumKeywords, err = unmarshalModelRouterPhrases(row.MediumPhrasesJSON); err != nil {
		return nil, fmt.Errorf("%w: model router %q medium phrases: %w", ErrConfigUnreadable, row.ID, err)
	}
	if cfg.Keywords.ComplexKeywords, err = unmarshalModelRouterPhrases(row.ComplexPhrasesJSON); err != nil {
		return nil, fmt.Errorf("%w: model router %q complex phrases: %w", ErrConfigUnreadable, row.ID, err)
	}
	cfg.ConfigHashes = decodeModelRouterHashes(row.ConfigHashesJSON)
	if row.SemanticProvider != nil {
		cfg.Semantic = &ComplexitySemanticConfig{
			Provider:            schemas.ModelProvider(*row.SemanticProvider),
			EmbeddingModel:      valueOrZero(row.SemanticEmbeddingModel),
			Timeout:             time.Duration(valueOrZero(row.SemanticTimeoutNS)),
			MinSimilarity:       valueOrZero(row.SemanticMinSimilarity),
			MessageHistoryCount: valueOrZero(row.SemanticMessageHistoryCount),
			CountTowardBudgets:  valueOrZero(row.SemanticCountTowardBudgets),
			VectorStore:         valueOrZero(row.SemanticVectorStore),
			Fallback:            valueOrZero(row.SemanticFallback),
		}
	}
	if row.DecisionProvider != nil {
		decision := &ComplexityDecisionConfig{
			Provider:      schemas.ModelProvider(*row.DecisionProvider),
			Model:         valueOrZero(row.DecisionModel),
			Timeout:       time.Duration(valueOrZero(row.DecisionTimeoutNS)),
			MinConfidence: valueOrZero(row.DecisionMinConfidence),
		}
		if row.DecisionPreviousMessageCount != nil {
			decision.PreviousMessageCount = bifrost.Ptr(*row.DecisionPreviousMessageCount)
		}
		if raw := valueOrZero(row.DecisionCriteriaJSON); strings.TrimSpace(raw) != "" {
			if err := json.Unmarshal([]byte(raw), &decision.Criteria); err != nil {
				return nil, fmt.Errorf("%w: model router %q decision criteria: %w", ErrConfigUnreadable, row.ID, err)
			}
		}
		cfg.Decision = decision
	}
	if row.LLMProvider != nil {
		cfg.LLM = &ComplexityLLMConfig{
			Provider:            schemas.ModelProvider(*row.LLMProvider),
			Model:               valueOrZero(row.LLMModel),
			Timeout:             time.Duration(valueOrZero(row.LLMTimeoutNS)),
			Prompt:              valueOrZero(row.LLMPrompt),
			MessageHistoryCount: valueOrZero(row.LLMMessageHistoryCount),
			CountTowardBudgets:  valueOrZero(row.LLMCountTowardBudgets),
		}
	}
	if row.SessionEnabled != nil {
		cfg.Session = &ComplexitySessionConfig{Enabled: *row.SessionEnabled}
	}
	if row.SuggestionsEnabled != nil {
		suggestions := &ComplexitySuggestionsConfig{
			Enabled:              *row.SuggestionsEnabled,
			SampleRate:           valueOrZero(row.SuggestionsSampleRate),
			LookbackDays:         valueOrZero(row.SuggestionsLookbackDays),
			MaxClusteredRequests: valueOrZero(row.SuggestionsMaxClusteredRequests),
			MinClusterRequests:   valueOrZero(row.SuggestionsMinClusterRequests),
			Provider:             schemas.ModelProvider(valueOrZero(row.SuggestionsProvider)),
			Model:                valueOrZero(row.SuggestionsModel),
			Instructions:         valueOrZero(row.SuggestionsInstructions),
		}
		if row.SuggestionsScheduleEvery != nil {
			suggestions.Schedule = &ComplexitySuggestionsSchedule{
				Every: *row.SuggestionsScheduleEvery,
			}
			if row.SuggestionsScheduleStartAt != nil {
				suggestions.Schedule.StartAt = bifrost.Ptr(row.SuggestionsScheduleStartAt.UTC())
			}
		}
		cfg.Suggestions = suggestions
	}
	return r, nil
}

// decodeModelRouterHashes reads the stored section hashes. They are
// bookkeeping, so a value this version cannot decode reads as none: the next
// config.json sync re-applies the file, which is what an unknown hash means.
func decodeModelRouterHashes(raw string) ComplexityAnalyzerConfigHashes {
	var hashes ComplexityAnalyzerConfigHashes
	if strings.TrimSpace(raw) == "" {
		return hashes
	}
	if err := json.Unmarshal([]byte(raw), &hashes); err != nil {
		return ComplexityAnalyzerConfigHashes{}
	}
	return hashes
}

// decodeModelRouterRow reads a row into a runnable router: decoded, normalized
// and validated. A row that does not validate is ErrConfigUnreadable, so
// callers with defaults degrade instead of failing.
func decodeModelRouterRow(row *tables.TableModelRouter) (*ModelRouter, error) {
	r, err := modelRouterFromTable(row)
	if err != nil {
		return nil, err
	}
	normalized := r.ComplexityAnalyzerConfig.Normalized()
	if err := normalized.Validate(); err != nil {
		return nil, fmt.Errorf("%w: invalid model router %q: %w", ErrConfigUnreadable, row.ID, err)
	}
	r.ComplexityAnalyzerConfig = normalized
	return r, nil
}

// isModelRouterPlaceholder reports the row a legacy complexity config save
// inserts to lock on when no router answers complexity_tier; see
// lockComplexityTierRouterRow. It is overwritten before its transaction
// commits, so it is only ever visible inside that transaction.
func isModelRouterPlaceholder(row *tables.TableModelRouter) bool {
	return row.Classifier == ""
}

// modelRouterDB returns the caller's transaction, or the store's DB.
func (s *RDBConfigStore) modelRouterDB(tx []*gorm.DB) *gorm.DB {
	if len(tx) > 0 && tx[0] != nil {
		return tx[0]
	}
	return s.DB()
}

// GetModelRouters returns every stored router, oldest first. A row this
// version cannot read is logged and left out rather than failing the list.
func (s *RDBConfigStore) GetModelRouters(ctx context.Context) ([]ModelRouter, error) {
	var rows []tables.TableModelRouter
	if err := s.DB().WithContext(ctx).Order("created_at ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	targets, err := loadModelRouterTargets(ctx, s.DB())
	if err != nil {
		return nil, err
	}
	routers := make([]ModelRouter, 0, len(rows))
	for i := range rows {
		if isModelRouterPlaceholder(&rows[i]) {
			continue
		}
		router, err := decodeModelRouterRow(&rows[i])
		if err != nil {
			if s.logger != nil {
				s.logger.Warn("skipping unreadable model router %q: %v", rows[i].ID, err)
			}
			continue
		}
		router.Targets = targets[router.ID]
		routers = append(routers, *router)
	}
	return routers, nil
}

// GetModelRouter returns one router. It is ErrNotFound when no row exists and
// ErrConfigUnreadable when the row exists but this version cannot run it.
func (s *RDBConfigStore) GetModelRouter(ctx context.Context, id string) (*ModelRouter, error) {
	router, err := s.getModelRouterWithDB(ctx, s.DB(), strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	if router == nil {
		return nil, ErrNotFound
	}
	return router, nil
}

// getModelRouterWithDB reads one router, returning nil when no row exists.
func (s *RDBConfigStore) getModelRouterWithDB(ctx context.Context, db *gorm.DB, id string) (*ModelRouter, error) {
	var row tables.TableModelRouter
	if err := db.WithContext(ctx).First(&row, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if isModelRouterPlaceholder(&row) {
		return nil, nil
	}
	router, err := decodeModelRouterRow(&row)
	if err != nil {
		return nil, err
	}
	targets, err := loadModelRouterTargets(ctx, db, id)
	if err != nil {
		return nil, err
	}
	router.Targets = targets[id]
	return router, nil
}

// CreateModelRouter stores a new router. A taken id or name is
// ErrAlreadyExists. A router created answering complexity_tier takes it over
// from the router that answered it.
func (s *RDBConfigStore) CreateModelRouter(ctx context.Context, router *ModelRouter, tx ...*gorm.DB) error {
	if router == nil {
		return fmt.Errorf("model router is nil")
	}
	r := *router
	normalizeModelRouterIdentity(&r)
	if err := validateModelRouterIdentity(&r); err != nil {
		return err
	}
	normalized := r.ComplexityAnalyzerConfig.Normalized()
	if err := normalized.Validate(); err != nil {
		return err
	}
	r.ComplexityAnalyzerConfig = normalized
	row, err := modelRouterToTable(&r)
	if err != nil {
		return err
	}
	return s.inModelRouterTx(ctx, tx, func(txDB *gorm.DB) error {
		db := txDB.WithContext(ctx)
		var taken int64
		if err := db.Model(&tables.TableModelRouter{}).Where("id = ? OR name = ?", r.ID, r.Name).Count(&taken).Error; err != nil {
			return err
		}
		if taken > 0 {
			return fmt.Errorf("%w: a model router with id %q or name %q", ErrAlreadyExists, r.ID, r.Name)
		}
		// The count and the insert do not share a lock, so a concurrent create
		// of the same id or name can still lose at the unique index.
		if err := db.Create(row).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) || isUniqueConstraintError(err) {
				return fmt.Errorf("%w: a model router with id %q or name %q", ErrAlreadyExists, r.ID, r.Name)
			}
			return err
		}
		if !r.AnswersComplexityTier {
			return nil
		}
		return s.takeOverComplexityTier(ctx, txDB, r)
	})
}

// UpdateModelRouter replaces a router's name, description, complexity_tier
// flag and config. Section hashes, the embedding fingerprint and the config
// hash the caller leaves empty keep their stored values, so a UI save never
// erases config.json bookkeeping. The router must already exist (ErrNotFound).
func (s *RDBConfigStore) UpdateModelRouter(ctx context.Context, router *ModelRouter, tx ...*gorm.DB) error {
	if router == nil {
		return fmt.Errorf("model router is nil")
	}
	r := *router
	normalizeModelRouterIdentity(&r)
	if err := validateModelRouterIdentity(&r); err != nil {
		return err
	}
	normalized := r.ComplexityAnalyzerConfig.Normalized()
	if err := normalized.Validate(); err != nil {
		return err
	}
	r.ComplexityAnalyzerConfig = normalized
	return s.inModelRouterTx(ctx, tx, func(txDB *gorm.DB) error {
		// A missing router is ErrNotFound even when its new name is taken.
		existing, err := s.lockModelRouterRow(ctx, txDB, r.ID)
		if err != nil {
			return err
		}
		if existing == nil {
			return ErrNotFound
		}
		var taken int64
		if err := txDB.WithContext(ctx).Model(&tables.TableModelRouter{}).
			Where("name = ? AND id <> ?", r.Name, r.ID).Count(&taken).Error; err != nil {
			return err
		}
		if taken > 0 {
			return fmt.Errorf("%w: a model router named %q", ErrAlreadyExists, r.Name)
		}
		_, err = s.saveModelRouterWithTx(ctx, txDB, r, false, true)
		return err
	})
}

// DeleteModelRouter removes a router and its targets. A router model routing
// rules still name cannot be deleted: that is a *ModelRouterInUseError listing
// them. The router's row is locked before the
// check, as a rule save locks it too, so a rule saved concurrently either
// commits first and is counted, or finds the router gone. Deleting the router
// that answers complexity_tier leaves complexity_tier on the built-in
// defaults.
func (s *RDBConfigStore) DeleteModelRouter(ctx context.Context, id string, tx ...*gorm.DB) error {
	id = strings.TrimSpace(id)
	return s.inModelRouterTx(ctx, tx, func(txDB *gorm.DB) error {
		row, err := s.lockModelRouterRow(ctx, txDB, id)
		if err != nil {
			return err
		}
		if row == nil {
			return ErrNotFound
		}
		names, err := modelRoutingRulesUsingRouter(ctx, txDB, id)
		if err != nil {
			return err
		}
		if len(names) > 0 {
			return &ModelRouterInUseError{RouterID: id, RuleNames: names}
		}
		if err := txDB.WithContext(ctx).Where("router_id = ?", id).Delete(&tables.TableModelRouterTarget{}).Error; err != nil {
			return err
		}
		return txDB.WithContext(ctx).Delete(&tables.TableModelRouter{}, "id = ?", id).Error
	})
}

// ResetModelRouter restores a router's tier boundaries and phrase lists from
// defaults, invalidates its embedding fingerprint since it named the phrases
// just replaced, and keeps everything else: the classifier settings, the
// section hashes, the router's name and whether it answers complexity_tier.
// It returns what was persisted.
//
// The read and the save share one transaction under the router's row lock, so
// a concurrent edit to a section the reset preserves cannot be overwritten by
// a stale copy. A stored config this version cannot read resets to the
// defaults, since that is the state reset exists to recover from.
func (s *RDBConfigStore) ResetModelRouter(ctx context.Context, id string, defaults *ComplexityAnalyzerConfig) (*ModelRouter, error) {
	if defaults == nil {
		return nil, fmt.Errorf("model router defaults are nil")
	}
	id = strings.TrimSpace(id)
	var persisted *ModelRouter
	err := s.DB().WithContext(ctx).Transaction(func(txDB *gorm.DB) error {
		row, err := s.lockModelRouterRow(ctx, txDB, id)
		if err != nil {
			return err
		}
		if row == nil {
			return ErrNotFound
		}
		persisted, err = s.resetModelRouterWithTx(ctx, txDB, row, defaults)
		return err
	})
	if err != nil {
		return nil, err
	}
	return persisted, nil
}

// resetModelRouterWithTx is ResetModelRouter on a row the caller has locked.
func (s *RDBConfigStore) resetModelRouterWithTx(ctx context.Context, txDB *gorm.DB, row *tables.TableModelRouter, defaults *ComplexityAnalyzerConfig) (*ModelRouter, error) {
	restored := ModelRouter{ID: row.ID, AnswersComplexityTier: row.AnswersComplexityTier, ComplexityAnalyzerConfig: *defaults}
	if !isModelRouterPlaceholder(row) {
		existing, err := decodeModelRouterRow(row)
		if err != nil && !errors.Is(err, ErrConfigUnreadable) {
			return nil, err
		}
		if existing != nil {
			restored = *existing
			restored.TierBoundaries = defaults.TierBoundaries
			restored.Keywords = defaults.Keywords
			restored.EmbeddingFingerprint = ""
		}
	}
	restored.ComplexityAnalyzerConfig = restored.ComplexityAnalyzerConfig.Normalized()
	if err := restored.ComplexityAnalyzerConfig.Validate(); err != nil {
		return nil, err
	}
	// carryOverFingerprint is false: an empty fingerprint here means the
	// reset just invalidated it, not that the caller left it out.
	return s.saveModelRouterWithTx(ctx, txDB, restored, true, false)
}

// inModelRouterTx runs fn in the caller's transaction, or in its own so the
// lock, the carry-over read and the save cannot interleave with another write.
func (s *RDBConfigStore) inModelRouterTx(ctx context.Context, tx []*gorm.DB, fn func(txDB *gorm.DB) error) error {
	if len(tx) > 0 && tx[0] != nil {
		return fn(tx[0])
	}
	return s.DB().WithContext(ctx).Transaction(fn)
}

// lockModelRouterRow takes the row lock that serializes saves and resets of one
// router, and returns the locked row, or nil when there is none.
//
// txDB must be a transaction, otherwise the lock is released at once.
func (s *RDBConfigStore) lockModelRouterRow(ctx context.Context, txDB *gorm.DB, id string) (*tables.TableModelRouter, error) {
	var row tables.TableModelRouter
	if err := dbForUpdate(txDB.WithContext(ctx)).First(&row, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

// getComplexityTierRouterWithDB reads the router that answers complexity_tier,
// or nil when none does. Should two rows carry the flag after racing saves,
// the one saved last wins, as it would have cleared the other.
func (s *RDBConfigStore) getComplexityTierRouterWithDB(ctx context.Context, db *gorm.DB) (*ModelRouter, error) {
	var row tables.TableModelRouter
	err := db.WithContext(ctx).Where("answers_complexity_tier = ?", true).
		Order("updated_at DESC, id ASC").First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if isModelRouterPlaceholder(&row) {
		return nil, nil
	}
	return decodeModelRouterRow(&row)
}

// lockComplexityTierRouterRow locks and returns the row the legacy complexity
// config API writes: the router that answers complexity_tier. With none, it
// is the router with LegacyComplexityRouterID, inserted first as a placeholder
// when there is no such row, so that a concurrent save and reset contend for
// the same row instead of both reading "absent" and both creating one; the
// caller's save overwrites the placeholder before commit and flags the row.
// A placeholder that cannot be inserted because another router already has
// its name is ErrAlreadyExists.
//
// txDB must be a transaction.
func (s *RDBConfigStore) lockComplexityTierRouterRow(ctx context.Context, txDB *gorm.DB) (*tables.TableModelRouter, error) {
	var flagged tables.TableModelRouter
	err := dbForUpdate(txDB.WithContext(ctx)).Where("answers_complexity_tier = ?", true).
		Order("updated_at DESC, id ASC").First(&flagged).Error
	if err == nil {
		return &flagged, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	placeholder := &tables.TableModelRouter{ID: LegacyComplexityRouterID, Name: LegacyComplexityRouterName}
	if err := txDB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(placeholder).Error; err != nil {
		return nil, err
	}
	row, err := s.lockModelRouterRow(ctx, txDB, LegacyComplexityRouterID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("%w: a model router named %q", ErrAlreadyExists, LegacyComplexityRouterName)
	}
	return row, nil
}

// takeOverComplexityTier makes r the only router that answers complexity_tier
// and writes its config into the legacy governance_config rows. txDB must be
// a transaction.
func (s *RDBConfigStore) takeOverComplexityTier(ctx context.Context, txDB *gorm.DB, r ModelRouter) error {
	if err := txDB.WithContext(ctx).Model(&tables.TableModelRouter{}).
		Where("id <> ? AND answers_complexity_tier = ?", r.ID, true).
		Update("answers_complexity_tier", false).Error; err != nil {
		return err
	}
	return s.writeLegacyComplexityRows(ctx, txDB, r.ComplexityAnalyzerConfig)
}

// saveModelRouterWithTx writes r under its row lock and returns what was
// written. Section hashes and the config hash that r leaves empty, and the
// embedding fingerprint when carryOverFingerprint is set, are read back from
// the stored row, which is locked first so a concurrent writer cannot slip a
// stale copy of either in between. They are read from the raw columns: stored
// bookkeeping this version cannot decode must not stop a valid save.
//
// keepIdentity keeps the stored name and description, and the stored
// complexity_tier flag unless r sets it, for saves that only carry a config.
// r's config must already be normalized and validated, and txDB must be a
// transaction.
//
// Saving the router that answers complexity_tier also rewrites the two
// governance_config rows the complexity config was stored in before model
// routers existed, so a rolled-back Bifrost still routes on it.
func (s *RDBConfigStore) saveModelRouterWithTx(ctx context.Context, txDB *gorm.DB, r ModelRouter, keepIdentity, carryOverFingerprint bool) (*ModelRouter, error) {
	existing, err := s.lockModelRouterRow(ctx, txDB, r.ID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, ErrNotFound
	}
	if keepIdentity && !r.AnswersComplexityTier {
		r.AnswersComplexityTier = existing.AnswersComplexityTier
	}
	if isModelRouterPlaceholder(existing) {
		if keepIdentity {
			r.Name, r.Description = existing.Name, existing.Description
		}
	} else {
		if r.ConfigHashes.Empty() {
			r.ConfigHashes = decodeModelRouterHashes(existing.ConfigHashesJSON)
		}
		if carryOverFingerprint && r.EmbeddingFingerprint == "" {
			r.EmbeddingFingerprint = existing.EmbeddingFingerprint
		}
		if keepIdentity {
			r.Name, r.Description, r.ConfigHash = existing.Name, existing.Description, existing.ConfigHash
		} else if r.ConfigHash == "" {
			r.ConfigHash = existing.ConfigHash
		}
	}
	row, err := modelRouterToTable(&r)
	if err != nil {
		return nil, err
	}
	row.CreatedAt = existing.CreatedAt
	row.UpdatedAt = time.Now()
	// The row is locked, so this is an update of every column, NULLs included.
	if err := txDB.WithContext(ctx).Save(row).Error; err != nil {
		return nil, err
	}
	if r.AnswersComplexityTier {
		if err := s.takeOverComplexityTier(ctx, txDB, r); err != nil {
			return nil, err
		}
	}
	return &r, nil
}

// writeLegacyComplexityRows writes the complexity_tier router's config into
// the two governance_config rows a Bifrost predating model routers reads it from, in
// the shapes those versions accept. Nothing in this version reads them back.
func (s *RDBConfigStore) writeLegacyComplexityRows(ctx context.Context, txDB *gorm.DB, config ComplexityAnalyzerConfig) error {
	raw, err := encodeComplexityAnalyzerConfig(config)
	if err != nil {
		return err
	}
	if err := s.UpdateConfig(ctx, &tables.TableGovernanceConfig{
		Key:   tables.ConfigComplexityAnalyzerConfigKey,
		Value: string(raw),
	}, txDB); err != nil {
		return err
	}
	semanticRaw, err := encodeComplexitySemanticConfigRow(config)
	if err != nil {
		return err
	}
	return s.UpdateConfig(ctx, &tables.TableGovernanceConfig{
		Key:   tables.ConfigComplexitySemanticConfigKey,
		Value: string(semanticRaw),
	}, txDB)
}
