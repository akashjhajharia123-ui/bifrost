package tables

import (
	"strings"
	"time"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"gorm.io/gorm"
)

// TableModelRoutingRule decides which requests a model router classifies. Its
// router then decides the tier and the model that tier gets. It runs after
// the routing rules, in its own step, and mirrors TableRoutingRule: a scope,
// an optional CEL condition and a priority unique within the scope. Its
// priorities are a separate space from routing rules', so the two never
// collide.
type TableModelRoutingRule struct {
	ID          string `gorm:"primaryKey;type:varchar(255)" json:"id"`
	ConfigHash  string `gorm:"type:varchar(255)" json:"config_hash"` // Hash of the config.json entry last applied, used for change detection
	Name        string `gorm:"type:varchar(255);not null;uniqueIndex:idx_model_routing_rule_scope_name" json:"name"`
	Description string `gorm:"type:text" json:"description"`
	Enabled     *bool  `gorm:"not null;default:true" json:"enabled,omitempty"` // nil = DB default (true); use EnabledValue() to read
	// CelExpression narrows which requests in the scope the rule takes; empty
	// takes all of them. It cannot use complexity_tier: the tier is what the
	// rule's router decides once the rule has matched.
	CelExpression string `gorm:"type:text" json:"cel_expression"`
	// Query is the dashboard's visual condition builder state, kept so an edit
	// reopens in the builder, as for routing rules. It is never evaluated;
	// CelExpression is.
	Query       *string        `gorm:"type:text" json:"-"`
	ParsedQuery map[string]any `gorm:"-" json:"query,omitempty"`
	// ModelRouterID names the router that classifies the request. Indexed so a
	// router still in use can be refused deletion.
	ModelRouterID string `gorm:"type:varchar(64);not null;index" json:"model_router_id"`

	Scope    string  `gorm:"type:varchar(50);not null;uniqueIndex:idx_model_routing_rule_scope_name" json:"scope"` // "global" | "team" | "customer" | "virtual_key" | "user"
	ScopeID  *string `gorm:"type:varchar(255);uniqueIndex:idx_model_routing_rule_scope_name" json:"scope_id"`      // nil for global, otherwise entity ID
	Priority int     `gorm:"type:int;not null;default:0;index" json:"priority"`                                    // Lower = evaluated first within scope

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

// TableName pins the physical table name.
func (TableModelRoutingRule) TableName() string { return "model_routing_rules" }

// BeforeSave serializes the condition builder state.
func (r *TableModelRoutingRule) BeforeSave(tx *gorm.DB) error {
	if r.ParsedQuery == nil {
		r.Query = nil
		return nil
	}
	data, err := sonic.Marshal(r.ParsedQuery)
	if err != nil {
		return err
	}
	r.Query = bifrost.Ptr(string(data))
	return nil
}

// AfterFind restores the condition builder state.
func (r *TableModelRoutingRule) AfterFind(tx *gorm.DB) error {
	if r.Query == nil || strings.TrimSpace(*r.Query) == "" {
		return nil
	}
	return sonic.Unmarshal([]byte(*r.Query), &r.ParsedQuery)
}

// EnabledValue returns the effective Enabled bool, treating nil as true (DB default).
func (r *TableModelRoutingRule) EnabledValue() bool {
	if r == nil {
		return false
	}
	if r.Enabled == nil {
		return true
	}
	return *r.Enabled
}
