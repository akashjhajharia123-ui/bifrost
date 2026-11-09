package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
)

// modelRouterManagerFake records what the model router endpoints serve.
type modelRouterManagerFake struct {
	mockRoutingManager
	reloadedRouters []string
	reloadedTargets []string
	removedRouters  []string
	reloadedRules   []string
	removedRules    []string
}

// ReloadModelRouterTargets records a targets reload.
func (m *modelRouterManagerFake) ReloadModelRouterTargets(_ context.Context, id string) error {
	m.reloadedTargets = append(m.reloadedTargets, id)
	return nil
}

// ReloadModelRouter records a router reload.
func (m *modelRouterManagerFake) ReloadModelRouter(_ context.Context, id string) error {
	m.reloadedRouters = append(m.reloadedRouters, id)
	return nil
}

// RemoveModelRouter records a router removal.
func (m *modelRouterManagerFake) RemoveModelRouter(_ context.Context, id string) error {
	m.removedRouters = append(m.removedRouters, id)
	return nil
}

// ReloadModelRoutingRule records a rule reload.
func (m *modelRouterManagerFake) ReloadModelRoutingRule(_ context.Context, id string) error {
	m.reloadedRules = append(m.reloadedRules, id)
	return nil
}

// RemoveModelRoutingRule records a rule removal.
func (m *modelRouterManagerFake) RemoveModelRoutingRule(_ context.Context, id string) error {
	m.removedRules = append(m.removedRules, id)
	return nil
}

// GetModelRouterStatus reports every known router as disabled.
func (m *modelRouterManagerFake) GetModelRouterStatus(_ context.Context, id string) (complexity.SemanticStatusInfo, complexity.LLMStatusInfo, error) {
	if id == "missing" {
		return complexity.SemanticStatusInfo{}, complexity.LLMStatusInfo{}, configstore.ErrNotFound
	}
	return complexity.SemanticStatusInfo{State: complexity.SemanticStatusDisabled}, complexity.LLMStatusInfo{State: complexity.LLMStatusDisabled}, nil
}

// modelRouterHandler builds a routing handler over a real SQLite store.
func modelRouterHandler(t *testing.T) (*RoutingHandler, *modelRouterManagerFake, configstore.ConfigStore) {
	t.Helper()
	SetLogger(&mockLogger{})
	store := setupPricingOverrideHandlerStore(t)
	manager := &modelRouterManagerFake{}
	return &RoutingHandler{configStore: store, routingManager: manager}, manager, store
}

// call runs a handler with a body and path parameters and returns the status
// and decoded JSON body.
func call(t *testing.T, handler fasthttp.RequestHandler, body string, params map[string]string) (int, map[string]any) {
	t.Helper()
	ctx := newTestRequestCtx(body)
	for key, value := range params {
		ctx.SetUserValue(key, value)
	}
	handler(ctx)
	var decoded map[string]any
	_ = json.Unmarshal(ctx.Response.Body(), &decoded)
	return ctx.Response.StatusCode(), decoded
}

// routerBody is a router payload with one phrase per tier.
func routerBody(id, name string) string {
	return `{"id":"` + id + `","name":"` + name + `","keywords":{"simple_keywords":["s"],"medium_keywords":["m"],"complex_keywords":["c"]}}`
}

// TestModelRouterEndpoints walks a router through the API: none is listed
// before one is created, a router is created, renamed and reset, deleting is
// refused for a router a rule names, and the complexity_tier flag is kept
// unless an update names it.
func TestModelRouterEndpoints(t *testing.T) {
	h, manager, _ := modelRouterHandler(t)

	status, body := call(t, h.listModelRouters, "", nil)
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Empty(t, body["routers"], "no router exists until one is created")
	assert.NotNil(t, body["routers"], "the list is empty, not null")

	status, body = call(t, h.createModelRouter, routerBody("support", "Support"), nil)
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	assert.Equal(t, "Support", body["name"])
	assert.Equal(t, []string{"support"}, manager.reloadedRouters)

	status, _ = call(t, h.createModelRouter, routerBody("support", "Again"), nil)
	assert.Equal(t, fasthttp.StatusConflict, status)
	status, _ = call(t, h.createModelRouter, `{"id":"default","name":"Default","keywords":{"simple_keywords":["s"],"medium_keywords":["m"],"complex_keywords":["c"]}}`, nil)
	assert.Equal(t, fasthttp.StatusOK, status, "no id or name is reserved")
	status, _ = call(t, h.createModelRouter, `{"name":"Typo","keywords":{"simple_keywords":["s"],"medium_keywords":["m"],"complex_keywords":["c"]},"semantik":{}}`, nil)
	assert.Equal(t, fasthttp.StatusBadRequest, status, "a misspelled setting is rejected, not dropped")

	status, body = call(t, h.createModelRouter, `{"name":"Generated","keywords":{"simple_keywords":["s"],"medium_keywords":["m"],"complex_keywords":["c"]}}`, nil)
	require.Equal(t, fasthttp.StatusOK, status)
	assert.NotEmpty(t, body["id"], "an omitted id is generated")

	status, body = call(t, h.updateModelRouter, routerBody("ignored", "Customer support"), map[string]string{"router_id": "support"})
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	assert.Equal(t, "support", body["id"], "the path names the router")
	assert.Equal(t, "Customer support", body["name"])

	status, _ = call(t, h.updateModelRouter, routerBody("", "Nobody"), map[string]string{"router_id": "missing"})
	assert.Equal(t, fasthttp.StatusNotFound, status)

	status, body = call(t, h.resetModelRouter, "", map[string]string{"router_id": "support"})
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	assert.Equal(t, "Customer support", body["name"], "a reset keeps the name")

	status, body = call(t, h.createModelRoutingRule, `{"name":"Support desk","model_router_id":"support"}`, nil)
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)

	status, body = call(t, h.deleteModelRouter, "", map[string]string{"router_id": "support"})
	assert.Equal(t, fasthttp.StatusConflict, status)
	errBody, _ := body["error"].(map[string]any)
	assert.Contains(t, errBody["message"], "Support desk", "the refusal names the rule in the way")
	assert.Empty(t, manager.removedRouters)

	general := `{"name":"General","answers_complexity_tier":true,"keywords":{"simple_keywords":["s"],"medium_keywords":["m"],"complex_keywords":["c"]}}`
	status, body = call(t, h.updateModelRouter, general, map[string]string{"router_id": "default"})
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	assert.Equal(t, true, body["answers_complexity_tier"])
	general = `{"name":"General settings","keywords":{"simple_keywords":["s"],"medium_keywords":["m"],"complex_keywords":["c"]}}`
	status, body = call(t, h.updateModelRouter, general, map[string]string{"router_id": "default"})
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	assert.Equal(t, true, body["answers_complexity_tier"], "an update that leaves the flag out keeps it")
	general = `{"name":"General","answers_complexity_tier":false,"keywords":{"simple_keywords":["s"],"medium_keywords":["m"],"complex_keywords":["c"]}}`
	status, body = call(t, h.updateModelRouter, general, map[string]string{"router_id": "default"})
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	assert.Nil(t, body["answers_complexity_tier"], "an update that sets it false turns it off")
	status, body = call(t, h.updateModelRouter, `{"name":"General","answers_complexity_tier":true,"keywords":{"simple_keywords":["s"],"medium_keywords":["m"],"complex_keywords":["c"]}}`, map[string]string{"router_id": "default"})
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	status, _ = call(t, h.deleteModelRouter, "", map[string]string{"router_id": "default"})
	assert.Equal(t, fasthttp.StatusOK, status, "the router that answers complexity_tier can be deleted")
	assert.Equal(t, []string{"default"}, manager.removedRouters)

	status, _ = call(t, h.getModelRouterStatus, "", map[string]string{"router_id": "missing"})
	assert.Equal(t, fasthttp.StatusNotFound, status)
	status, _ = call(t, h.getModelRouterStatus, "", map[string]string{"router_id": "support"})
	assert.Equal(t, fasthttp.StatusOK, status)
}

// TestModelRouterTargetsEndpoint pins how a router's targets are set: on their
// own endpoint, validated, served without reloading the router's classifiers,
// and left alone by a save of the router's config.
func TestModelRouterTargetsEndpoint(t *testing.T) {
	h, manager, _ := modelRouterHandler(t)
	status, body := call(t, h.createModelRouter, routerBody("support", "Support"), nil)
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	manager.reloadedRouters = nil

	targets := `{"targets":[{"tier":"SIMPLE","provider":"openai","model":"gpt-4o-mini","weight":1},{"tier":"COMPLEX","provider":"anthropic","model":"claude-opus","weight":1,"fallbacks":["openai/gpt-4o"]}]}`
	status, body = call(t, h.setModelRouterTargets, targets, map[string]string{"router_id": "support"})
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	assert.Len(t, body["targets"], 2)
	assert.Equal(t, []string{"support"}, manager.reloadedTargets)
	assert.Empty(t, manager.reloadedRouters, "new targets never reconfigure the classifiers")

	status, body = call(t, h.updateModelRouter, `{"name":"Support","keywords":{"simple_keywords":["s"],"medium_keywords":["m"],"complex_keywords":["c"]},"targets":[]}`, map[string]string{"router_id": "support"})
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	assert.Len(t, body["targets"], 2, "saving the router's config leaves its targets alone")

	invalid := map[string]string{
		"weights do not sum":         `{"targets":[{"tier":"SIMPLE","model":"a","weight":0.5}]}`,
		"unknown tier":               `{"targets":[{"tier":"HARD","model":"a","weight":1}]}`,
		"config-only fallback field": `{"targets":[{"tier":"SIMPLE","model":"m","weight":1,"fallbacks":[{"provider":"openai","model":"gpt-4o","provider_key_name":"k"}]}]}`,
	}
	for name, payload := range invalid {
		t.Run(name, func(t *testing.T) {
			status, _ := call(t, h.setModelRouterTargets, payload, map[string]string{"router_id": "support"})
			assert.Equal(t, fasthttp.StatusBadRequest, status)
		})
	}

	status, _ = call(t, h.setModelRouterTargets, targets, map[string]string{"router_id": "missing"})
	assert.Equal(t, fasthttp.StatusNotFound, status)

	status, body = call(t, h.setModelRouterTargets, `{"targets":[]}`, map[string]string{"router_id": "support"})
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	assert.Nil(t, body["targets"], "no targets clears them")
}

// TestModelRouterDefaultsEndpoint pins what a new router starts from: the
// built-in phrases and the guidance each model-based classifier sends.
func TestModelRouterDefaultsEndpoint(t *testing.T) {
	h, _, _ := modelRouterHandler(t)
	status, body := call(t, h.getModelRouterDefaults, "", nil)
	require.Equal(t, fasthttp.StatusOK, status)
	config := body["config"].(map[string]any)
	keywords := config["keywords"].(map[string]any)
	assert.NotEmpty(t, keywords["simple_keywords"])
	assert.NotEmpty(t, keywords["complex_keywords"])
	assert.NotEmpty(t, body["llm_default_prompt"])
	assert.NotEmpty(t, body["decision_defaults"])
}

// TestModelRoutingRuleEndpoints walks a model routing rule through the API
// and pins the checks a write makes beyond the store's.
func TestModelRoutingRuleEndpoints(t *testing.T) {
	h, manager, _ := modelRouterHandler(t)
	status, body := call(t, h.createModelRouter, routerBody("general", "General"), nil)
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)

	rule := `{"name":"Support desk","model_router_id":"general","cel_expression":"model == \"engineering-assistant\""}`
	status, body = call(t, h.createModelRoutingRule, rule, nil)
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	id := body["id"].(string)
	assert.Equal(t, "global", body["scope"])
	assert.Equal(t, []string{id}, manager.reloadedRules)

	invalid := map[string]string{
		"complexity_tier in the condition": `{"name":"x","model_router_id":"general","cel_expression":"complexity_tier == \"SIMPLE\""}`,
		"unknown router":                   `{"name":"x","model_router_id":"ghost"}`,
		"no router":                        `{"name":"x"}`,
		"team without a team":              `{"name":"x","model_router_id":"general","scope":"team","scope_id":"no-such-team"}`,
	}
	for name, payload := range invalid {
		t.Run(name, func(t *testing.T) {
			status, _ := call(t, h.createModelRoutingRule, payload, nil)
			assert.Equal(t, fasthttp.StatusBadRequest, status)
		})
	}

	status, _ = call(t, h.createModelRoutingRule, `{"name":"Second","model_router_id":"general"}`, nil)
	assert.Equal(t, fasthttp.StatusConflict, status, "priority 0 in global is taken")

	status, body = call(t, h.updateModelRoutingRule, `{"name":"Support desk","model_router_id":"general","enabled":false}`, map[string]string{"rule_id": id})
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	assert.Equal(t, false, body["enabled"])

	status, body = call(t, h.listModelRoutingRules, "", nil)
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, float64(1), body["count"])

	status, _ = call(t, h.deleteModelRoutingRule, "", map[string]string{"rule_id": id})
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, []string{id}, manager.removedRules)
	status, _ = call(t, h.getModelRoutingRule, "", map[string]string{"rule_id": id})
	assert.Equal(t, fasthttp.StatusNotFound, status)
}
