/**
 * 额度渲染层的类型化样式契约。
 *
 * 额度 body 在两个宿主穿不同外衣：额度页（QuotaBody.module.scss）与
 * 认证文件卡片（AuthFileQuota.module.scss）。宿主通过 bindQuotaClasses
 * 把自己的 CSS Module 绑定成 QuotaClassMap —— 缺任何一个类名会在模块
 * 初始化时抛错并列出缺失清单，替代旧字符串 styleMap 的静默 class="undefined"。
 */

export interface QuotaClassMap {
  // Shared quota rows.
  quotaRow: string;
  quotaRowHeader: string;
  quotaModel: string;
  quotaMeta: string;
  quotaPercent: string;
  quotaReset: string;
  quotaResetRelative: string;
  quotaResetRelativeSoon: string;
  quotaAmount: string;
  quotaMessage: string;
  // Shared plan chips.
  codexPlan: string;
  codexPlanItem: string;
  codexPlanLabel: string;
  codexPlanValue: string;
  premiumPlanValue: string;
  elitePlanValue: string;
  // Codex reset credits.
  codexResetCredits: string;
  codexResetCreditsTitle: string;
  codexResetCreditRow: string;
  codexResetCreditRowSoon: string;
  codexResetCreditLabel: string;
  codexResetCreditTime: string;
  codexResetCreditsError: string;
  // Quota meter.
  quotaBar: string;
  quotaBarFill: string;
  quotaBarFillHigh: string;
  quotaBarFillMedium: string;
  quotaBarFillLow: string;
}

export const QUOTA_CLASS_KEYS: readonly (keyof QuotaClassMap)[] = [
  'quotaRow',
  'quotaRowHeader',
  'quotaModel',
  'quotaMeta',
  'quotaPercent',
  'quotaReset',
  'quotaResetRelative',
  'quotaResetRelativeSoon',
  'quotaAmount',
  'quotaMessage',
  'codexPlan',
  'codexPlanItem',
  'codexPlanLabel',
  'codexPlanValue',
  'premiumPlanValue',
  'elitePlanValue',
  'codexResetCredits',
  'codexResetCreditsTitle',
  'codexResetCreditRow',
  'codexResetCreditRowSoon',
  'codexResetCreditLabel',
  'codexResetCreditTime',
  'codexResetCreditsError',
  'quotaBar',
  'quotaBarFill',
  'quotaBarFillHigh',
  'quotaBarFillMedium',
  'quotaBarFillLow',
];

/** Bind a host CSS module to the typed quota style contract. */
export function bindQuotaClasses(module: Record<string, string>, source: string): QuotaClassMap {
  const missing = QUOTA_CLASS_KEYS.filter((key) => !module[key]);
  if (missing.length > 0) {
    throw new Error(`[quota] ${source} 缺少额度契约类名: ${missing.join(', ')}`);
  }
  const bound = {} as Record<keyof QuotaClassMap, string>;
  for (const key of QUOTA_CLASS_KEYS) {
    bound[key] = module[key];
  }
  return bound;
}

export interface QuotaBodyProps<TState> {
  quota: TState;
  classes: QuotaClassMap;
}
