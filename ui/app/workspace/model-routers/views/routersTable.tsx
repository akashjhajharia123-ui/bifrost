import PageTitle from "@/components/pageTitle";
import { PIN_SHADOW_RIGHT } from "@/components/table/columnPinning";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { getLocale } from "@/lib/i18n";
import { getErrorMessage } from "@/lib/store";
import { useDeleteModelRouterMutation, useGetModelRoutersQuery, useSetModelRouterRoutingMutation } from "@/lib/store/apis/governanceApi";
import { ModelRouter } from "@/lib/types/complexityRouter";
import { getScopeLabel } from "@/lib/utils/labels";
import { getPriorityBadgeClass } from "@/lib/utils/routingRules";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Plus } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { DeleteConfirmDialog, RowActionsMenu } from "./formParts";
import { classifierSummary } from "./overviewTab";
import RouterDetailsDialog from "./routerDetailsDialog";
import type { RouterTraffic } from "./useRouterTraffic";

// RouterEnabledSwitch turns a router on or off, as a routing rule's switch
// does. Only `enabled` changes; the rest of the router's routing is sent back
// as it was read.
export function RouterEnabledSwitch({ router, disabled }: { router: ModelRouter; disabled?: boolean }) {
	const { t } = useTranslation("models", { keyPrefix: "routing.modelRouters" });
	const [setRouting] = useSetModelRouterRoutingMutation();
	return (
		<Switch
			data-testid={`model-router-enabled-${router.id}-switch`}
			aria-label={t("enabledAria", { name: router.name })}
			checked={router.routing.enabled}
			size="md"
			disabled={disabled}
			onAsyncCheckedChange={async (checked) => {
				await setRouting({ id: router.id, routing: { ...router.routing, enabled: checked } })
					.unwrap()
					.then(() => toast.success(checked ? t("turnedOn", { name: router.name }) : t("turnedOff", { name: router.name })))
					.catch((err) => toast.error(t("toggleFailed", { name: router.name, error: getErrorMessage(err) })));
			}}
		/>
	);
}

interface RoutersTableProps {
	// Each router's last seven days, read once by the page; undefined while it loads.
	traffic: Map<string, RouterTraffic> | undefined;
	onOpenRouter: (id: string) => void;
	onCreate: () => void;
}

// RoutersTable is the Routers section's list: each router with what it
// classifies with, where it takes requests (scope and priority), how many it
// took over the last week, and whether it is on, with the same row actions as
// routing rules.
export function RoutersTable({ traffic, onOpenRouter, onCreate }: RoutersTableProps) {
	const { t } = useTranslation("models", { keyPrefix: "routing.modelRouters" });
	const { t: tcpx } = useTranslation("models", { keyPrefix: "routing.complexityUi" });
	const canCreate = useRbac(RbacResource.Governance, RbacOperation.Create);
	const canUpdate = useRbac(RbacResource.Governance, RbacOperation.Update);
	const canDelete = useRbac(RbacResource.Governance, RbacOperation.Delete);
	const { data, isLoading, error } = useGetModelRoutersQuery();
	const [deleteRouter, { isLoading: isDeleting }] = useDeleteModelRouterMutation();
	const [editing, setEditing] = useState<ModelRouter | null>(null);
	const [deleting, setDeleting] = useState<ModelRouter | null>(null);
	const routers = data?.routers ?? [];

	const confirmDelete = () => {
		if (!deleting) return;
		deleteRouter(deleting.id)
			.unwrap()
			.then(() => toast.success(t("deleted"), { position: "top-right" }))
			.catch((err) => toast.error(getErrorMessage(err)))
			.finally(() => setDeleting(null));
	};

	return (
		<div className="flex flex-col gap-4" data-testid="model-routers-view">
			<PageTitle title={t("title")} beta>
				{t("pageDescription")}
			</PageTitle>

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
							<TableHead className="font-semibold">{t("columnScope")}</TableHead>
							<TableHead className="text-right font-semibold">{t("columnPriority")}</TableHead>
							<TableHead className="text-right font-semibold">{t("columnRequests")}</TableHead>
							<TableHead className="font-semibold">{t("columnEnabled")}</TableHead>
							<TableHead className={`bg-muted sticky right-0 z-30 w-[50px] text-right font-semibold ${PIN_SHADOW_RIGHT}`} />
						</TableRow>
					</TableHeader>
					<TableBody>
						{isLoading ? (
							<TableRow>
								<TableCell colSpan={7} className="h-10">
									<div className="bg-muted h-2 w-32 animate-pulse rounded" />
								</TableCell>
							</TableRow>
						) : error ? (
							<TableRow>
								<TableCell colSpan={7} className="text-destructive h-24 text-center text-sm">
									{t("loadFailed", { error: getErrorMessage(error) })}
								</TableCell>
							</TableRow>
						) : routers.length === 0 ? (
							<TableRow>
								<TableCell colSpan={7} className="h-24 text-center" data-testid="model-routers-empty">
									<p className="text-sm font-medium">{t("emptyTitle")}</p>
									<p className="text-muted-foreground text-sm">{t("emptyDescription")}</p>
								</TableCell>
							</TableRow>
						) : (
							routers.map((router) => {
								const classifier = classifierSummary(router, tcpx);
								const requests = traffic?.get(router.id)?.requests;
								return (
									<TableRow
										key={router.id}
										className="group hover:bg-muted/50 cursor-pointer transition-colors"
										onClick={() => onOpenRouter(router.id)}
										data-testid={`model-router-row-${router.id}`}
									>
										<TableCell className="font-medium">
											<div className="flex flex-col gap-1">
												<span className="max-w-xs truncate">{router.name}</span>
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
											<Badge variant="secondary">{getScopeLabel(router.routing.scope)}</Badge>
										</TableCell>
										<TableCell className="text-right">
											<div className={`inline-block rounded px-2.5 py-1 text-xs font-medium ${getPriorityBadgeClass()}`}>
												{router.routing.priority}
											</div>
										</TableCell>
										<TableCell className="text-right font-mono tabular-nums">
											{requests ? requests.toLocaleString(getLocale()) : <span className="text-muted-foreground">-</span>}
										</TableCell>
										<TableCell onClick={(event) => event.stopPropagation()}>
											<RouterEnabledSwitch router={router} disabled={!canUpdate} />
										</TableCell>
										<TableCell
											className={`group-hover:bg-muted dark:bg-card dark:group-hover:bg-muted sticky right-0 z-20 bg-white text-right ${PIN_SHADOW_RIGHT}`}
											onClick={(event) => event.stopPropagation()}
										>
											<div className="flex items-center justify-center">
												<RowActionsMenu
													id={router.id}
													testIdPrefix="model-router"
													ariaLabel={t("actionsAria", { name: router.name })}
													canUpdate={canUpdate}
													canDelete={canDelete}
													onEdit={() => setEditing(router)}
													onDelete={() => setDeleting(router)}
												/>
											</div>
										</TableCell>
									</TableRow>
								);
							})
						)}
					</TableBody>
				</Table>
			</div>

			{editing && <RouterDetailsDialog open onOpenChange={(open) => !open && setEditing(null)} router={editing} />}
			<DeleteConfirmDialog
				open={deleting !== null}
				onOpenChange={(open) => !open && setDeleting(null)}
				title={t("deleteTitle", { name: deleting?.name ?? "" })}
				description={t("deleteDescription")}
				isDeleting={isDeleting}
				onConfirm={confirmDelete}
			/>
		</div>
	);
}