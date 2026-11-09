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
	reloadedRouting []string
	removedRouters  []string
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

// ReloadModelRouterRouting records a routing reload.
func (m *modelRouterManagerFake) ReloadModelRouterRouting(_ context.Context, id string) error {
	m.reloadedRouting = append(m.reloadedRouting, id)
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
// before one is created, a router is created off, renamed and reset, and
// deleted.
func TestModelRouterEndpoints(t *testing.T) {
	h, manager, _ := modelRouterHandler(t)

	status, body := call(t, h.listModelRouters, "", nil)
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Empty(t, body["routers"], "no router exists until one is created")
	assert.NotNil(t, body["routers"], "the list is empty, not null")

	status, body = call(t, h.createModelRouter, routerBody("support", "Support"), nil)
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	assert.Equal(t, "Support", body["name"])
	assert.Equal(t, map[string]any{"enabled": false, "scope": "global", "priority": float64(0)}, body["routing"], "a new router starts off")
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

	status, _ = call(t, h.deleteModelRouter, "", map[string]string{"router_id": "default"})
	assert.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, []string{"default"}, manager.removedRouters)
	status, _ = call(t, h.deleteModelRouter, "", map[string]string{"router_id": "default"})
	assert.Equal(t, fasthttp.StatusNotFound, status)

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

// TestModelRouterRoutingEndpoint pins how a router's routing is set: on its
// own endpoint, validated, served without reloading the router's classifiers,
// and left alone by a save of the router's config.
func TestModelRouterRoutingEndpoint(t *testing.T) {
	h, manager, _ := modelRouterHandler(t)
	status, body := call(t, h.createModelRouter, routerBody("support", "Support"), nil)
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	status, body = call(t, h.createModelRouter, routerBody("billing", "Billing"), nil)
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	assert.Equal(t, float64(1), body["routing"].(map[string]any)["priority"], "a new router goes after the last global one")
	manager.reloadedRouters = nil

	routing := `{"enabled":true,"scope":"global","cel_expression":"model == \"support-assistant\"","query":{"combinator":"and"},"priority":4}`
	status, body = call(t, h.setModelRouterRouting, routing, map[string]string{"router_id": "support"})
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	saved := body["routing"].(map[string]any)
	assert.Equal(t, true, saved["enabled"])
	assert.Equal(t, `model == "support-assistant"`, saved["cel_expression"])
	assert.Equal(t, map[string]any{"combinator": "and"}, saved["query"])
	assert.Equal(t, []string{"support"}, manager.reloadedRouting)
	assert.Empty(t, manager.reloadedRouters, "new routing never reconfigures the classifiers")

	status, body = call(t, h.updateModelRouter, `{"name":"Support","keywords":{"simple_keywords":["s"],"medium_keywords":["m"],"complex_keywords":["c"]},"routing":{"enabled":false,"scope":"global","priority":9}}`, map[string]string{"router_id": "support"})
	require.Equal(t, fasthttp.StatusOK, status, "%v", body)
	assert.Equal(t, true, body["routing"].(map[string]any)["enabled"], "saving the router's config leaves its routing alone")

	status, _ = call(t, h.setModelRouterRouting, `{"enabled":true,"scope":"global","priority":4}`, map[string]string{"router_id": "billing"})
	assert.Equal(t, fasthttp.StatusConflict, status, "priority 4 in global is taken")

	invalid := map[string]string{
		"complexity_tier in the condition": `{"scope":"global","cel_expression":"complexity_tier == \"SIMPLE\""}`,
		"condition that does not compile":  `{"scope":"global","cel_expression":"model =="}`,
		"team without a team":              `{"scope":"team","scope_id":"no-such-team"}`,
		"team without an id":               `{"scope":"team"}`,
		"unknown scope":                    `{"scope":"org"}`,
	}
	for name, payload := range invalid {
		t.Run(name, func(t *testing.T) {
			status, _ := call(t, h.setModelRouterRouting, payload, map[string]string{"router_id": "billing"})
			assert.Equal(t, fasthttp.StatusBadRequest, status)
		})
	}

	status, _ = call(t, h.setModelRouterRouting, `{"scope":"global"}`, map[string]string{"router_id": "missing"})
	assert.Equal(t, fasthttp.StatusNotFound, status)

	status, body = call(t, h.createModelRouter, `{"id":"sales","name":"Sales","keywords":{"simple_keywords":["s"],"medium_keywords":["m"],"complex_keywords":["c"]},"routing":{"enabled":true,"scope":"global","priority":4}}`, nil)
	assert.Equal(t, fasthttp.StatusConflict, status, "a create with routing is checked too: %v", body)
}
