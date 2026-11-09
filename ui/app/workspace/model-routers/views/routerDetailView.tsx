import FullPageLoader from "@/components/fullPageLoader";
import PageTitle from "@/components/pageTitle";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { getErrorMessage } from "@/lib/store";
import { useDeleteModelRouterMutation, useGetModelRouterQuery, useUpdateModelRouterMutation } from "@/lib/store/apis/governanceApi";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Link } from "@tanstack/react-router";
import { ArrowLeft, MoreHorizontal, Pencil, ScrollText, Trash2 } from "lucide-react";
import { parseAsStringLiteral, useQueryState } from "nuqs";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { ConfigurationTab } from "./configurationTab";
import { DeleteConfirmDialog } from "./formParts";
import { ModelsTab } from "./modelsTab";
import { OverviewTab, tiersWithModels } from "./overviewTab";
import { RouterConfigurationSheets } from "./routerConfigurationEditor";
import RouterDetailsDialog from "./routerDetailsDialog";
import { RouterEnabledSwitch } from "./routersTable";
import { RoutingTab } from "./routingTab";
import { TestTab } from "./testTab";
import { useRouterConfigForm } from "./useRouterConfigForm";
import type { RouterTraffic, TrafficWindow } from "./useRouterTraffic";

export const ROUTER_TABS = ["overview", "configuration", "models", "routing", "test"] as const;
export type RouterTab = (typeof ROUTER_TABS)[number];

// A count beside a tab's name, as the project page shows them.
function TabCount({ children }: { children: React.ReactNode }) {
	return <span className="text-muted-foreground ml-1 font-mono text-xs">{children}</span>;
}

interface RouterDetailViewProps {
	routerId: string;
	// Every router's last seven days, read once by the page; undefined while it loads.
	traffic: Map<string, RouterTraffic> | undefined;
	// The window those figures cover, so the overview's tier split reads the same one.
	trafficWindow: TrafficWindow;
	onBack: () => void;
}

// RouterDetailView is one model router's page, laid out like a project's: the
// router's name in the topbar, its tabs, and beside them its on/off switch and
// its actions (the requests it took, its details, deleting it).
export default function RouterDetailView({ routerId, traffic, trafficWindow, onBack }: RouterDetailViewProps) {
	const { t } = useTranslation("models");
	const { t: tc } = useTranslation("common");
	const { t: tmr } = useTranslation("models", { keyPrefix: "routing.modelRouters" });
	const canUpdate = useRbac(RbacResource.Governance, RbacOperation.Update);
	const canDelete = useRbac(RbacResource.Governance, RbacOperation.Delete);
	const [tab, setTab] = useQueryState("tab", parseAsStringLiteral(ROUTER_TABS).withDefault("overview").withOptions({ history: "push" }));
	const { data, isLoading, error, refetch } = useGetModelRouterQuery(routerId, { refetchOnMountOrArgChange: true });
	const [updateRouter] = useUpdateModelRouterMutation();
	const [deleteRouter, { isLoading: isDeleting }] = useDeleteModelRouterMutation();
	const [detailsOpen, setDetailsOpen] = useState(false);
	const [deleteOpen, setDeleteOpen] = useState(false);
	// Whether a test ran on this visit; testing is optional and nothing records it.
	const [tested, setTested] = useState(false);

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
					<Button data-testid="model-router-fetch-retry-button" type="button" variant="outline" size="sm" onClick={() => refetch()}>
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

			<Tabs value={tab} onValueChange={(value) => void setTab(value as RouterTab)} className="min-h-0 grow gap-3">
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
								<TabCount>{tiersWithModels(data)}/3</TabCount>
							</TabsTrigger>
							<TabsTrigger value="routing" data-testid="model-router-tab-routing">
								{tmr("tabs.routing")}
							</TabsTrigger>
							<TabsTrigger value="test" data-testid="model-router-tab-test">
								{tmr("tabs.test")}
							</TabsTrigger>
						</TabsList>
					</div>
					<div className="ml-auto flex items-center gap-3">
						<label className="text-muted-foreground flex items-center gap-2 text-sm" data-testid="model-router-enabled">
							{tmr("columnEnabled")}
							<RouterEnabledSwitch router={data} disabled={!canUpdate} />
						</label>
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
					<OverviewTab
						router={data}
						traffic={traffic ? (traffic.get(routerId) ?? { requests: 0, cost: 0, costTrend: null }) : undefined}
						trafficWindow={trafficWindow}
						tested={tested}
						onNavigate={(next) => void setTab(next)}
					/>
				</TabsContent>
				<TabsContent value="configuration" className="flex min-h-0 flex-col overflow-hidden">
					<ConfigurationTab config={config} />
				</TabsContent>
				<TabsContent value="models" className="flex min-h-0 flex-col overflow-hidden">
					<ModelsTab router={data} canUpdate={canUpdate} providers={config.providers.all} keys={config.providers.keys} />
				</TabsContent>
				<TabsContent value="routing" className="flex min-h-0 flex-col overflow-hidden">
					<RoutingTab router={data} canUpdate={canUpdate} />
				</TabsContent>
				<TabsContent value="test" className="flex min-h-0 flex-col overflow-hidden">
					<TestTab router={data} onRun={() => setTested(true)} />
				</TabsContent>
			</Tabs>

			<RouterConfigurationSheets config={config} />
			<RouterDetailsDialog open={detailsOpen} onOpenChange={setDetailsOpen} router={data} />
			<DeleteConfirmDialog
				open={deleteOpen}
				onOpenChange={setDeleteOpen}
				title={tmr("deleteTitle", { name: data.name })}
				description={tmr("deleteDescription")}
				isDeleting={isDeleting}
				onConfirm={confirmDelete}
			/>
		</div>
	);
}