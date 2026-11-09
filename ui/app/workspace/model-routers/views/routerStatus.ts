import { COMPLEXITY_TIER_VALUES, ModelRouter, ModelRoutingRule, SemanticStatusInfo } from "@/lib/types/complexityRouter";
import { isRouterConfigured } from "../formSchema";

// What a router is doing, at a glance. Live serves traffic; Draft does not yet,
// and says why through the setup checklist. Failed and warming only matter
// while the semantic classifier is broken or still loading, and override the rest.
export type RouterStatus = "live" | "draft" | "warming" | "failed";

// The tiers a router has no model for. A tier without one keeps the request's
// model, which is rarely what a router is for, so all three are asked for.
export function missingTiers(router: Pick<ModelRouter, "targets">): string[] {
	const covered = new Set((router.targets ?? []).map((target) => target.tier));
	return COMPLEXITY_TIER_VALUES.filter((tier) => !covered.has(tier));
}

// The enabled model routing rules that send requests to a router.
export function routingRulesFor(routerId: string, rules: ModelRoutingRule[] | undefined): ModelRoutingRule[] {
	return (rules ?? []).filter((rule) => rule.model_router_id === routerId && rule.enabled);
}

// routerStatus decides a router's status: live once it can classify, has a
// model for every tier and has a model routing rule sending it requests.
// A decision router whose provider cannot serve it has failed, as a semantic
// one whose warmup failed has; decisionProviderReady is undefined while that
// is not known yet.
export function routerStatus(
	router: ModelRouter,
	rules: ModelRoutingRule[] | undefined,
	semantic?: SemanticStatusInfo,
	decisionProviderReady?: boolean,
): RouterStatus {
	const classifier = router.classifier ?? "semantic";
	if (classifier === "semantic" && semantic?.state === "failed") return "failed";
	if (classifier === "decision" && decisionProviderReady === false) return "failed";
	if (classifier === "semantic" && semantic?.state === "warming") return "warming";
	if (!isRouterConfigured(router)) return "draft";
	if (missingTiers(router).length > 0) return "draft";
	return routingRulesFor(router.id, rules).length > 0 ? "live" : "draft";
}