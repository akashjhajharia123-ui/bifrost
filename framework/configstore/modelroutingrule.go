package configstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
)

// ErrModelRoutingRulePriorityTaken is returned when another model routing rule
// in the same scope and scope ID already has the priority. It wraps
// ErrAlreadyExists, as ErrRoutingRulePriorityTaken does.
var ErrModelRoutingRulePriorityTaken = fmt.Errorf("%w", ErrAlreadyExists)

// ErrUnknownModelRouter is returned when a model routing rule names a router
// that does not exist.
var ErrUnknownModelRouter = errors.New("unknown model router")

// ErrModelRouterInUse is returned when deleting a router that model routing
// rules still name. The error is a *ModelRouterInUseError naming them.
var ErrModelRouterInUse = errors.New("model router is used by model routing rules")

// ModelRouterInUseError names the model routing rules that keep a router from
// being deleted.
type ModelRouterInUseError struct {
	RouterID  string
	RuleNames []string
}

// Error lists the rules by name so the caller knows what to change first.
func (e *ModelRouterInUseError) Error() string {
	return fmt.Sprintf("model router %q is used by model routing rules: %s", e.RouterID, strings.Join(e.RuleNames, ", "))
}

// Unwrap lets callers match ErrModelRouterInUse.
func (e *ModelRouterInUseError) Unwrap() error { return ErrModelRouterInUse }

// validModelRoutingScopes are the scopes a model routing rule can take, the
// same as routing rules'.
var validModelRoutingScopes = map[string]bool{
	"global":      true,
	"team":        true,
	"customer":    true,
	"virtual_key": true,
	"user":        true,
}

// ValidateModelRoutingRule checks what a model routing rule can be checked for
// on its own: identity, scope and router. It trims the name and
// defaults an empty scope to global in place. Whether the scope ID and the
// router exist, and whether the CEL expression compiles, are checked by the
// store and the caller respectively.
func ValidateModelRoutingRule(rule *tables.TableModelRoutingRule) error {
	if rule == nil {
		return fmt.Errorf("model routing rule is nil")
	}
	rule.Name = strings.TrimSpace(rule.Name)
	rule.ModelRouterID = strings.TrimSpace(rule.ModelRouterID)
	if rule.Name == "" {
		return fmt.Errorf("model routing rule name is required")
	}
	if rule.Scope == "" {
		rule.Scope = "global"
	}
	if !validModelRoutingScopes[rule.Scope] {
		return fmt.Errorf("invalid scope %q: must be one of: global, team, customer, virtual_key, user", rule.Scope)
	}
	if rule.Scope == "global" {
		rule.ScopeID = nil
	} else if rule.ScopeID == nil || strings.TrimSpace(*rule.ScopeID) == "" {
		return fmt.Errorf("scope_id is required when scope is not global")
	}
	if rule.ModelRouterID == "" {
		return fmt.Errorf("model_router_id is required")
	}
	return nil
}

// modelRoutingRuleHashPayload is the canonical form a config.json model
// routing rule is hashed in.
type modelRoutingRuleHashPayload struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	Enabled       bool   `json:"enabled"`
	CelExpression string `json:"cel_expression"`
	ModelRouterID string `json:"model_router_id"`
	Scope         string `json:"scope"`
	ScopeID       string `json:"scope_id"`
	Priority      int    `json:"priority"`
}

// GenerateModelRoutingRuleHash hashes a model routing rule for config.json
// change detection. Like a routing rule, the rule is one unit: any change to
// the file's entry re-applies the whole rule.
func GenerateModelRoutingRuleHash(rule tables.TableModelRoutingRule) (string, error) {
	payload := modelRoutingRuleHashPayload{
		ID:            rule.ID,
		Name:          rule.Name,
		Description:   rule.Description,
		Enabled:       rule.EnabledValue(),
		CelExpression: rule.CelExpression,
		ModelRouterID: rule.ModelRouterID,
		Scope:         rule.Scope,
		ScopeID:       derefStr(rule.ScopeID),
		Priority:      rule.Priority,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// loadModelRoutingRules reads rules in the order routing rules are read in
// (loadRoutingRulesOrdered): priority ASC, created_at DESC, id ASC.
func (s *RDBConfigStore) loadModelRoutingRules(ctx context.Context, db *gorm.DB, where ...func(*gorm.DB) *gorm.DB) ([]tables.TableModelRoutingRule, error) {
	query := db.WithContext(ctx).Order("priority ASC, created_at DESC, id ASC")
	for _, w := range where {
		query = w(query)
	}
	var rules []tables.TableModelRoutingRule
	if err := query.Find(&rules).Error; err != nil {
		return nil, err
	}
	return rules, nil
}

// GetModelRoutingRules returns every model routing rule.
func (s *RDBConfigStore) GetModelRoutingRules(ctx context.Context) ([]tables.TableModelRoutingRule, error) {
	return s.loadModelRoutingRules(ctx, s.DB())
}

// GetModelRoutingRule returns one model routing rule, or ErrNotFound.
func (s *RDBConfigStore) GetModelRoutingRule(ctx context.Context, id string) (*tables.TableModelRoutingRule, error) {
	rules, err := s.loadModelRoutingRules(ctx, s.DB(), func(q *gorm.DB) *gorm.DB { return q.Where("id = ?", id) })
	if err != nil {
		return nil, err
	}
	if len(rules) == 0 {
		return nil, ErrNotFound
	}
	return &rules[0], nil
}

// checkModelRoutingRuleRefs checks, inside the write's transaction, that the
// rule's router exists and that no other rule in its scope has its priority.
func (s *RDBConfigStore) checkModelRoutingRuleRefs(ctx context.Context, tx *gorm.DB, rule *tables.TableModelRoutingRule) error {
	// Locked so a concurrent DeleteModelRouter, which locks the row before
	// counting the rules that name it, cannot miss this one.
	router, err := s.lockModelRouterRow(ctx, tx, rule.ModelRouterID)
	if err != nil {
		return err
	}
	if router == nil || isModelRouterPlaceholder(router) {
		return fmt.Errorf("%w %q", ErrUnknownModelRouter, rule.ModelRouterID)
	}
	query := tx.WithContext(ctx).Model(&tables.TableModelRoutingRule{}).
		Where("scope = ? AND priority = ? AND id != ?", rule.Scope, rule.Priority, rule.ID)
	if rule.ScopeID != nil {
		query = query.Where("scope_id = ?", *rule.ScopeID)
	} else {
		query = query.Where("scope_id IS NULL")
	}
	var taken int64
	if err := query.Count(&taken).Error; err != nil {
		return err
	}
	if taken > 0 {
		if rule.ScopeID != nil {
			return fmt.Errorf("model routing rule with priority %d %w for scope '%s' with scope ID '%s'", rule.Priority, ErrModelRoutingRulePriorityTaken, rule.Scope, *rule.ScopeID)
		}
		return fmt.Errorf("model routing rule with priority %d %w for scope '%s'", rule.Priority, ErrModelRoutingRulePriorityTaken, rule.Scope)
	}
	return nil
}

// CreateModelRoutingRule validates and stores a new model routing rule.
func (s *RDBConfigStore) CreateModelRoutingRule(ctx context.Context, rule *tables.TableModelRoutingRule, tx ...*gorm.DB) error {
	if err := ValidateModelRoutingRule(rule); err != nil {
		return err
	}
	return s.parseGormError(s.modelRouterDB(tx).WithContext(ctx).Transaction(func(txDB *gorm.DB) error {
		if err := s.checkModelRoutingRuleRefs(ctx, txDB, rule); err != nil {
			return err
		}
		return txDB.Create(rule).Error
	}))
}

// UpdateModelRoutingRule replaces a model routing rule. An
// omitted Enabled keeps the stored value, and created_at never changes.
func (s *RDBConfigStore) UpdateModelRoutingRule(ctx context.Context, rule *tables.TableModelRoutingRule, tx ...*gorm.DB) error {
	if err := ValidateModelRoutingRule(rule); err != nil {
		return err
	}
	return s.parseGormError(s.modelRouterDB(tx).WithContext(ctx).Transaction(func(txDB *gorm.DB) error {
		var existing tables.TableModelRoutingRule
		if err := dbForUpdate(txDB).First(&existing, "id = ?", rule.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if err := s.checkModelRoutingRuleRefs(ctx, txDB, rule); err != nil {
			return err
		}
		rule.CreatedAt = existing.CreatedAt
		if rule.Enabled == nil {
			rule.Enabled = existing.Enabled
		}
		return txDB.Save(rule).Error
	}))
}

// DeleteModelRoutingRule removes a model routing rule.
func (s *RDBConfigStore) DeleteModelRoutingRule(ctx context.Context, id string, tx ...*gorm.DB) error {
	result := s.modelRouterDB(tx).WithContext(ctx).Delete(&tables.TableModelRoutingRule{}, "id = ?", id)
	if result.Error != nil {
		return s.parseGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// modelRoutingRulesUsingRouter names the rules that select a router, for the
// guard that keeps a router in use from being deleted.
func modelRoutingRulesUsingRouter(ctx context.Context, db *gorm.DB, routerID string) ([]string, error) {
	var names []string
	if err := db.WithContext(ctx).Model(&tables.TableModelRoutingRule{}).
		Where("model_router_id = ?", routerID).Order("name ASC").Pluck("name", &names).Error; err != nil {
		return nil, err
	}
	return names, nil
}
