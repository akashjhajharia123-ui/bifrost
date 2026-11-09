import { useTranslation } from "react-i18next";
import { ModelRoutingRulesView } from "../modelRoutingRulesView";

// RoutingTab lists the model routing rules that send requests to the router.
// A rule's condition can match who is calling (a team, a virtual key) or the
// model name an app asks for, which is how an app calls a router by name.
export function RoutingTab({ routerId }: { routerId: string }) {
	const { t } = useTranslation("models", { keyPrefix: "routing.modelRouters.routing" });
	return (
		<div className="custom-scrollbar flex min-h-0 grow flex-col gap-4 overflow-y-auto pb-6" data-testid="model-router-routing">
			<ModelRoutingRulesView routerId={routerId} />
			<p className="text-muted-foreground text-xs">{t("orderNote")}</p>
		</div>
	);
}