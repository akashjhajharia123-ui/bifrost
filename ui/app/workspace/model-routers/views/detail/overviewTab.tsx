import { Fact, FactStrip } from "@/components/factStrip";
import { SetupChecklist, type SetupChecklistItem } from "@/components/setupChecklist";
import { getLocale } from "@/lib/i18n";
import { ModelRouter, ModelRoutingRule, TIER_PHRASE_LIST_DEFINITIONS } from "@/lib/types/complexityRouter";
import { useTranslation } from "react-i18next";
import { isRouterConfigured } from "../../formSchema";
import { missingTiers } from "../routerStatus";
import type { RouterTab } from "./routerDetailView";

// The classifier's name, and the model behind it.
export function classifierSummary(
	router: ModelRouter,
	t: (key: "classifierNameSemantic" | "classifierNameDecision" | "classifierNameLlm") => string,
) {
	switch (router.classifier) {
		case "decision":
			return { name: t("classifierNameDecision"), model: router.decision?.model };
		case "llm":
			return { name: t("classifierNameLlm"), model: router.llm?.model };
		default:
			return { name: t("classifierNameSemantic"), model: router.semantic?.embedding_model };
	}
}

interface OverviewTabProps {
	router: ModelRouter;
	// The enabled model routing rules that send requests to this router.
	rules: ModelRoutingRule[];
	requests7d: number | undefined;
	// Whether a test ran on this visit.
	tested: boolean;
	onNavigate: (tab: RouterTab) => void;
}

// OverviewTab says what the router still needs before it serves traffic, and
// how it is set up, one fact per tab.
export function OverviewTab({ router, rules, requests7d, tested, onNavigate }: OverviewTabProps) {
	const { t } = useTranslation("models", { keyPrefix: "routing.modelRouters.overview" });
	const { t: tcpx } = useTranslation("models", { keyPrefix: "routing.complexityUi" });
	const classifier = classifierSummary(router, tcpx);
	const configured = isRouterConfigured(router);
	const missing = missingTiers(router);
	const modelsSet = 3 - missing.length;
	const tierNames = TIER_PHRASE_LIST_DEFINITIONS.map((definition) => definition.label).join(" · ");

	const steps: SetupChecklistItem[] = [
		{
			key: "classifier",
			title: t("setup.classifier"),
			detail: configured ? `${classifier.name} · ${tierNames}` : t("setup.classifierPending"),
			done: configured,
			actionLabel: t("setup.edit"),
			onAction: () => onNavigate("configuration"),
		},
		{
			key: "models",
			title: t("setup.models"),
			detail: missing.length === 0 ? t("setup.modelsDone") : t("setup.modelsPending"),
			done: missing.length === 0,
			actionLabel: t("setup.pickModels"),
			onAction: () => onNavigate("models"),
		},
		{
			key: "routing",
			title: t("setup.routing"),
			detail: rules.length > 0 ? t("setup.routingDone", { count: rules.length }) : t("setup.routingPending"),
			done: rules.length > 0,
			actionLabel: t("setup.addRule"),
			onAction: () => onNavigate("routing"),
		},
		{
			key: "test",
			title: t("setup.test"),
			detail: t("setup.testDetail"),
			done: tested,
			actionLabel: t("setup.testAction"),
			onAction: () => onNavigate("test"),
		},
	];
	const done = steps.filter((step) => step.done).length;
	// Testing is optional, so the card goes once everything else is done.
	const setupComplete = steps.every((step) => step.done || step.key === "test");

	return (
		<div className="flex min-h-0 grow flex-col gap-4 overflow-y-auto" data-testid="model-router-overview">
			{!setupComplete && (
				<SetupChecklist
					title={t("setup.title")}
					summary={t("setup.summary", { done, total: steps.length })}
					items={steps}
					testId="model-router-setup"
				/>
			)}

			<FactStrip testId="model-router-facts">
				<Fact
					label={t("facts.classifier")}
					value={classifier.name}
					sub={classifier.model || t("facts.notConfigured")}
					dim={!configured}
					onClick={() => onNavigate("configuration")}
					testId="model-router-fact-classifier"
				/>
				<Fact
					label={t("facts.tiers")}
					value={tierNames}
					sub={router.session?.enabled ? t("facts.sessionOn") : t("facts.sessionOff")}
					onClick={() => onNavigate("configuration")}
					testId="model-router-fact-tiers"
				/>
				<Fact
					label={t("facts.models")}
					value={t("facts.modelsSet", { count: modelsSet })}
					sub={
						modelsSet === 0
							? t("facts.pickModels")
							: Array.from(new Set((router.targets ?? []).map((target) => target.model || target.provider))).join(", ")
					}
					dim={modelsSet === 0}
					onClick={() => onNavigate("models")}
					testId="model-router-fact-models"
				/>
				<Fact
					label={t("facts.routing")}
					value={rules.length > 0 ? t("facts.rules", { count: rules.length }) : t("facts.notRouted")}
					sub={rules.length > 0 ? rules.map((rule) => rule.name).join(", ") : t("facts.addRule")}
					dim={rules.length === 0}
					onClick={() => onNavigate("routing")}
					testId="model-router-fact-routing"
				/>
				<Fact
					label={t("facts.requests")}
					value={requests7d === undefined ? "-" : requests7d.toLocaleString(getLocale())}
					sub={t("facts.last7Days")}
					dim={!requests7d}
					testId="model-router-fact-requests"
				/>
			</FactStrip>
		</div>
	);
}