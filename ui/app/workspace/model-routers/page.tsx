import { useActionParam } from "@/hooks/useActionParam";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { parseAsString, parseAsStringLiteral, useQueryStates } from "nuqs";
import { useState } from "react";
import RouterDetailView from "./views/detail/routerDetailView";
import { MODEL_ROUTERS_VIEWS, ModelRoutersView } from "./views/modelRoutersView";
import RouterWizard from "./views/wizard/routerWizard";

// ModelRoutersPage lists the model routers and, in a second view, every model
// routing rule in the order they are tried; creates a router in a full-page
// wizard; and opens a router's page when the URL names it. Each router's
// Routing tab lists the rules that send requests to it.
export default function ModelRoutersPage() {
	const canCreate = useRbac(RbacResource.Governance, RbacOperation.Create);
	const [urlState, setUrlState] = useQueryStates(
		{
			router: parseAsString,
			// The router page's own tab, cleared when leaving it.
			tab: parseAsString,
			// The list's view: the routers, or every model routing rule.
			view: parseAsStringLiteral(MODEL_ROUTERS_VIEWS).withDefault("routers"),
		},
		{ history: "push" },
	);
	const [creating, setCreating] = useState(false);
	useActionParam("create", () => setCreating(true), { isOpen: creating, enabled: canCreate });

	if (creating) {
		return (
			<RouterWizard
				onCancel={() => setCreating(false)}
				onCreated={(id) => {
					setCreating(false);
					void setUrlState({ router: id, tab: null });
				}}
			/>
		);
	}

	if (urlState.router) {
		return (
			<RouterDetailView key={urlState.router} routerId={urlState.router} onBack={() => void setUrlState({ router: null, tab: null })} />
		);
	}

	return (
		<div className="no-padding-parent mx-auto flex w-full min-w-0 flex-col gap-4 p-4">
			<ModelRoutersView
				view={urlState.view}
				onViewChange={(view) => void setUrlState({ view: view === "routers" ? null : view })}
				onOpenRouter={(id) => void setUrlState({ router: id, view: null })}
				onCreate={() => setCreating(true)}
			/>
		</div>
	);
}