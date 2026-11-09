import { useGetDimensionRankingsQuery } from "@/lib/store/apis/logsApi";
import { useMemo, useState } from "react";

const WEEK_MS = 7 * 24 * 60 * 60 * 1000;

// useRouterRequestCounts reads how many requests each model router classified
// over the last seven days, keyed by router id. One rankings call covers every
// router; `all` so a router outside any top-N still gets its figure. The window
// is frozen on mount: a date minted in render would change the query argument
// every render and re-subscribe forever. Undefined while it loads or fails.
export function useRouterRequestCounts(): Map<string, number> | undefined {
	const [window] = useState(() => {
		const now = Date.now();
		return { start_time: new Date(now - WEEK_MS).toISOString(), end_time: new Date(now).toISOString() };
	});
	const { data } = useGetDimensionRankingsQuery({ filters: window, dimension: "model_router", all: true });
	return useMemo(() => {
		if (!data) return undefined;
		const counts = new Map<string, number>();
		for (const entry of data.rankings ?? []) counts.set(entry.id, entry.total_requests);
		return counts;
	}, [data]);
}