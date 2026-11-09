import { CELRuleBuilder, type ConditionMode, initialConditionMode, RuleScopeEntityField, RuleScopeSelect, scopeNoun } from "./ruleShared";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { getErrorMessage } from "@/lib/store";
import {
	useCreateModelRoutingRuleMutation,
	useGetModelRoutersQuery,
	useUpdateModelRoutingRuleMutation,
} from "@/lib/store/apis/governanceApi";
import { useGetProvidersQuery } from "@/lib/store/apis/providersApi";
import { ModelRoutingRule, ModelRoutingRuleRequest } from "@/lib/types/complexityRouter";
import { RoutingRuleScope } from "@/lib/types/routingRules";
import { validateRateLimitAndBudgetRules, validateRoutingRules } from "@/lib/utils/celConverterRouting";
import { normalizeRoutingRuleGroupQuery } from "@/lib/utils/routingRuleGroupQuery";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { RuleGroupType } from "react-querybuilder";
import { toast } from "sonner";

interface RuleFormData {
	name: string;
	description: string;
	enabled: boolean;
	model_router_id: string;
	scope: string;
	scope_id: string;
	priority: number;
	cel_expression: string;
}

const defaultQuery: RuleGroupType = { combinator: "and", rules: [] };

// The tier is what the rule's router decides once the rule has matched, so a
// rule's condition cannot use it.
const EXCLUDED_CEL_FIELDS = ["complexity_tier"];

const emptyForm = (routerId: string): RuleFormData => ({
	name: "",
	description: "",
	enabled: true,
	model_router_id: routerId,
	scope: RoutingRuleScope.Global,
	scope_id: "",
	priority: 0,
	cel_expression: "",
});

interface ModelRoutingRuleSheetProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	editingRule?: ModelRoutingRule | null;
	// The router a new rule starts on, such as the one whose page opened it.
	defaultRouterId?: string;
	// Set when the sheet is opened from a router's page: the rule stays on it.
	lockRouter?: boolean;
}

// ModelRoutingRuleSheet creates or edits a model routing rule. It follows the
// routing rule sheet: the same name, scope, priority and condition fields.
// The rule only decides which requests its router classifies; the router's
// own targets decide the model each tier gets.
export function ModelRoutingRuleSheet({ open, onOpenChange, editingRule, defaultRouterId, lockRouter }: ModelRoutingRuleSheetProps) {
	const { t } = useTranslation("models");
	const { t: tc } = useTranslation("common");
	const { t: tmr } = useTranslation("models", { keyPrefix: "routing.modelRoutingRules" });
	const isEditing = Boolean(editingRule);
	const { data: routersData } = useGetModelRoutersQuery(undefined, { skip: !open });
	const { data: providersData = [] } = useGetProvidersQuery(undefined, { skip: !open });
	const [createRule, { isLoading: isCreating }] = useCreateModelRoutingRuleMutation();
	const [updateRule, { isLoading: isUpdating }] = useUpdateModelRoutingRuleMutation();

	const [query, setQuery] = useState<RuleGroupType>(defaultQuery);
	const [conditionMode, setConditionMode] = useState<ConditionMode>("builder");
	const [builderKey, setBuilderKey] = useState(0);
	const [celError, setCelError] = useState<string | null>(null);
	const [scopeError, setScopeError] = useState<string | null>(null);

	const {
		register,
		handleSubmit,
		reset,
		setValue,
		watch,
		formState: { errors },
	} = useForm<RuleFormData>({ defaultValues: emptyForm(defaultRouterId ?? "") });
	const scope = watch("scope");
	const scopeId = watch("scope_id");
	const enabled = watch("enabled");
	const routerId = watch("model_router_id");

	useEffect(() => {
		if (!open) return;
		reset(
			editingRule
				? {
						name: editingRule.name,
						description: editingRule.description ?? "",
						enabled: editingRule.enabled,
						model_router_id: editingRule.model_router_id,
						scope: editingRule.scope,
						scope_id: editingRule.scope_id ?? "",
						priority: editingRule.priority,
						cel_expression: editingRule.cel_expression ?? "",
					}
				: emptyForm(defaultRouterId ?? ""),
		);
		setQuery(editingRule ? normalizeRoutingRuleGroupQuery(editingRule.query) : defaultQuery);
		setConditionMode(initialConditionMode(editingRule));
		setBuilderKey((key) => key + 1);
		setCelError(null);
		setScopeError(null);
	}, [open, editingRule, defaultRouterId, reset]);

	const availableProviders = useMemo(() => providersData.map((provider) => provider.name), [providersData]);

	const handleQueryChange = useCallback(
		(expression: string, newQuery: RuleGroupType) => {
			setValue("cel_expression", expression);
			setQuery(newQuery);
			setCelError(null);
		},
		[setValue],
	);
	const handleModeChange = useCallback((mode: ConditionMode) => {
		setConditionMode(mode);
		setCelError(null);
	}, []);

	const onSubmit = (data: RuleFormData) => {
		setCelError(null);
		const missingScope = data.scope !== RoutingRuleScope.Global && !data.scope_id.trim();
		setScopeError(missingScope ? t("routing.scopeRequired", { scope: scopeNoun(data.scope, t) }) : null);
		if (missingScope) return;

		// Builder-only checks: they read the visual query, which raw-CEL mode does
		// not have. A CEL expression is checked by the gateway on save instead.
		if (conditionMode === "builder") {
			const builderErrors = [...validateRoutingRules(query), ...validateRateLimitAndBudgetRules(query)];
			if (builderErrors.length > 0) {
				setCelError(builderErrors.join("\n"));
				return;
			}
		}

		const payload: ModelRoutingRuleRequest = {
			name: data.name,
			description: data.description,
			enabled: data.enabled,
			cel_expression: data.cel_expression,
			query: conditionMode === "builder" ? query : undefined,
			model_router_id: data.model_router_id,
			scope: data.scope,
			scope_id: data.scope === RoutingRuleScope.Global ? undefined : data.scope_id || undefined,
			priority: data.priority,
		};
		const saving = editingRule ? updateRule({ id: editingRule.id, data: payload }).unwrap() : createRule(payload).unwrap();
		saving
			.then(() => {
				toast.success(isEditing ? tmr("updatedSuccess") : tmr("createdSuccess"));
				onOpenChange(false);
			})
			.catch((error) => {
				const message = getErrorMessage(error);
				// A malformed condition is a field-level problem: show it under the
				// editor, as the routing rule sheet does.
				if (/cel|condition|complexity_tier/i.test(message)) {
					setCelError(message);
					return;
				}
				toast.error(message);
			});
	};

	const routers = routersData?.routers ?? [];

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="flex w-full min-w-1/2 flex-col gap-4 overflow-x-hidden p-0 pt-4" data-testid="model-routing-rule-sheet">
				<SheetHeader className="flex flex-col items-start py-4" headerClassName="mb-0 sticky -top-4 bg-card z-10 px-4 md:px-8">
					<SheetTitle>{isEditing ? tmr("editTitle") : tmr("createTitle")}</SheetTitle>
					<SheetDescription>{isEditing ? tmr("editDescription") : tmr("createDescription")}</SheetDescription>
				</SheetHeader>

				<form onSubmit={handleSubmit(onSubmit)} className="flex grow flex-col" noValidate>
					<div className="flex grow flex-col gap-6 px-4 pb-6 md:px-8">
						<div className="space-y-3">
							<Label htmlFor="model-routing-rule-name">
								{t("routing.ruleName")} <span className="text-red-500">*</span>
							</Label>
							<Input
								id="model-routing-rule-name"
								placeholder={t("routing.ruleNamePlaceholder")}
								data-testid="model-routing-rule-name-input"
								{...register("name", { required: t("routing.ruleNameRequired") })}
							/>
							{errors.name && <p className="text-destructive text-sm">{errors.name.message}</p>}
						</div>

						<div className="space-y-3">
							<Label htmlFor="model-routing-rule-description">{t("virtualKeys.description")}</Label>
							<Textarea
								id="model-routing-rule-description"
								rows={2}
								placeholder={t("routing.descriptionPlaceholder")}
								data-testid="model-routing-rule-description-input"
								{...register("description")}
							/>
						</div>

						<div className="flex items-center justify-between rounded-lg border p-4">
							<div className="space-y-0.5">
								<Label htmlFor="model-routing-rule-enabled">{t("routing.enableRule")}</Label>
								<p className="text-muted-foreground text-sm">{t("routing.enableRuleHelp")}</p>
							</div>
							<Switch
								id="model-routing-rule-enabled"
								checked={enabled}
								onCheckedChange={(checked) => setValue("enabled", checked)}
								data-testid="model-routing-rule-enabled-switch"
							/>
						</div>

						<div className="space-y-3">
							<Label htmlFor="model-routing-rule-router">
								{tmr("routerLabel")} <span className="text-red-500">*</span>
							</Label>
							<Select
								value={routerId}
								onValueChange={(value) => setValue("model_router_id", value, { shouldValidate: true })}
								disabled={lockRouter}
							>
								<SelectTrigger
									id="model-routing-rule-router"
									className="w-full"
									data-testid="model-routing-rule-router-select"
									aria-invalid={Boolean(errors.model_router_id)}
								>
									<SelectValue placeholder={tmr("routerPlaceholder")} />
								</SelectTrigger>
								<SelectContent>
									{routers.map((router) => (
										<SelectItem key={router.id} value={router.id} data-testid={`model-routing-rule-router-option-${router.id}`}>
											{router.name}
										</SelectItem>
									))}
									{/* A rule naming a router this list has not loaded keeps its value. */}
									{routerId && !routers.some((router) => router.id === routerId) && <SelectItem value={routerId}>{routerId}</SelectItem>}
								</SelectContent>
							</Select>
							<input type="hidden" {...register("model_router_id", { required: tmr("routerRequired") })} />
							{errors.model_router_id && <p className="text-destructive text-sm">{errors.model_router_id.message}</p>}
							<p className="text-muted-foreground text-xs">{tmr("routerHelp")}</p>
						</div>

						<div className="grid grid-cols-1 gap-4 md:grid-cols-2">
							<RuleScopeSelect
								id="model-routing-rule-scope"
								value={scope}
								onChange={(value) => {
									setValue("scope", value);
									setValue("scope_id", "");
								}}
								testId="model-routing-rule-scope-select"
							/>
							<div className="space-y-3">
								<Label htmlFor="model-routing-rule-priority">
									{t("routing.priority")} <span className="text-red-500">*</span>
								</Label>
								<Input
									id="model-routing-rule-priority"
									type="number"
									min={0}
									max={1000}
									data-testid="model-routing-rule-priority-input"
									{...register("priority", {
										required: t("routing.priorityRequired"),
										min: { value: 0, message: t("routing.priorityMin") },
										max: { value: 1000, message: t("routing.priorityMax") },
										valueAsNumber: true,
									})}
								/>
								<p className="text-muted-foreground text-xs">{tmr("priorityHelp")}</p>
								{errors.priority && <p className="text-destructive text-sm">{errors.priority.message}</p>}
							</div>
						</div>

						{scope !== RoutingRuleScope.Global && (
							<RuleScopeEntityField
								scope={scope}
								value={scopeId}
								onChange={(value) => setValue("scope_id", value)}
								error={scopeError ?? undefined}
								userInputTestId="model-routing-rule-scope-user-input"
							/>
						)}

						<Separator />

						<div className="space-y-3">
							<Label>{tmr("conditionLabel")}</Label>
							<p className="text-muted-foreground text-sm">{tmr("conditionHelp")}</p>
							<CELRuleBuilder
								key={builderKey}
								initialQuery={query}
								onChange={handleQueryChange}
								providers={availableProviders}
								models={[]}
								allowCustomModels
								allowCelMode
								initialMode={conditionMode}
								initialCel={editingRule?.cel_expression ?? ""}
								onModeChange={handleModeChange}
								celError={celError}
								excludeFields={EXCLUDED_CEL_FIELDS}
							/>
						</div>
					</div>

					<div className="bg-card sticky bottom-0 flex justify-end gap-2 border-t px-4 py-3 md:px-8">
						<Button type="button" variant="outline" onClick={() => onOpenChange(false)} data-testid="model-routing-rule-cancel-button">
							{tc("cancel")}
						</Button>
						<Button type="submit" disabled={isCreating || isUpdating} data-testid="model-routing-rule-save-button">
							{isEditing ? tc("save") : tmr("createButton")}
						</Button>
					</div>
				</form>
			</SheetContent>
		</Sheet>
	);
}