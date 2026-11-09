import { describe, expect, test } from "vitest";
import type { ModelRouter, ModelRoutingRule } from "@/lib/types/complexityRouter";
import { missingTiers, routerStatus, routingRulesFor } from "./routerStatus";

const keywords = { simple_keywords: ["a"], medium_keywords: ["b"], complex_keywords: ["c"] };
const everyTier = (["SIMPLE", "MEDIUM", "COMPLEX"] as const).map((tier) => ({ tier, provider: "openai", model: "gpt-4o-mini", weight: 1 }));

const router = (overrides: Partial<ModelRouter> = {}): ModelRouter => ({
	id: "support",
	name: "Support",
	keywords,
	classifier: "decision",
	targets: everyTier,
	...overrides,
});

const rule = (overrides: Partial<ModelRoutingRule> = {}): ModelRoutingRule => ({
	id: "rule-1",
	name: "Support desk",
	description: "",
	enabled: true,
	cel_expression: "",
	model_router_id: "support",
	scope: "global",
	priority: 0,
	created_at: "",
	updated_at: "",
	...overrides,
});

describe("router status", () => {
	test("is live once it classifies, has a model for every tier and an enabled rule sends it requests", () => {
		expect(routerStatus(router(), [rule()])).toBe("live");
	});

	test("is a draft while any of those is missing", () => {
		expect(routerStatus(router(), []), "no rule").toBe("draft");
		expect(routerStatus(router(), [rule({ enabled: false })]), "only a disabled rule").toBe("draft");
		expect(routerStatus(router(), [rule({ model_router_id: "billing" })]), "a rule on another router").toBe("draft");
		expect(routerStatus(router({ targets: everyTier.slice(0, 2) }), [rule()]), "a tier without a model").toBe("draft");
		expect(routerStatus(router({ classifier: "llm" }), [rule()]), "an llm classifier without its model").toBe("draft");
		expect(routerStatus(router({ classifier: "semantic" }), [rule()]), "semantic without an embedding model").toBe("draft");
	});

	test("reports a broken or loading semantic classifier over the rest", () => {
		const semantic = router({ classifier: "semantic", semantic: { provider: "openai", embedding_model: "text-embedding-3-small" } });
		const status = (state: "failed" | "warming" | "ready") => ({ state, loaded: 0, total: 0 });
		expect(routerStatus(semantic, [rule()], status("failed"))).toBe("failed");
		expect(routerStatus(semantic, [rule()], status("warming"))).toBe("warming");
		expect(routerStatus(semantic, [rule()], status("ready"))).toBe("live");
	});

	test("reports a decision router whose provider cannot serve it as failed", () => {
		expect(routerStatus(router(), [rule()], undefined, false)).toBe("failed");
		expect(routerStatus(router(), [rule()], undefined, true)).toBe("live");
		expect(routerStatus(router(), [rule()], undefined, undefined), "not known yet").toBe("live");
	});

	test("judges every router alike, whatever its id or complexity_tier flag", () => {
		expect(routerStatus(router({ id: "default", targets: [] }), [])).toBe("draft");
		expect(routerStatus(router({ answers_complexity_tier: true, targets: [] }), [])).toBe("draft");
	});

	test("lists the tiers without a model and the enabled rules on a router", () => {
		expect(missingTiers(router({ targets: everyTier.slice(1) }))).toEqual(["SIMPLE"]);
		expect(missingTiers(router({ targets: undefined }))).toEqual(["SIMPLE", "MEDIUM", "COMPLEX"]);
		expect(
			routingRulesFor("support", [rule(), rule({ id: "off", enabled: false }), rule({ id: "other", model_router_id: "billing" })]).map(
				(r) => r.id,
			),
		).toEqual(["rule-1"]);
	});
});