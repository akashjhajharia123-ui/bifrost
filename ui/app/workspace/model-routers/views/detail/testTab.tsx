import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { getErrorMessage } from "@/lib/store";
import { useClassifyWithModelRouterMutation } from "@/lib/store/apis/governanceApi";
import { COMPLEXITY_TIER_VALUES, DecisionTier, ModelRouter, ModelRouterClassifyInput } from "@/lib/types/complexityRouter";
import { CircleAlert, CircleCheck, LoaderCircle, Play } from "lucide-react";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

const TIER_LABEL_KEYS = { SIMPLE: "tierSimple", MEDIUM: "tierMedium", COMPLEX: "tierComplex" } as const satisfies Record<
	DecisionTier,
	string
>;

// One call classifies at most this many statements, as the gateway allows.
export const MAX_TEST_STATEMENTS = 100;

const TIER_PREFIX = new RegExp(`^(${COMPLEXITY_TIER_VALUES.join("|")})\\s*:\\s*`, "i");

// parseTestStatements reads one statement per line. A line may start with
// SIMPLE:, MEDIUM: or COMPLEX: to say which tier it should get; blank lines
// are skipped.
export function parseTestStatements(text: string): ModelRouterClassifyInput[] {
	return text
		.split("\n")
		.map((line) => line.trim())
		.filter(Boolean)
		.map((line, index) => {
			const match = line.match(TIER_PREFIX);
			const expected = match ? (match[1].toUpperCase() as DecisionTier) : undefined;
			return {
				id: String(index + 1),
				text: match ? line.slice(match[0].length).trim() : line,
				...(expected ? { expected_tier: expected } : {}),
			};
		})
		.filter((input) => input.text !== "");
}

// The model a tier sends requests to, as the router's targets name it.
function tierModels(router: ModelRouter, tier: string | undefined): string {
	if (!tier) return "";
	return (router.targets ?? [])
		.filter((target) => target.tier === tier)
		.map((target) => [target.provider, target.model].filter(Boolean).join("/"))
		.join(", ");
}

// TestTab classifies statements with the router as it is served now, so its
// tiers can be checked before traffic depends on them. Nothing is saved and
// no session or suggestion is touched; a decision-model or llm router makes
// one model call per statement.
export function TestTab({ router, onRun }: { router: ModelRouter; onRun?: () => void }) {
	const { t } = useTranslation("models", { keyPrefix: "routing.modelRouters.test" });
	const { t: tcpx } = useTranslation("models", { keyPrefix: "routing.complexityUi" });
	const tierLabel = (tier: string | undefined) =>
		tier && tier in TIER_LABEL_KEYS ? tcpx(TIER_LABEL_KEYS[tier as DecisionTier]) : (tier ?? "");
	const [text, setText] = useState("");
	const [classify, { data, isLoading, error, reset }] = useClassifyWithModelRouterMutation();
	const inputs = useMemo(() => parseTestStatements(text), [text]);
	const tooMany = inputs.length > MAX_TEST_STATEMENTS;

	const run = () => {
		if (inputs.length === 0 || tooMany) return;
		// Only a run that classified counts as tested; a failure shows in the
		// mutation's own error state.
		classify({ id: router.id, inputs })
			.unwrap()
			.then(() => onRun?.())
			.catch(() => {});
	};

	return (
		<div className="custom-scrollbar flex min-h-0 grow flex-col gap-4 overflow-y-auto pb-6" data-testid="model-router-test">
			<div className="space-y-2">
				<p className="text-muted-foreground text-sm">{t("description")}</p>
				<Textarea
					value={text}
					onChange={(event) => {
						setText(event.target.value);
						if (data || error) reset();
					}}
					rows={6}
					placeholder={t("placeholder")}
					className="font-mono text-sm"
					data-testid="model-router-test-input"
				/>
				<div className="flex flex-wrap items-center gap-3">
					<Button
						type="button"
						size="sm"
						onClick={run}
						disabled={inputs.length === 0 || tooMany || isLoading}
						data-testid="model-router-test-run"
					>
						{isLoading ? <LoaderCircle className="h-3.5 w-3.5 animate-spin" /> : <Play className="h-3.5 w-3.5" />}
						{t("run", { count: inputs.length })}
					</Button>
					<span className={tooMany ? "text-destructive text-xs" : "text-muted-foreground text-xs"}>
						{tooMany ? t("tooMany", { max: MAX_TEST_STATEMENTS }) : t("hint")}
					</span>
				</div>
			</div>

			{error ? (
				<Alert variant="destructive" data-testid="model-router-test-error">
					<CircleAlert className="h-4 w-4" />
					<AlertDescription>{t("failed", { error: getErrorMessage(error) })}</AlertDescription>
				</Alert>
			) : null}

			{data && (
				<>
					<div className="flex flex-wrap items-center gap-2 text-sm" data-testid="model-router-test-summary">
						{COMPLEXITY_TIER_VALUES.map((tier) => (
							<Badge key={tier} variant="secondary" className="font-normal">
								{tierLabel(tier)} {data.summary.tiers[tier] ?? 0}
							</Badge>
						))}
						{(data.summary.tiers.UNCLASSIFIED ?? 0) > 0 && (
							<Badge variant="warning" className="font-normal">
								{t("unclassified")} {data.summary.tiers.UNCLASSIFIED}
							</Badge>
						)}
						{data.summary.labeled > 0 && (
							<span className="text-muted-foreground" data-testid="model-router-test-accuracy">
								{t("accuracy", { correct: data.summary.correct, labeled: data.summary.labeled })}
							</span>
						)}
					</div>
					<div className="overflow-hidden rounded-sm border">
						<Table>
							<TableHeader className="bg-muted">
								<TableRow>
									<TableHead className="font-semibold">{t("columnStatement")}</TableHead>
									<TableHead className="font-semibold">{t("columnTier")}</TableHead>
									<TableHead className="font-semibold">{t("columnHow")}</TableHead>
									<TableHead className="font-semibold">{t("columnModel")}</TableHead>
								</TableRow>
							</TableHeader>
							<TableBody>
								{data.results.map((result) => (
									<TableRow key={result.index} data-testid={`model-router-test-result-${result.index}`}>
										<TableCell className="max-w-md align-top whitespace-normal">{result.text}</TableCell>
										<TableCell className="align-top">
											<div className="flex items-center gap-1.5">
												<span>{result.tier ? tierLabel(result.tier) : t("unclassified")}</span>
												{result.correct === true && <CircleCheck className="size-3.5 text-green-600" aria-label={t("correct")} />}
												{result.correct === false && (
													<span className="text-destructive text-xs">{t("expected", { tier: tierLabel(result.expected_tier) })}</span>
												)}
											</div>
										</TableCell>
										<TableCell className="text-muted-foreground align-top text-xs">
											{result.mechanism}
											{result.confidence !== undefined
												? ` · ${result.confidence.toFixed(2)}`
												: result.score !== undefined
													? ` · ${result.score.toFixed(2)}`
													: ""}
										</TableCell>
										<TableCell className="align-top font-mono text-xs">
											{tierModels(router, result.tier) || <span className="text-muted-foreground">{t("keepsModel")}</span>}
										</TableCell>
									</TableRow>
								))}
							</TableBody>
						</Table>
					</div>
				</>
			)}
		</div>
	);
}