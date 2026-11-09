import { createFileRoute, redirect } from "@tanstack/react-router";

// The complexity router is now a model router. Links to its old page open the
// model routers list, where the router that answers complexity_tier is marked.
export const Route = createFileRoute("/workspace/complexity-router")({
	beforeLoad: () => {
		// A route target, not a root-relative href, so a base path is kept.
		throw redirect({ to: "/workspace/model-routers", replace: true });
	},
});