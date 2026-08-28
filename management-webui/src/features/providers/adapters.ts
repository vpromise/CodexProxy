import type { OpenAIProviderConfig, ProviderKeyConfig } from '@/types';
import { hasDisableAllModelsRule, stripDisableAllModelsRule } from '@/components/providers/utils';
import { maskApiKey } from '@/utils/format';
import type { ProviderBrand, ProviderResource, ProviderResourceSelector } from './types';

const countHeaders = (headers?: Record<string, string>): number =>
  headers ? Object.keys(headers).length : 0;

const collectModelNames = (models?: Array<{ name?: string }>): string[] => {
  const seen = new Set<string>();
  (models ?? []).forEach((model) => {
    const name = (model?.name ?? '').trim();
    if (name) seen.add(name);
  });
  return Array.from(seen);
};

const normalizePriority = (priority?: number): number =>
  typeof priority === 'number' && Number.isFinite(priority) ? priority : 0;

const buildID = (brand: ProviderBrand, index: number, fragment: string) =>
  `${brand}:${index}:${fragment || 'item'}`;

const truncateForID = (value: string | undefined | null): string => {
  const trimmed = String(value ?? '').trim();
  if (!trimmed) return '';
  return trimmed.length <= 12 ? trimmed : trimmed.slice(0, 8);
};

function providerKeyToResource(
  brand: 'codex' | 'claude',
  config: ProviderKeyConfig,
  index: number
): ProviderResource {
  const apiKey = config.apiKey ?? '';
  const flags: ProviderResource['flags'] = {};
  if (brand === 'codex') flags.websockets = config.websockets === true;
  if (brand === 'claude') {
    flags.cloakEnabled = Boolean(config.cloak?.mode?.trim());
    flags.claudeCodeCliProfile = config.fingerprintProfile === 'claude-code-cli';
  }

  const selector: ProviderResourceSelector = {
    brand,
    apiKey,
    baseUrl: config.baseUrl,
    index,
  };

  return {
    id: buildID(brand, index, truncateForID(apiKey)),
    brand,
    originalIndex: index,
    name: null,
    identifier: maskApiKey(apiKey) || `#${index + 1}`,
    apiKeyPreview: apiKey ? maskApiKey(apiKey) : null,
    apiKey: apiKey || null,
    authIndex: config.authIndex ?? null,
    baseUrl: config.baseUrl ?? null,
    proxyUrl: config.proxyUrl ?? null,
    prefix: config.prefix ?? null,
    modelCount: config.models?.length ?? 0,
    models: collectModelNames(config.models),
    priority: normalizePriority(config.priority),
    headerCount: countHeaders(config.headers),
    excludedModelCount: stripDisableAllModelsRule(config.excludedModels).length,
    apiKeyEntryCount: 0,
    disabled: hasDisableAllModelsRule(config.excludedModels),
    flags,
    selector,
    raw: config,
  };
}

export const codexToResource = (config: ProviderKeyConfig, index: number): ProviderResource =>
  providerKeyToResource('codex', config, index);

export const claudeToResource = (config: ProviderKeyConfig, index: number): ProviderResource =>
  providerKeyToResource('claude', config, index);

export function openaiToResource(config: OpenAIProviderConfig, index: number): ProviderResource {
  const sourceIndex = config.sourceIndex ?? index;
  const name = (config.name ?? '').trim();
  const firstEntry = config.apiKeyEntries?.[0];
  const previewApiKey = firstEntry?.apiKey ? maskApiKey(firstEntry.apiKey) : null;
  return {
    id: buildID('openaiCompatibility', sourceIndex, truncateForID(name) || `#${sourceIndex}`),
    brand: 'openaiCompatibility',
    originalIndex: sourceIndex,
    name: name || null,
    identifier: name || `#${sourceIndex + 1}`,
    apiKeyPreview: previewApiKey,
    apiKey: null,
    authIndex: config.authIndex ?? null,
    baseUrl: config.baseUrl ?? null,
    proxyUrl: null,
    prefix: config.prefix ?? null,
    modelCount: config.models?.length ?? 0,
    models: collectModelNames(config.models),
    priority: normalizePriority(config.priority),
    headerCount: countHeaders(config.headers),
    excludedModelCount: 0,
    apiKeyEntryCount: config.apiKeyEntries?.length ?? 0,
    disabled: config.disabled === true,
    flags: {},
    selector: { brand: 'openaiCompatibility', name, index: sourceIndex },
    raw: config,
  };
}
