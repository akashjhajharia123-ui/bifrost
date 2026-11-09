import { expect, test } from '../../core/fixtures/base.fixture'
import { assertToast } from '../../core/utils/test-helpers'

// The routers, their status and defaults, the model routing rules and the
// providers are mocked, so these tests depend neither on an embedding provider
// nor on what the gateway holds.
test.describe('Model Routers', () => {
  const KEYWORDS = { simple_keywords: ['hello'], medium_keywords: ['summarize this thread'], complex_keywords: ['design a system'] }
  const EVERY_TIER = ['SIMPLE', 'MEDIUM', 'COMPLEX'].map((tier) => ({ tier, provider: 'openai', model: 'gpt-4o-mini', weight: 1 }))
  const ROUTERS = [
    {
      id: 'general',
      name: 'General',
      answers_complexity_tier: true,
      classifier: 'semantic',
      keywords: KEYWORDS,
      semantic: { provider: 'openai', embedding_model: 'text-embedding-3-small', timeout: '2000ms', min_similarity: 0.4, vector_store: 'embedded', fallback: 'none' },
    },
    {
      id: 'support',
      name: 'Support',
      description: 'Help desk traffic',
      classifier: 'decision',
      keywords: KEYWORDS,
      decision: { provider: 'openai', model: 'gpt-4o-mini', previous_message_count: 1, timeout: '3000ms', min_confidence: 0.7 },
      targets: EVERY_TIER,
    },
    {
      id: 'billing',
      name: 'Billing',
      classifier: 'llm',
      keywords: KEYWORDS,
      llm: { provider: 'openai', model: 'gpt-4o-mini', timeout: '4000ms', message_history_count: 1 },
    },
  ]
  const RULES = [
    {
      id: 'desk',
      name: 'Support desk',
      description: '',
      enabled: true,
      cel_expression: 'model == "support-assistant"',
      model_router_id: 'support',
      scope: 'global',
      priority: 0,
      created_at: '2026-11-09T00:00:00Z',
      updated_at: '2026-11-09T00:00:00Z',
    },
  ]
  let deleteAttempts = 0
  let createdBodies: Record<string, unknown>[] = []
  let targetBodies: Record<string, unknown>[] = []

  test.beforeEach(async ({ page }) => {
    deleteAttempts = 0
    createdBodies = []
    targetBodies = []
    await page.route(
      (url) => url.pathname === '/api/routing/model-routers',
      async (route) => {
        if (route.request().method() === 'POST') {
          const body = route.request().postDataJSON() as Record<string, unknown>
          createdBodies.push(body)
          return route.fulfill({ json: { ...body, id: 'engineering' } })
        }
        return route.fulfill({ json: { routers: ROUTERS, count: ROUTERS.length } })
      },
    )
    await page.route(
      (url) => url.pathname === '/api/routing/model-routers/defaults',
      (route) =>
        route.fulfill({
          json: {
            config: { classifier: 'semantic', keywords: KEYWORDS },
            decision_defaults: { criteria: {} },
            llm_default_prompt: 'Classify the request.',
          },
        }),
    )
    await page.route(
      (url) => /^\/api\/routing\/model-routers\/[^/]+\/status$/.test(url.pathname),
      (route) => route.fulfill({ json: { state: 'ready', loaded: 3, total: 3, cached_phrases: 3 } }),
    )
    await page.route(
      (url) => /^\/api\/routing\/model-routers\/[^/]+\/targets$/.test(url.pathname),
      (route) => {
        const body = route.request().postDataJSON() as Record<string, unknown>
        targetBodies.push(body)
        const id = new URL(route.request().url()).pathname.split('/')[4]
        return route.fulfill({ json: { ...ROUTERS.find((router) => router.id === id), ...body } })
      },
    )
    await page.route(
      (url) => /^\/api\/routing\/model-routers\/[^/]+$/.test(url.pathname) && !url.pathname.endsWith('/defaults'),
      async (route) => {
        if (route.request().method() === 'DELETE') {
          deleteAttempts++
          return route.fulfill({ status: 409, json: { error: { message: 'model router "support" is used by model routing rules: Support desk' } } })
        }
        const id = new URL(route.request().url()).pathname.split('/').pop()
        return route.fulfill({ json: ROUTERS.find((router) => router.id === id) ?? { ...createdBodies[0], id } })
      },
    )
    await page.route(
      (url) => url.pathname === '/api/routing/model-routing-rules',
      (route) => route.fulfill({ json: { rules: RULES, count: RULES.length } }),
    )
    await page.route(
      (url) => url.pathname === '/api/logs/rankings/by-dimension',
      (route) => route.fulfill({ json: { rankings: [{ id: 'support', name: 'Support', total_requests: 41960 }] } }),
    )
    await page.route(
      (url) => url.pathname === '/api/providers',
      (route) => route.fulfill({ json: { providers: [{ name: 'openai', provider_status: 'active', keys: [] }] } }),
    )
    await page.route(
      (url) => url.pathname === '/api/keys',
      (route) => route.fulfill({ json: [{ key_id: 'openai-key', name: 'OpenAI', provider: 'openai', enabled: true }] }),
    )
  })

  test.afterEach(async ({ page }) => {
    await page.unrouteAll({ behavior: 'ignoreErrors' })
  })

  test('should list routers with their classifier, routing, traffic and status', async ({ page }) => {
    await page.goto('/workspace/model-routers')
    const general = page.getByTestId('model-router-row-general')
    await expect(general).toContainText('General')
    // Whether a router answers complexity_tier is not shown; it is judged like any other router.
    await expect(general).not.toContainText('complexity_tier')
    await expect(general).toContainText('Not routed yet')

    const support = page.getByTestId('model-router-row-support')
    await expect(support).toContainText('Decision model')
    await expect(support).toContainText('1 rule')
    await expect(support).toContainText('41,960')
    await expect(page.getByTestId('model-router-status-support')).toHaveAttribute('data-state-value', 'live')

    const billing = page.getByTestId('model-router-row-billing')
    await expect(billing).toContainText('LLM classifier')
    await expect(billing).toContainText('Not routed yet')
    await expect(page.getByTestId('model-router-status-billing')).toHaveAttribute('data-state-value', 'draft')
  })

  test('should show a decision router whose provider has no enabled key as failed', async ({ page }) => {
    await page.route(
      (url) => url.pathname === '/api/keys',
      (route) => route.fulfill({ json: [] }),
    )
    await page.goto('/workspace/model-routers')
    await expect(page.getByTestId('model-router-status-support')).toHaveAttribute('data-state-value', 'failed')
  })

  test('should open a router on its overview, with what it still needs', async ({ page }) => {
    await page.goto('/workspace/model-routers')
    await page.getByTestId('model-router-row-billing').click()
    await expect(page).toHaveURL(/router=billing/)
    await expect(page.getByTestId('model-router-tab-overview')).toHaveAttribute('data-state', 'active')

    await expect(page.getByTestId('model-router-setup-classifier')).toHaveAttribute('data-done', 'true')
    await expect(page.getByTestId('model-router-setup-models')).toHaveAttribute('data-done', 'false')
    await expect(page.getByTestId('model-router-setup-routing')).toHaveAttribute('data-done', 'false')
    await expect(page.getByTestId('model-router-fact-models')).toContainText('0 of 3 set')

    await page.getByTestId('model-router-setup-models-action').click()
    await expect(page.getByTestId('model-router-tab-models')).toHaveAttribute('data-state', 'active')
  })

  test('should create a router in the wizard and open it', async ({ page }) => {
    await page.goto('/workspace/model-routers')
    await page.getByTestId('model-router-create-button').click()
    await expect(page.getByTestId('model-router-wizard')).toBeVisible()

    // A name is required before the next step.
    await page.getByTestId('model-router-wizard-next').click()
    await expect(page.getByTestId('model-router-wizard-step-classifier')).toBeDisabled()
    await page.getByTestId('model-router-name-input').fill('Engineering assistant')
    await page.getByTestId('model-router-description-input').fill('Coding agents and IDE traffic.')
    await page.getByTestId('model-router-wizard-next').click()

    await page.getByTestId('complexity-router-classifier-option-semantic').click()
    await page.getByTestId('model-router-wizard-next').click()
    // Semantic needs an embedding model, so its sheet opens first; the router
    // can still be created with its phrases and configured later.
    await expect(page.getByTestId('complexity-router-embedding-sheet')).toBeVisible()
    await page.getByTestId('complexity-router-embedding-sheet-close-button').click()
    await expect(page.getByTestId('model-router-starter-phrases-notice')).toBeVisible()

    await page.getByTestId('model-router-wizard-create').click()
    await expect.poll(() => createdBodies.length).toBe(1)
    expect(createdBodies[0]).toMatchObject({
      name: 'Engineering assistant',
      description: 'Coding agents and IDE traffic.',
      classifier: 'semantic',
      keywords: KEYWORDS,
    })
    await expect(page).toHaveURL(/router=engineering/)
  })

  test('should save the model each tier gets on the Models tab', async ({ page }) => {
    await page.goto('/workspace/model-routers?router=billing&tab=models')
    await expect(page.getByTestId('model-router-tier-simple')).toContainText("Keeps the request's model")
    await expect(page.getByTestId('model-router-models-save-button')).toBeDisabled()

    await page.getByTestId('model-router-tier-simple-add-button').click()
    await page.getByTestId('model-router-models-save-button').click()
    // A target needs a provider or a model.
    await expect(page.getByTestId('model-router-tier-simple-error')).toBeVisible()
    expect(targetBodies).toHaveLength(0)
  })

  test('should list the rules that send requests to a router and add one on it', async ({ page }) => {
    await page.goto('/workspace/model-routers?router=support&tab=routing')
    const row = page.getByTestId('model-routing-rule-row-desk')
    await expect(row).toContainText('Support desk')
    await expect(row).toContainText('support-assistant')

    await page.getByTestId('model-routing-rule-create-button').click()
    await expect(page.getByTestId('model-routing-rule-sheet')).toBeVisible()
    await expect(page.getByTestId('model-routing-rule-router-select')).toBeDisabled()
    await expect(page.getByTestId('model-routing-rule-router-select')).toContainText('Support')
  })

  test('should classify labelled requests on the Test tab', async ({ page }) => {
    let classifyBody: Record<string, unknown> | undefined
    await page.route(
      (url) => url.pathname === '/api/routing/model-routers/support/classify',
      (route) => {
        classifyBody = route.request().postDataJSON() as Record<string, unknown>
        return route.fulfill({
          json: {
            router_id: 'support',
            router_name: 'Support',
            classifier: 'decision',
            results: [
              { index: 0, id: '1', text: 'hi', expected_tier: 'SIMPLE', correct: true, tier: 'SIMPLE', mechanism: 'decision', confidence: 0.92 },
              { index: 1, id: '2', text: 'design a cache', expected_tier: 'COMPLEX', correct: false, tier: 'MEDIUM', mechanism: 'decision' },
            ],
            summary: { total: 2, tiers: { SIMPLE: 1, MEDIUM: 1, COMPLEX: 0, UNCLASSIFIED: 0 }, labeled: 2, correct: 1, accuracy: 0.5 },
          },
        })
      },
    )
    await page.goto('/workspace/model-routers?router=support&tab=test')
    await page.getByTestId('model-router-test-input').fill('SIMPLE: hi\nCOMPLEX: design a cache')
    await page.getByTestId('model-router-test-run').click()

    await expect(page.getByTestId('model-router-test-accuracy')).toContainText('1 of 2')
    await expect(page.getByTestId('model-router-test-result-0')).toContainText('openai/gpt-4o-mini')
    await expect(page.getByTestId('model-router-test-result-1')).toContainText('expected')
    expect(classifyBody).toEqual({
      inputs: [
        { id: '1', text: 'hi', expected_tier: 'SIMPLE' },
        { id: '2', text: 'design a cache', expected_tier: 'COMPLEX' },
      ],
    })
  })

  test('should count a router as tested only once a Test run succeeds', async ({ page }) => {
    let fail = true
    await page.route(
      (url) => url.pathname === '/api/routing/model-routers/billing/classify',
      (route) =>
        fail
          ? route.fulfill({ status: 503, json: { is_bifrost_error: false, error: { message: 'the decision model is unavailable' } } })
          : route.fulfill({
              json: {
                router_id: 'billing',
                router_name: 'Billing',
                classifier: 'semantic',
                results: [{ index: 0, id: '1', text: 'hi', tier: 'SIMPLE', mechanism: 'semantic', score: 0.93 }],
                summary: { total: 1, tiers: { SIMPLE: 1, MEDIUM: 0, COMPLEX: 0, UNCLASSIFIED: 0 }, labeled: 0, correct: 0 },
              },
            }),
    )
    await page.goto('/workspace/model-routers?router=billing&tab=test')
    await page.getByTestId('model-router-test-input').fill('hi')
    await page.getByTestId('model-router-test-run').click()
    await expect(page.getByTestId('model-router-test-error')).toContainText(/the decision model is unavailable/i)
    await page.getByTestId('model-router-tab-overview').click()
    await expect(page.getByTestId('model-router-setup-test')).toHaveAttribute('data-done', 'false')

    fail = false
    await page.getByTestId('model-router-tab-test').click()
    await page.getByTestId('model-router-test-input').fill('hi')
    await page.getByTestId('model-router-test-run').click()
    await expect(page.getByTestId('model-router-test-summary')).toBeVisible()
    await page.getByTestId('model-router-tab-overview').click()
    await expect(page.getByTestId('model-router-setup-test')).toHaveAttribute('data-done', 'true')
  })

  test('should show why a router that rules use cannot be deleted', async ({ page }) => {
    await page.goto('/workspace/model-routers?router=support')
    await page.getByTestId('model-router-actions-btn').click()
    await page.getByTestId('model-router-delete-btn').click()
    await page.getByRole('alertdialog').getByRole('button', { name: 'Delete' }).click()
    await assertToast(page, 'Support desk', 'error')
    expect(deleteAttempts).toBe(1)
  })

  test('should warn that deleting the router that answers complexity_tier leaves the built-in defaults', async ({ page }) => {
    await page.goto('/workspace/model-routers?router=general')
    await page.getByTestId('model-router-actions-btn').click()
    await page.getByTestId('model-router-delete-btn').click()
    await expect(page.getByTestId('model-router-delete-complexity-tier-warning')).toContainText('built-in defaults')
  })

  test('should not save a rule until it names a router', async ({ page }) => {
    await page.goto('/workspace/model-routers?view=rules')
    await page.getByTestId('model-routing-rule-create-button').click()
    await expect(page.getByTestId('model-routing-rule-sheet')).toBeVisible()
    await page.getByTestId('model-routing-rule-name-input').fill('Unrouted')
    await expect(page.getByTestId('model-routing-rule-router-select')).toContainText('Select a router')
    await page.getByTestId('model-routing-rule-save-button').click()
    await expect(page.getByTestId('model-routing-rule-sheet')).toContainText('Select a router for the rule.')
  })

  test('should list every model routing rule with its router in the model routers section', async ({ page }) => {
    await page.goto('/workspace/model-routers?view=rules')
    const row = page.getByTestId('model-routing-rule-row-desk')
    await expect(row).toContainText('Support desk')
    await expect(row).toContainText('Support')
  })
})
