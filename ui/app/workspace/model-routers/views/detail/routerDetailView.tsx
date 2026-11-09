import { DeleteRuleDialog } from "../ruleShared";
import FullPageLoader from "@/components/fullPageLoader";
import PageTitle from "@/components/pageTitle";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { getErrorMessage } from "@/lib/store";
import {
	useDeleteModelRouterMutation,
	useGetModelRouterQuery,
	useGetModelRoutingRulesQuery,
	useUpdateModelRouterMutation,
} from "@/lib/store/apis/governanceApi";
import { MAX_SEMANTIC_PHRASES } from "@/lib/types/complexityRouter";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Link } from "@tanstack/react-router";
import { ArrowLeft, MoreHorizontal, Pencil, ScrollText, Trash2 } from "lucide-react";
import { parseAsStringLiteral, useQueryState } from "nuqs";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { RouterStatusChip } from "../classifierStatusBadge";
import { RouterConfigurationSheets } from "../routerConfigurationEditor";
import RouterDetailsDialog from "../routerDetailsDialog";
import { getDecisionProviderState } from "../../formSchema";
import { missingTiers, routerStatus, routingRulesFor } from "../routerStatus";
import SuggestionsTab from "../suggestionsTab";
import { useRouterConfigForm } from "../useRouterConfigForm";
import { useRouterRequestCounts } from "../useRouterRequestCounts";
import { ConfigurationTab } from "./configurationTab";
import { ModelsTab } from "./modelsTab";
import { OverviewTab } from "./overviewTab";
import { RoutingTab } from "./routingTab";

export const ROUTER_TABS = ["overview", "configuration", "models", "routing", "suggestions"] as const;
export type RouterTab = (typeof ROUTER_TABS)[number];

// A count beside a tab's name, as the project page shows them.
function TabCount({ children }: { children: React.ReactNode }) {
	return <span className="text-muted-foreground ml-1 font-mono text-xs">{children}</span>;
}

interface RouterDetailViewProps {
	routerId: string;
	onBack: () => void;
}

// RouterDetailView is one model router's page, laid out like a project's:
// the router's name in the topbar, its tabs, and its actions beside them.
export default function RouterDetailView({ routerId, onBack }: RouterDetailViewProps) {
	const { t } = useTranslation("models");
	const { t: tc } = useTranslation("common");
	const { t: tmr } = useTranslation("models", { keyPrefix: "routing.modelRouters" });
	const canUpdate = useRbac(RbacResource.Governance, RbacOperation.Update);
	const canDelete = useRbac(RbacResource.Governance, RbacOperation.Delete);
	const [tab, setTab] = useQueryState("tab", parseAsStringLiteral(ROUTER_TABS).withDefault("overview").withOptions({ history: "push" }));
	const { data, isLoading, error, refetch } = useGetModelRouterQuery(routerId, { refetchOnMountOrArgChange: true });
	const { data: rulesData } = useGetModelRoutingRulesQuery();
	const [updateRouter] = useUpdateModelRouterMutation();
	const [deleteRouter, { isLoading: isDeleting }] = useDeleteModelRouterMutation();
	const requestCounts = useRouterRequestCounts();
	const [detailsOpen, setDetailsOpen] = useState(false);
	const [deleteOpen, setDeleteOpen] = useState(false);

	const config = useRouterConfigForm({
		saved: data,
		routerId,
		canUpdate,
		// The router's identity rides along unchanged; it is edited on its own.
		save: (payload) => updateRouter({ ...payload, id: routerId, name: data?.name ?? "", description: data?.description }).unwrap(),
	});

	if (isLoading && !data) {
		return (
			<>
				<PageTitle breadcrumbs={[{ label: tmr("title"), to: "/workspace/model-routers" }, { label: routerId }]} beta />
				<FullPageLoader />
			</>
		);
	}

	if (!data) {
		return (
			<div className="mx-auto w-full max-w-7xl space-y-4 px-4 pt-6" data-testid="model-router-missing">
				<PageTitle breadcrumbs={[{ label: tmr("title"), to: "/workspace/model-routers" }, { label: routerId }]} beta />
				<p className="text-sm font-medium">{t("routing.complexityLoadFailed")}</p>
				{error ? <p className="text-muted-foreground text-sm">{getErrorMessage(error)}</p> : null}
				<div className="flex gap-2">
					<Button data-testid="complexity-router-fetch-retry-button" type="button" variant="outline" size="sm" onClick={() => refetch()}>
						{tc("retry")}
					</Button>
					<Button data-testid="model-router-fetch-back-button" type="button" variant="ghost" size="sm" onClick={onBack}>
						<ArrowLeft className="size-4" />
						{tmr("title")}
					</Button>
				</div>
			</div>
		);
	}

	const rules = routingRulesFor(routerId, rulesData?.rules);
	const allRules = (rulesData?.rules ?? []).filter((rule) => rule.model_router_id === routerId);
	const isDecision = data.classifier === "decision";
	const savedDecisionProvider =
		isDecision && !config.providers.loading
			? getDecisionProviderState(config.providers.all, config.providers.keys, data.decision?.provider ?? "")
			: undefined;
	const status = routerStatus(
		data,
		rulesData?.rules,
		config.status.semantic,
		savedDecisionProvider ? savedDecisionProvider === "configured" : undefined,
	);
	const modelsSet = 3 - missingTiers(data).length;
	const showSuggestions = config.live.classifier === "semantic";
	// Suggestions only serve the semantic classifier, so a link to them on any
	// other router lands on its overview.
	const currentTab: RouterTab = tab === "suggestions" && !showSuggestions ? "overview" : tab;

	const confirmDelete = () => {
		deleteRouter(routerId)
			.unwrap()
			.then(() => {
				toast.success(tmr("deleted"), { position: "top-right" });
				setDeleteOpen(false);
				onBack();
			})
			.catch((err) => {
				toast.error(getErrorMessage(err));
				setDeleteOpen(false);
			});
	};

	return (
		<div className="no-padding-parent flex h-full min-h-0 w-full grow flex-col gap-3 overflow-hidden p-4" data-testid="model-router-detail">
			{/* The topbar carries the router's name; the page itself starts at the tabs. */}
			<PageTitle breadcrumbs={[{ label: tmr("title"), to: "/workspace/model-routers" }, { label: data.name }]} beta>
				{data.description || tmr("noDescription")}
			</PageTitle>

			<Tabs value={currentTab} onValueChange={(value) => void setTab(value as RouterTab)} className="min-h-0 grow gap-3">
				<div className="flex flex-wrap items-center gap-2" data-testid="model-router-header">
					<div className="custom-scrollbar min-w-0 flex-1 overflow-x-auto">
						<TabsList className="w-max min-w-max rounded-sm [&_[data-state=active]]:shadow-none">
							<TabsTrigger value="overview" data-testid="model-router-tab-overview">
								{tmr("tabs.overview")}
							</TabsTrigger>
							<TabsTrigger value="configuration" data-testid="model-router-tab-configuration">
								{tmr("tabs.configuration")}
								{config.derived.isDirty && (
									<span className="ml-1.5 size-1.5 rounded-full bg-amber-500" role="status" aria-label={tmr("tabs.unsaved")} />
								)}
							</TabsTrigger>
							<TabsTrigger value="models" data-testid="model-router-tab-models">
								{tmr("tabs.models")}
								<TabCount>{modelsSet}/3</TabCount>
							</TabsTrigger>
							<TabsTrigger value="routing" data-testid="model-router-tab-routing">
								{tmr("tabs.routing")}
								<TabCount>{allRules.length}</TabCount>
							</TabsTrigger>
							{showSuggestions && (
								<TabsTrigger value="suggestions" data-testid="complexity-router-tab-suggestions">
									{tmr("tabs.suggestions")}
									{config.staging.pendingSuggestions > 0 && (
										<span data-testid="complexity-router-tab-suggestions-count">
											<TabCount>{config.staging.pendingSuggestions}</TabCount>
										</span>
									)}
								</TabsTrigger>
							)}
						</TabsList>
					</div>
					<div className="ml-auto flex items-center gap-2">
						<RouterStatusChip
							status={status}
							testId={`model-router-status-${routerId}`}
							decision={savedDecisionProvider ? { provider: data.decision?.provider ?? "", state: savedDecisionProvider } : undefined}
						/>
						<DropdownMenu>
							<DropdownMenuTrigger asChild>
								<Button
									variant="outline"
									size="icon"
									className="mr-0.5 h-9 w-9"
									aria-label={tmr("actionsAria", { name: data.name })}
									data-testid="model-router-actions-btn"
								>
									<MoreHorizontal className="h-4 w-4" />
								</Button>
							</DropdownMenuTrigger>
							<DropdownMenuContent align="end" className="shadow-none">
								<DropdownMenuItem asChild data-testid="model-router-view-logs-btn" className="cursor-pointer">
									<Link to="/workspace/logs" search={{ model_router_ids: [routerId] }}>
										<ScrollText className="h-4 w-4" />
										{tmr("viewLogs")}
									</Link>
								</DropdownMenuItem>
								{canUpdate && (
									<DropdownMenuItem
										onClick={() => setDetailsOpen(true)}
										data-testid="model-router-edit-details-button"
										className="cursor-pointer"
									>
										<Pencil className="h-4 w-4" />
										{tmr("editDetails")}
									</DropdownMenuItem>
								)}
								{canDelete && (
									<DropdownMenuItem
										variant="destructive"
										onClick={() => setDeleteOpen(true)}
										data-testid="model-router-delete-btn"
										className="cursor-pointer"
									>
										<Trash2 className="h-4 w-4" />
										{tc("delete")}
									</DropdownMenuItem>
								)}
							</DropdownMenuContent>
						</DropdownMenu>
					</div>
				</div>

				<TabsContent value="overview" className="flex min-h-0 flex-col overflow-hidden">
					<OverviewTab router={data} rules={rules} requests7d={requestCounts?.get(routerId)} onNavigate={(next) => void setTab(next)} />
				</TabsContent>
				<TabsContent value="configuration" className="flex min-h-0 flex-col overflow-hidden">
					<ConfigurationTab config={config} />
				</TabsContent>
				<TabsContent value="models" className="flex min-h-0 flex-col overflow-hidden">
					<ModelsTab router={data} canUpdate={canUpdate} providers={config.providers.all} keys={config.providers.keys} />
				</TabsContent>
				<TabsContent value="routing" className="flex min-h-0 flex-col overflow-hidden">
					<RoutingTab routerId={routerId} />
				</TabsContent>
				{showSuggestions && (
					<TabsContent value="suggestions" className="custom-scrollbar flex min-h-0 flex-col overflow-y-auto">
						<SuggestionsTab
							routerId={routerId}
							canUpdate={canUpdate}
							enabled={Boolean(data.suggestions?.enabled)}
							lookbackDays={data.suggestions?.lookback_days ?? config.live.suggestions.lookback_days}
							staged={config.staging.active}
							remainingPhrases={Math.max(0, MAX_SEMANTIC_PHRASES - config.derived.totalPhrases)}
							onStage={config.staging.stage}
							onUnstage={config.staging.unstage}
							onEditStaged={config.staging.edit}
							onOpenSettings={() => config.sheets.setOpen("suggestions")}
						/>
					</TabsContent>
				)}
			</Tabs>

			<RouterConfigurationSheets config={config} />
			<RouterDetailsDialog open={detailsOpen} onOpenChange={setDetailsOpen} router={data} />
			<DeleteRuleDialog
				open={deleteOpen}
				onOpenChange={setDeleteOpen}
				title={tmr("deleteTitle", { name: data.name })}
				description={
					<>
						{tmr("deleteDescription")}
						{/* Routing rules that use complexity_tier classify with this router; once it is gone they fall back to the built-in defaults. */}
						{data.answers_complexity_tier && (
							<span className="mt-2 block" data-testid="model-router-delete-complexity-tier-warning">
								{tmr("deleteComplexityTierWarning")}
							</span>
						)}
					</>
				}
				isDeleting={isDeleting}
				onConfirm={confirmDelete}
			/>
		</div>
	);
}