import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { LoaderCircle } from "lucide-react";
import { useTranslation } from "react-i18next";

interface Props {
	// Groups accepted from Suggestions and staged in the draft.
	groups: number;
	// Whether the impact of the draft is what is on screen.
	onImpact: boolean;
	canSave: boolean;
	isSaving: boolean;
	// Why the last save failed, shown beside the actions that started it.
	error?: string | null;
	onDiscard: () => void;
	onReviewImpact: () => void;
	onApply: () => void;
}

// SuggestionsDraftBar stays at the foot of a router's page while groups
// accepted from Suggestions wait in the draft: they can be thrown away, their
// impact reviewed, and, from the review, applied with the rest of the draft.
export default function SuggestionsDraftBar({ groups, onImpact, canSave, isSaving, error, onDiscard, onReviewImpact, onApply }: Props) {
	const { t } = useTranslation("models", { keyPrefix: "routing.complexityUi.suggestions.draftBar" });
	return (
		<div
			className="bg-card flex flex-wrap items-center gap-x-3 gap-y-2 rounded-sm border px-4 py-2.5 shadow-sm"
			data-testid="complexity-suggestions-draft-bar"
		>
			<Badge variant="warning">{t("badge")}</Badge>
			<span className="text-muted-foreground text-sm" data-testid="complexity-suggestions-draft-message">
				{t("message", { count: groups })}
			</span>
			<div className="ml-auto flex items-center gap-2">
				<Button
					type="button"
					variant="ghost"
					size="sm"
					onClick={onDiscard}
					disabled={isSaving}
					data-testid="complexity-suggestions-draft-discard"
				>
					{t("discard")}
				</Button>
				{onImpact ? (
					<Button
						type="button"
						size="sm"
						onClick={onApply}
						disabled={!canSave || isSaving}
						data-testid="complexity-suggestions-draft-apply"
					>
						{isSaving && <LoaderCircle className="size-3.5 animate-spin" />}
						{t("apply")}
					</Button>
				) : (
					<Button type="button" size="sm" onClick={onReviewImpact} data-testid="complexity-suggestions-draft-review">
						{t("reviewImpact")}
					</Button>
				)}
			</div>
			{error && (
				<p role="alert" className="text-destructive w-full text-xs" data-testid="complexity-suggestions-draft-error">
					{error}
				</p>
			)}
		</div>
	);
}