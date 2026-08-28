import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { providersApi } from '@/services/api';
import { getErrorMessage } from '@/utils/helpers';
import { useAuthStore, useConfigStore } from '@/stores';
import {
  withDisableAllModelsRule,
  withoutDisableAllModelsRule,
} from '@/components/providers/utils';
import type { ModelAlias, OpenAIProviderConfig, ProviderKeyConfig } from '@/types';
import { claudeToResource, codexToResource, openaiToResource } from './adapters';
import { PROVIDER_BRAND_ORDER } from './descriptors';
import { buildThinkingFromLevels } from './thinkingLevels';
import type {
  ProviderBrand,
  ProviderEntryFormInput,
  ProviderGroup,
  ProviderResource,
  ProviderSnapshot,
} from './types';

export interface UseProviderWorkbenchResult {
  connected: boolean;
  isPending: boolean;
  isFetching: boolean;
  isError: boolean;
  errorMessage: string | null;
  snapshot: ProviderSnapshot | null;
  refetch: () => Promise<void>;
  createProvider: (brand: ProviderBrand, input: ProviderEntryFormInput) => Promise<void>;
  updateProvider: (resource: ProviderResource, input: ProviderEntryFormInput) => Promise<void>;
  deleteProvider: (resource: ProviderResource) => Promise<void>;
  toggleDisabled: (resource: ProviderResource, disabled: boolean) => Promise<void>;
  mutating: boolean;
  refreshSnapshot: () => void;
}

const parseTextList = (text: string): string[] =>
  text
    .split(/[\n,]+/)
    .map((item) => item.trim())
    .filter(Boolean);

const headersFromEntries = (
  entries: Array<{ key: string; value: string }>
): Record<string, string> => {
  const out: Record<string, string> = {};
  entries.forEach((entry) => {
    const key = entry.key.trim();
    if (key) out[key] = entry.value;
  });
  return out;
};

const parseThinkingJSON = (value: string | undefined): Record<string, unknown> | undefined => {
  const trimmed = (value ?? '').trim();
  if (!trimmed) return undefined;
  const parsed = JSON.parse(trimmed) as unknown;
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new Error('Thinking config must be a JSON object');
  }
  return parsed as Record<string, unknown>;
};

export const buildExcludedModels = (
  textValue: string,
  disabled: boolean,
  brand: ProviderBrand
): string[] | undefined => {
  const filtered = parseTextList(textValue).filter((value) => value !== '*');
  if (brand === 'openaiCompatibility') return filtered.length ? filtered : undefined;
  return disabled ? withDisableAllModelsRule(filtered) : filtered.length ? filtered : undefined;
};

const buildModelAliases = (
  models: ProviderEntryFormInput['models'] | undefined,
  includeImage = false
): ModelAlias[] =>
  (models ?? [])
    .map((model) => {
      const entry: ModelAlias = {
        name: model.name.trim(),
        alias: model.alias?.trim() || undefined,
        priority: model.priority,
        testModel: model.testModel,
        thinking: model.thinkingLevelsTouched
          ? buildThinkingFromLevels(model.thinkingLevels)
          : parseThinkingJSON(model.thinkingJson),
      };
      if (includeImage) entry.image = model.image === true;
      return entry;
    })
    .filter((model) => model.name);

const buildProviderKeyConfig = (
  brand: 'codex' | 'claude',
  input: ProviderEntryFormInput,
  existing?: ProviderKeyConfig | null
): ProviderKeyConfig => {
  const headers = headersFromEntries(input.headers);
  const models = buildModelAliases(input.models);
  const next: ProviderKeyConfig = {
    apiKey: input.apiKey.trim() || existing?.apiKey || '',
    priority: input.priority,
    weight: input.weight,
    prefix: input.prefix.trim() || undefined,
    baseUrl: input.baseUrl.trim() || undefined,
    proxyUrl: input.proxyUrl.trim() || undefined,
    models: models.length ? models : undefined,
    headers: Object.keys(headers).length ? headers : undefined,
    excludedModels: buildExcludedModels(input.excludedModelsText, input.disabled, brand),
    disableCooling:
      input.disableCooling === true
        ? true
        : existing?.disableCooling !== undefined
          ? false
          : undefined,
    authIndex: existing?.authIndex,
  };
  if (brand === 'codex') next.websockets = input.websockets;
  if (brand === 'claude') {
    next.fingerprintProfile = input.fingerprintProfile?.trim() || undefined;
    if (input.cloak) {
      next.cloak = {
        mode: input.cloak.mode.trim() || undefined,
        strictMode: input.cloak.strictMode,
        sensitiveWords: parseTextList(input.cloak.sensitiveWordsText),
        cacheUserId: input.cloak.cacheUserId === true,
      };
    }
  }
  return next;
};

const buildOpenAIConfig = (
  input: ProviderEntryFormInput,
  existing?: OpenAIProviderConfig | null
): OpenAIProviderConfig => {
  const headers = headersFromEntries(input.headers);
  const models = buildModelAliases(input.models, true);
  const apiKeyEntries =
    input.apiKeyEntries
      ?.map((entry, index) => ({
        apiKey:
          entry.apiKey.trim() ||
          entry.existingApiKey?.trim() ||
          existing?.apiKeyEntries?.[index]?.apiKey?.trim() ||
          '',
        proxyUrl: entry.proxyUrl.trim() || undefined,
        weight: entry.weight,
        authIndex: entry.authIndex?.trim() || undefined,
      }))
      .filter((entry) => entry.apiKey) ?? [];

  return {
    ...(existing ?? {}),
    name: input.name.trim(),
    baseUrl: input.baseUrl.trim(),
    prefix: input.prefix.trim() || undefined,
    apiKeyEntries,
    disabled: input.disabled,
    disableCooling:
      input.disableCooling === true
        ? true
        : existing?.disableCooling !== undefined
          ? false
          : undefined,
    headers: Object.keys(headers).length ? headers : undefined,
    models: models.length ? models : undefined,
    priority: input.priority,
    testModel: input.testModel?.trim() || undefined,
  };
};

export function useProviderWorkbench(): UseProviderWorkbenchResult {
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const config = useConfigStore((state) => state.config);
  const fetchConfig = useConfigStore((state) => state.fetchConfig);
  const isCacheValid = useConfigStore((state) => state.isCacheValid);
  const [isPending, setIsPending] = useState(() => !isCacheValid());
  const [isFetching, setIsFetching] = useState(false);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [mutating, setMutating] = useState(false);
  const [fetchedAt, setFetchedAt] = useState(() => new Date().toISOString());
  const hasFetchedRef = useRef(false);
  const connected = connectionStatus === 'connected';

  const refetch = useCallback(async () => {
    setIsFetching(true);
    setErrorMessage(null);
    try {
      await fetchConfig(true);
      setFetchedAt(new Date().toISOString());
    } catch (error) {
      setErrorMessage(getErrorMessage(error) || 'Failed to load providers');
    } finally {
      setIsPending(false);
      setIsFetching(false);
    }
  }, [fetchConfig]);

  useEffect(() => {
    if (!connected || hasFetchedRef.current) return;
    hasFetchedRef.current = true;
    void refetch();
  }, [connected, refetch]);

  const snapshot = useMemo<ProviderSnapshot | null>(() => {
    if (!config) return null;
    const groups: ProviderGroup[] = PROVIDER_BRAND_ORDER.map((brand) => {
      if (brand === 'codex') {
        return { id: brand, resources: (config.codexApiKeys ?? []).map(codexToResource) };
      }
      if (brand === 'claude') {
        return { id: brand, resources: (config.claudeApiKeys ?? []).map(claudeToResource) };
      }
      return {
        id: brand,
        resources: (config.openaiCompatibility ?? []).map(openaiToResource),
      };
    });
    return { fetchedAt, groups };
  }, [config, fetchedAt]);

  const mutate = useCallback(
    async (operation: () => Promise<unknown>) => {
      setMutating(true);
      try {
        await operation();
        await refetch();
      } finally {
        setMutating(false);
      }
    },
    [refetch]
  );

  const createProvider = useCallback(
    (brand: ProviderBrand, input: ProviderEntryFormInput) =>
      mutate(() => {
        if (brand === 'codex') {
          return providersApi.createCodexConfig(buildProviderKeyConfig('codex', input));
        }
        if (brand === 'claude') {
          return providersApi.createClaudeConfig(buildProviderKeyConfig('claude', input));
        }
        return providersApi.createOpenAIProvider(buildOpenAIConfig(input));
      }),
    [mutate]
  );

  const updateProvider = useCallback(
    (resource: ProviderResource, input: ProviderEntryFormInput) =>
      mutate(() => {
        const selector = resource.selector;
        if (selector.brand === 'codex') {
          return providersApi.updateCodexConfig(
            selector.apiKey,
            selector.baseUrl,
            buildProviderKeyConfig('codex', input, resource.raw as ProviderKeyConfig)
          );
        }
        if (selector.brand === 'claude') {
          return providersApi.updateClaudeConfig(
            selector.apiKey,
            selector.baseUrl,
            buildProviderKeyConfig('claude', input, resource.raw as ProviderKeyConfig)
          );
        }
        return providersApi.updateOpenAIProvider(
          selector.name,
          selector.index,
          buildOpenAIConfig(input, resource.raw as OpenAIProviderConfig)
        );
      }),
    [mutate]
  );

  const deleteProvider = useCallback(
    (resource: ProviderResource) =>
      mutate(() => {
        const selector = resource.selector;
        if (selector.brand === 'codex') {
          return providersApi.deleteCodexConfig(selector.apiKey, selector.baseUrl);
        }
        if (selector.brand === 'claude') {
          return providersApi.deleteClaudeConfig(selector.apiKey, selector.baseUrl);
        }
        return providersApi.deleteOpenAIProvider(selector.name, selector.index);
      }),
    [mutate]
  );

  const toggleDisabled = useCallback(
    (resource: ProviderResource, disabled: boolean) =>
      mutate(() => {
        const selector = resource.selector;
        if (selector.brand === 'openaiCompatibility') {
          return providersApi.updateOpenAIProviderDisabled(selector.name, selector.index, disabled);
        }
        const current = resource.raw as ProviderKeyConfig;
        const next = {
          ...current,
          excludedModels: disabled
            ? withDisableAllModelsRule(current.excludedModels)
            : withoutDisableAllModelsRule(current.excludedModels),
        };
        return selector.brand === 'codex'
          ? providersApi.updateCodexConfig(selector.apiKey, selector.baseUrl, next)
          : providersApi.updateClaudeConfig(selector.apiKey, selector.baseUrl, next);
      }),
    [mutate]
  );

  return {
    connected,
    isPending,
    isFetching,
    isError: Boolean(errorMessage),
    errorMessage,
    snapshot,
    refetch,
    createProvider,
    updateProvider,
    deleteProvider,
    toggleDisabled,
    mutating,
    refreshSnapshot: () => setFetchedAt(new Date().toISOString()),
  };
}
