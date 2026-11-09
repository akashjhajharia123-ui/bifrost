package tables

import (
	"strings"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"gorm.io/gorm"
)

// Complexity tiers a model router target applies to. They are the values a
// model router classifies a request into.
const (
	ModelRoutingTierSimple  = "SIMPLE"
	ModelRoutingTierMedium  = "MEDIUM"
	ModelRoutingTierComplex = "COMPLEX"
)

// IsModelRoutingTier reports whether tier is one a model router produces.
func IsModelRoutingTier(tier string) bool {
	switch tier {
	case ModelRoutingTierSimple, ModelRoutingTierMedium, ModelRoutingTierComplex:
		return true
	}
	return false
}

// TableModelRouterTarget is one model for one tier of a model router. A tier
// with no target keeps the request's model; one with several splits by
// weight, and the weights sum to 1 within the tier.
//
// RouterID carries no foreign key, as routing rule targets have none:
// DeleteModelRouter removes a router's targets with the router.
type TableModelRouterTarget struct {
	RouterID string  `gorm:"type:varchar(64);not null;index;uniqueIndex:idx_model_router_target_config" json:"-"`
	Tier     string  `gorm:"type:varchar(20);not null;uniqueIndex:idx_model_router_target_config" json:"tier"`
	Provider *string `gorm:"type:varchar(255);uniqueIndex:idx_model_router_target_config" json:"provider,omitempty"` // nil = keep the request's provider
	Model    *string `gorm:"type:varchar(255);uniqueIndex:idx_model_router_target_config" json:"model,omitempty"`    // nil = keep the request's model
	KeyID    *string `gorm:"type:varchar(255);uniqueIndex:idx_model_router_target_config" json:"key_id,omitempty"`   // persisted key pin
	Weight   float64 `gorm:"not null;default:1" json:"weight"`

	// Fallbacks replace the request's fallback chain when the target is chosen;
	// with none, the chain the routing rules set is kept.
	Fallbacks       *string           `gorm:"type:text" json:"-"`
	ParsedFallbacks []RoutingFallback `gorm:"-" json:"fallbacks,omitempty"`
}

// TableName pins the physical table name.
func (TableModelRouterTarget) TableName() string { return "model_router_targets" }

// BeforeSave serializes the fallback chain into its JSON column.
func (t *TableModelRouterTarget) BeforeSave(tx *gorm.DB) error {
	if len(t.ParsedFallbacks) == 0 {
		t.Fallbacks = nil
		return nil
	}
	data, err := sonic.Marshal(t.ParsedFallbacks)
	if err != nil {
		return err
	}
	t.Fallbacks = bifrost.Ptr(string(data))
	return nil
}

// AfterFind parses the fallback chain from its JSON column.
func (t *TableModelRouterTarget) AfterFind(tx *gorm.DB) error {
	if t.Fallbacks == nil || strings.TrimSpace(*t.Fallbacks) == "" {
		return nil
	}
	return sonic.Unmarshal([]byte(*t.Fallbacks), &t.ParsedFallbacks)
}
