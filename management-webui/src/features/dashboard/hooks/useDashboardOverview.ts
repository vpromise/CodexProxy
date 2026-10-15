import { useCallback, useEffect, useMemo, useState } from 'react';
import { authFilesApi } from '@/services/api';
import { useAuthStore, useConfigStore, useModelsStore } from '@/stores';
import { useProviderRecentRequests } from '@/components/providers/hooks/useProviderRecentRequests';
import {
  mergeRecentRequestBucketGroups,
  normalizeRecentRequestUsageEntry,
  type RecentRequestBucket,
} from '@/utils/recentRequests';
import type { Config } from '@/types';
import type { AuthFileItem } from '@/types/authFile';
import {
  TRAFFIC_BUCKET_MINUTES,
  type CredentialHealth,
  type DashboardCounts,
  type ProviderTraffic,
  type TrafficWindow,
} from '../types';

const EMPTY_TRAFFIC: TrafficWindow = {
  buckets: [],
  totalSuccess: 0,
  totalFailure: 0,
  total: 0,
  successRate: null,
  peakTotal: 0,
  peakIndex: -1,
  activeBuckets: 0,
  windowMinutes: 0,
};

/** Extract the key after the first separator in `<baseUrl>|<apiKey>`. */
const apiKeyFromCompositeKey = (compositeKey: string): string => {
  const separatorIndex = compositeKey.indexOf('|');
  return separatorIndex < 0 ? '' : compositeKey.slice(separatorIndex + 1).trim();
};

const providerIdOfAuthFile = (file: AuthFileItem): string => {
  const candidate = String(file.type ?? file.provider ?? '')
    .trim()
    .toLowerCase();
  return candidate && candidate !== 'empty' ? candidate : 'unknown';
};

const buildTrafficWindow = (bucketGroups: RecentRequestBucket[][]): TrafficWindow => {
  const buckets = mergeRecentRequestBucketGroups(bucketGroups);
  if (buckets.length === 0) {
    return EMPTY_TRAFFIC;
  }

  let totalSuccess = 0;
  let totalFailure = 0;
  let peakTotal = 0;
  let peakIndex = -1;
  let activeBuckets = 0;

  buckets.forEach((bucket, index) => {
    const bucketTotal = bucket.success + bucket.failed;
    totalSuccess += bucket.success;
    totalFailure += bucket.failed;
    if (bucketTotal > 0) {
      activeBuckets += 1;
    }
    if (bucketTotal > peakTotal) {
      peakTotal = bucketTotal;
      peakIndex = index;
    }
  });

  const total = totalSuccess + totalFailure;

  return {
    buckets,
    totalSuccess,
    totalFailure,
    total,
    successRate: total > 0 ? (totalSuccess / total) * 100 : null,
    peakTotal,
    peakIndex,
    activeBuckets,
    windowMinutes: buckets.length * TRAFFIC_BUCKET_MINUTES,
  };
};

interface ProviderAccumulator {
  credentials: number;
  success: number;
  failure: number;
  bucketGroups: RecentRequestBucket[][];
}

const createAccumulator = (): ProviderAccumulator => ({
  credentials: 0,
  success: 0,
  failure: 0,
  bucketGroups: [],
});

export const getProviderKeyCounts = (config: Config) => ({
  codex: config.codexApiKeys?.length ?? 0,
  claude: config.claudeApiKeys?.length ?? 0,
  openai: config.openaiCompatibility?.length ?? 0,
});

/**
 * Aggregate dashboard data from configured API keys and auth-file credentials.
 * The sources are deduplicated defensively by account type and account.
 */
export function useDashboardOverview() {
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const apiBase = useAuthStore((state) => state.apiBase);
  const config = useConfigStore((state) => state.config);
  const fetchConfig = useConfigStore((state) => state.fetchConfig);

  const models = useModelsStore((state) => state.models);
  const modelsLoading = useModelsStore((state) => state.loading);
  const modelsError = useModelsStore((state) => state.error);
  const fetchModelsFromStore = useModelsStore((state) => state.fetchModels);

  const connected = connectionStatus === 'connected';

  const { usageByProvider, refreshRecentRequests } = useProviderRecentRequests({
    enabled: connected,
  });

  const [authFiles, setAuthFiles] = useState<AuthFileItem[] | null>(null);
  const [authFilesLoading, setAuthFilesLoading] = useState(false);

  const loadAuthFiles = useCallback(async () => {
    if (!connected) return;
    setAuthFilesLoading(true);
    try {
      const response = await authFilesApi.list();
      setAuthFiles(response.files);
    } catch {
      setAuthFiles(null);
    } finally {
      setAuthFilesLoading(false);
    }
  }, [connected]);

  const loadModels = useCallback(async () => {
    if (!connected || !apiBase) return;
    try {
      await fetchModelsFromStore(apiBase);
    } catch {
      // Catalog failures must not block the rest of the dashboard.
    }
  }, [connected, apiBase, fetchModelsFromStore]);

  useEffect(() => {
    if (!connected) return;
    void fetchConfig().catch(() => undefined);
    void loadAuthFiles();
    void loadModels();
  }, [connected, fetchConfig, loadAuthFiles, loadModels]);

  const refresh = useCallback(async () => {
    if (!connected) return;
    await Promise.allSettled([
      fetchConfig(true),
      loadAuthFiles(),
      loadModels(),
      refreshRecentRequests(),
    ]);
  }, [connected, fetchConfig, loadAuthFiles, loadModels, refreshRecentRequests]);

  const providerKeyCounts = useMemo(() => (config ? getProviderKeyCounts(config) : null), [config]);

  const { traffic, providers } = useMemo(() => {
    const accumulators = new Map<string, ProviderAccumulator>();
    const allBucketGroups: RecentRequestBucket[][] = [];
    const apiKeysFromUsage = new Set<string>();

    const accumulatorFor = (providerId: string): ProviderAccumulator => {
      const existing = accumulators.get(providerId);
      if (existing) return existing;
      const created = createAccumulator();
      accumulators.set(providerId, created);
      return created;
    };

    usageByProvider.forEach((entriesByKey, providerId) => {
      const accumulator = accumulatorFor(providerId);
      entriesByKey.forEach((entry, compositeKey) => {
        const apiKey = apiKeyFromCompositeKey(compositeKey);
        if (apiKey) {
          apiKeysFromUsage.add(apiKey);
        }
        accumulator.credentials += 1;
        accumulator.success += entry.success;
        accumulator.failure += entry.failed;
        if (entry.recentRequests.length > 0) {
          accumulator.bucketGroups.push(entry.recentRequests);
          allBucketGroups.push(entry.recentRequests);
        }
      });
    });

    (authFiles ?? []).forEach((file) => {
      const accountType = String(file.account_type ?? '')
        .trim()
        .toLowerCase();
      const account = String(file.account ?? '').trim();
      // Do not count credentials already included in api-key-usage twice.
      if (accountType === 'api_key' && account && apiKeysFromUsage.has(account)) {
        return;
      }

      const accumulator = accumulatorFor(providerIdOfAuthFile(file));
      const entry = normalizeRecentRequestUsageEntry(file);
      accumulator.credentials += 1;
      accumulator.success += entry.success;
      accumulator.failure += entry.failed;
      if (entry.recentRequests.length > 0) {
        accumulator.bucketGroups.push(entry.recentRequests);
        allBucketGroups.push(entry.recentRequests);
      }
    });

    const providerRows: ProviderTraffic[] = Array.from(accumulators.entries())
      .map(([id, accumulator]) => {
        const total = accumulator.success + accumulator.failure;
        return {
          id,
          credentials: accumulator.credentials,
          success: accumulator.success,
          failure: accumulator.failure,
          total,
          successRate: total > 0 ? (accumulator.success / total) * 100 : null,
          buckets: mergeRecentRequestBucketGroups(accumulator.bucketGroups),
        };
      })
      .sort(
        (a, b) => b.total - a.total || b.credentials - a.credentials || a.id.localeCompare(b.id)
      );

    return {
      traffic: buildTrafficWindow(allBucketGroups),
      providers: providerRows,
    };
  }, [usageByProvider, authFiles]);

  const credentials = useMemo<CredentialHealth | null>(() => {
    if (!authFiles) return null;

    let disabled = 0;
    let unavailable = 0;
    const countsByType = new Map<string, number>();

    authFiles.forEach((file) => {
      if (file.disabled) {
        disabled += 1;
      } else if (file.unavailable) {
        unavailable += 1;
      }
      const type = providerIdOfAuthFile(file);
      countsByType.set(type, (countsByType.get(type) ?? 0) + 1);
    });

    return {
      total: authFiles.length,
      active: authFiles.length - disabled - unavailable,
      disabled,
      unavailable,
      byType: Array.from(countsByType.entries())
        .map(([type, count]) => ({ type, count }))
        .sort((a, b) => b.count - a.count || a.type.localeCompare(b.type)),
    };
  }, [authFiles]);

  const counts = useMemo<DashboardCounts>(
    () => ({
      managementKeys: config ? (config.apiKeys?.length ?? 0) : null,
      providerKeys: providerKeyCounts
        ? Object.values(providerKeyCounts).reduce((sum, count) => sum + count, 0)
        : null,
      credentials: authFiles ? authFiles.length : null,
      models: modelsLoading || modelsError ? null : models.length,
    }),
    [config, providerKeyCounts, authFiles, models.length, modelsLoading, modelsError]
  );

  return {
    connectionStatus,
    connected,
    config,
    counts,
    providerKeyCounts,
    traffic,
    providers,
    credentials,
    /** Show the initial skeleton while both configuration and credentials are pending. */
    initialLoading: connected && !config && authFiles === null,
    authFilesLoading,
    refresh,
  };
}
