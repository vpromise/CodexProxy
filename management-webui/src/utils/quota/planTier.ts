import { normalizePlanType } from './parsers';

/** Map Codex plan tiers to badge styles. */
export type CodexPlanTier = 'elite' | 'premium' | 'plain';

export const PREMIUM_CODEX_PLAN_TYPES = new Set(['pro', 'prolite', 'pro-lite', 'pro_lite']);

// Pro 20x uses the elite badge style.
export const ELITE_CODEX_PLAN_TYPE = 'pro';

/** Check elite first because `pro` also belongs to the premium set. */
export function resolvePlanTier(planType: string | null | undefined): CodexPlanTier {
  const normalized = normalizePlanType(planType);
  if (!normalized) return 'plain';
  if (normalized === ELITE_CODEX_PLAN_TYPE) return 'elite';
  if (PREMIUM_CODEX_PLAN_TYPES.has(normalized)) return 'premium';
  return 'plain';
}
