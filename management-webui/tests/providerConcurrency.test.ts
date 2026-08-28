import { afterEach, describe, expect, test } from 'bun:test';
import { apiClient } from '../src/services/api/client';
import {
  appendLatestProviderRecord,
  providersApi,
  removeLatestProviderRecord,
  replaceLatestProviderRecord,
} from '../src/services/api/providers';

const originalGet = apiClient.get;
const originalPut = apiClient.put;

afterEach(() => {
  apiClient.get = originalGet;
  apiClient.put = originalPut;
});

const mergeRecord = (raw: unknown, payload: Record<string, unknown>) => ({
  ...(raw as Record<string, unknown> | undefined),
  ...payload,
});

describe('provider list concurrency', () => {
  test('preserves concurrent additions while appending a provider', () => {
    const latest = [
      { 'api-key': 'existing', custom: 'keep' },
      { 'api-key': 'concurrent', custom: 'also-keep' },
    ];

    expect(appendLatestProviderRecord(latest, { 'api-key': 'created' }, mergeRecord)).toEqual([
      { 'api-key': 'existing', custom: 'keep' },
      { 'api-key': 'concurrent', custom: 'also-keep' },
      { 'api-key': 'created' },
    ]);
  });

  test('replaces only the selected provider in the latest list', () => {
    const latest = [
      { 'api-key': 'existing', custom: 'keep' },
      { 'api-key': 'concurrent', custom: 'also-keep' },
    ];

    expect(
      replaceLatestProviderRecord(
        latest,
        (record) => record['api-key'] === 'existing',
        { 'api-key': 'updated' },
        mergeRecord
      )
    ).toEqual([
      { 'api-key': 'updated', custom: 'keep' },
      { 'api-key': 'concurrent', custom: 'also-keep' },
    ]);
  });

  test('removes only the selected provider from the latest list', () => {
    const latest = [{ name: 'target' }, { name: 'concurrent' }];
    expect(removeLatestProviderRecord(latest, (record) => record.name === 'target')).toEqual([
      { name: 'concurrent' },
    ]);
  });

  test('refuses a stale OpenAI-compatible index instead of mutating another provider', async () => {
    let writes = 0;
    apiClient.get = (async () => ({
      'openai-compatibility': [
        { name: 'concurrent', 'base-url': 'https://concurrent.example/v1' },
        { name: 'target', 'base-url': 'https://target.example/v1' },
      ],
    })) as typeof apiClient.get;
    apiClient.put = (async () => {
      writes += 1;
      return undefined;
    }) as typeof apiClient.put;

    for (const operation of [
      () => providersApi.updateOpenAIProviderDisabled('target', 0, true),
      () => providersApi.deleteOpenAIProvider('target', 0),
    ]) {
      let error: unknown;
      try {
        await operation();
      } catch (caught) {
        error = caught;
      }
      expect(error).toBeInstanceOf(Error);
      expect((error as Error).message).toContain('refresh and try again');
    }
    expect(writes).toBe(0);
  });
});
