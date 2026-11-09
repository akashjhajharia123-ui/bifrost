import PageTitle from "@/components/pageTitle";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { getLocale } from "@/lib/i18n";
import { getErrorMessage, useGetProvidersQuery } from "@/lib/store";
import { useGetAllKeysQuery } from "@/lib/store/apis/providersApi";
import { useGetModelRoutersQuery, useGetModelRouterStatusQuery, useGetModelRoutingRulesQuery } from "@/lib/store/apis/governanceApi";
import { ModelRouter, ModelRoutingRule } from "@/lib/types/complexityRouter";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Plus } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { DecisionProviderState, getDecisionProviderState, isRouterConfigured } from "../formSchema";
import { RouterStatusChip } from "./classifierStatusBadge";
import { classifierSummary } from "./detail/overviewTab";
import { ModelRoutingRulesView } from "./modelRoutingRulesView";
import { routerStatus } from "./routerStatus";
import { useRouterRequestCounts } from "./useRouterRequestCounts";

// RouterStatusCell is a router's status. A semantic router's classifier is
// polled while it warms, so the row moves to Live without a reload.
function RouterStatusCell({
	router,
	rules,
	decisionProvider,
}: {
	router: ModelRouter;
	rules: ModelRoutingRule[] | undefined;
	// A decision router's provider state; absent while it loads.
	decisionProvider?: DecisionProviderState;
}) {
	const semantic = (router.classifier ?? "semantic") === "semantic";
	const [pollingInterval, setPollingInterval] = useState(0);
	const { data: status } = useGetModelRouterStatusQuery(router.id, { skip: !semantic || !isRouterConfigured(router), pollingInterval });
	useEffect(() => setPollingInterval(status?.state === "warming" ? 2000 : 0), [status?.state]);
	const isDecision = router.classifier === "decision";
	return (
		<RouterStatusChip
			status={routerStatus(router, rules, status, isDecision && decisionProvider ? decisionProvider === "configured" : undefined)}
			testId={`model-router-status-${router.id}`}
			decision={isDecision && decisionProvider ? { provider: router.decision?.provider ?? "", state: decisionProvider } : undefined}
		/>
	);
}

export const MODEL_ROUTERS_VIEWS = ["routers", "rules"] as const;
export type ModelRoutersListView = (typeof MODEL_ROUTERS_VIEWS)[number];

interface ModelRoutersViewProps {
	view: ModelRoutersListView;
	onViewChange: (view: ModelRoutersListView) => void;
	onOpenRouter: (id: string) => void;
	onCreate: () => void;
}

// ModelRoutersView is the model routers section: the routers, with what each
// classifies with, what sends it requests, how many it took over the last
// week and whether it is live; and every model routing rule, across routers,
// in the order they are tried.
export function ModelRoutersView({ view, onViewChange, onOpenRouter, onCreate }: ModelRoutersViewProps) {
	const { t } = useTranslation("models", { keyPrefix: "routing.modelRouters" });
	return (
		<div className="flex flex-col gap-4" data-testid="model-routers-view">
			<PageTitle title={t("title")} beta>
				{t("pageDescription")}
			</PageTitle>

			<Tabs value={view} onValueChange={(next) => onViewChange(next as ModelRoutersListView)}>
				<TabsList className="rounded-sm [&_[data-state=active]]:shadow-none">
					<TabsTrigger value="routers" data-testid="model-routers-view-routers">
						{t("listTitle")}
					</TabsTrigger>
					<TabsTrigger value="rules" data-testid="model-routers-view-rules">
						{t("viewRules")}
					</TabsTrigger>
				</TabsList>
			</Tabs>

			{view === "rules" ? <ModelRoutingRulesView /> : <RoutersTable onOpenRouter={onOpenRouter} onCreate={onCreate} />}
		</div>
	);
}

// RoutersTable is the list of routers, with the button that creates one.
function RoutersTable({ onOpenRouter, onCreate }: Pick<ModelRoutersViewProps, "onOpenRouter" | "onCreate">) {
	const { t } = useTranslation("models", { keyPrefix: "routing.modelRouters" });
	const { t: tcpx } = useTranslation("models", { keyPrefix: "routing.complexityUi" });
	const canCreate = useRbac(RbacResource.Governance, RbacOperation.Create);
	const { data, isLoading, error } = useGetModelRoutersQuery();
	const { data: rulesData } = useGetModelRoutingRulesQuery();
	const requestCounts = useRouterRequestCounts();
	const routers = data?.routers ?? [];
	// Decision routers are judged by their provider, as the router page's alert does.
	const hasDecisionRouter = routers.some((router) => router.classifier === "decision");
	const { data: providers, isLoading: providersLoading } = useGetProvidersQuery(undefined, { skip: !hasDecisionRouter });
	const { data: keys, isLoading: keysLoading } = useGetAllKeysQuery(undefined, { skip: !hasDecisionRouter });
	const decisionProviderState = (router: ModelRouter) =>
		providersLoading || keysLoading ? undefined : getDecisionProviderState(providers, keys, router.decision?.provider ?? "");

	return (
		<>
			<div className="flex flex-wrap items-start justify-between gap-3">
				<p className="text-muted-foreground max-w-3xl min-w-0 text-sm">{t("listDescription")}</p>
				{canCreate && (
					<Button type="button" className="h-9" onClick={onCreate} data-testid="model-router-create-button">
						<Plus className="size-4" />
						{t("newRouter")}
					</Button>
				)}
			</div>

			<div className="overflow-hidden rounded-sm border">
				<Table containerClassName="h-full overflow-auto">
					<TableHeader className="bg-muted sticky top-0 z-10">
						<TableRow className="bg-muted/50">
							<TableHead className="font-semibold">{t("columnName")}</TableHead>
							<TableHead className="font-semibold">{t("columnClassifier")}</TableHead>
							<TableHead className="font-semibold">{t("columnRouting")}</TableHead>
							<TableHead className="text-right font-semibold">{t("columnRequests")}</TableHead>
							<TableHead className="font-semibold">{t("columnStatus")}</TableHead>
						</TableRow>
					</TableHeader>
					<TableBody>
						{isLoading ? (
							<TableRow>
								<TableCell colSpan={5} className="h-10">
									<div className="bg-muted h-2 w-32 animate-pulse rounded" />
								</TableCell>
							</TableRow>
						) : error ? (
							<TableRow>
								<TableCell colSpan={5} className="text-destructive h-24 text-center text-sm">
									{t("loadFailed", { error: getErrorMessage(error) })}
								</TableCell>
							</TableRow>
						) : (
							routers.map((router) => {
								const classifier = classifierSummary(router, tcpx);
								const ruleCount = (rulesData?.rules ?? []).filter((rule) => rule.model_router_id === router.id).length;
								const requests = requestCounts?.get(router.id);
								return (
									<TableRow
										key={router.id}
										className="hover:bg-muted/50 cursor-pointer transition-colors"
										onClick={() => onOpenRouter(router.id)}
										data-testid={`model-router-row-${router.id}`}
									>
										<TableCell className="font-medium">
											<div className="flex flex-col gap-1">
												<span className="flex items-center gap-2">
													<span className="max-w-xs truncate">{router.name}</span>
												</span>
												{router.description && (
													<span className="text-muted-foreground max-w-md truncate text-xs">{router.description}</span>
												)}
											</div>
										</TableCell>
										<TableCell>
											<div className="flex flex-col gap-0.5">
												<span>{classifier.name}</span>
												<span className="text-muted-foreground text-xs">{classifier.model || t("classifierNotSet")}</span>
											</div>
										</TableCell>
										<TableCell>
											<span className={ruleCount === 0 ? "text-muted-foreground" : undefined}>
												{ruleCount > 0 ? t("rulesCount", { count: ruleCount }) : t("notRouted")}
											</span>
										</TableCell>
										<TableCell className="text-right font-mono tabular-nums">
											{requests ? requests.toLocaleString(getLocale()) : <span className="text-muted-foreground">-</span>}
										</TableCell>
										<TableCell>
											<RouterStatusCell router={router} rules={rulesData?.rules} decisionProvider={decisionProviderState(router)} />
										</TableCell>
									</TableRow>
								);
							})
						)}
					</TableBody>
				</Table>
			</div>
		</>
	);
}