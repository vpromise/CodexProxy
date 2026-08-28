import { afterEach, describe, expect, test } from 'bun:test';
import { apiClient } from '../src/services/api/client';
import { providersApi } from '../src/services/api/providers';
import {
  normalizeOpenAIProvider,
  normalizeProviderKeyConfig,
} from '../src/services/api/transformers';

const originalGet = apiClient.get;
const originalPut = apiClient.put;

afterEach(() => {
  apiClient.get = originalGet;
  apiClient.put = originalPut;
});

describe('provider credential weight normalization', () => {
  test('reads weight for direct API key credentials', () => {
    expect(normalizeProviderKeyConfig({ 'api-key': 'claude-key', weight: 5 })?.weight).toBe(5);
    expect(normalizeProviderKeyConfig({ 'api-key': 'codex-key', weight: 0 })?.weight).toBe(0);
  });

  test('reads per-key weight for OpenAI-compatible providers', () => {
    const provider = normalizeOpenAIProvider({
      name: 'example',
      'base-url': 'https://example.com/v1',
      'api-key-entries': [{ 'api-key': 'key-a', weight: 3 }, { 'api-key': 'key-b' }],
    });

    expect(provider?.apiKeyEntries[0]?.weight).toBe(3);
    expect(provider?.apiKeyEntries[1]?.weight).toBeUndefined();
  });

  test('removes a cleared Codex weight while preserving unknown fields', async () => {
    let written: unknown;
    apiClient.get = (async () => ({
      'codex-api-key': [
        {
          'api-key': 'codex-key',
          'base-url': 'https://api.openai.com',
          weight: 9,
          'future-field': 'keep',
        },
      ],
    })) as typeof apiClient.get;
    apiClient.put = (async (_url: string, data?: unknown) => {
      written = data;
      return undefined;
    }) as typeof apiClient.put;

    await providersApi.updateCodexConfig('codex-key', 'https://api.openai.com', {
      apiKey: 'codex-key',
      baseUrl: 'https://api.openai.com',
      weight: undefined,
    });

    expect(written).toEqual([
      {
        'api-key': 'codex-key',
        'base-url': 'https://api.openai.com',
        'future-field': 'keep',
      },
    ]);
  });

  test('writes and clears nested OpenAI-compatible key weights', async () => {
    let written: unknown;
    apiClient.get = (async () => ({
      'openai-compatibility': [
        {
          name: 'example',
          'base-url': 'https://example.com/v1',
          'api-key-entries': [
            { 'api-key': 'key-a', weight: 8, custom: 'keep-a' },
            { 'api-key': 'key-b', custom: 'keep-b' },
          ],
        },
      ],
    })) as typeof apiClient.get;
    apiClient.put = (async (_url: string, data?: unknown) => {
      written = data;
      return undefined;
    }) as typeof apiClient.put;

    await providersApi.updateOpenAIProvider('example', 0, {
      name: 'example',
      baseUrl: 'https://example.com/v1',
      apiKeyEntries: [
        { apiKey: 'key-a', weight: undefined },
        { apiKey: 'key-b', weight: 4 },
      ],
    });

    expect(written).toEqual([
      {
        name: 'example',
        'base-url': 'https://example.com/v1',
        'api-key-entries': [
          { 'api-key': 'key-a', custom: 'keep-a' },
          { 'api-key': 'key-b', custom: 'keep-b', weight: 4 },
        ],
      },
    ]);
  });

  test('preserves explicit false cooling overrides during unrelated edits', async () => {
    const writes: unknown[] = [];
    apiClient.get = (async (url: string) =>
      url === '/config'
        ? {
            'claude-api-key': [
              {
                'api-key': 'claude-key',
                'base-url': 'https://api.anthropic.com',
                'disable-cooling': false,
              },
            ],
            'openai-compatibility': [
              {
                name: 'example',
                'base-url': 'https://example.com/v1',
                'api-key-entries': [{ 'api-key': 'key-a' }],
                'disable-cooling': false,
              },
            ],
          }
        : {}) as typeof apiClient.get;
    apiClient.put = (async (_url: string, data?: unknown) => {
      writes.push(data);
      return undefined;
    }) as typeof apiClient.put;

    await providersApi.updateClaudeConfig('claude-key', 'https://api.anthropic.com', {
      apiKey: 'claude-key',
      baseUrl: 'https://api.anthropic.com',
      disableCooling: false,
      priority: 3,
    });
    await providersApi.updateOpenAIProvider('example', 0, {
      name: 'example',
      baseUrl: 'https://example.com/v1',
      apiKeyEntries: [{ apiKey: 'key-a' }],
      disableCooling: false,
      priority: 4,
    });

    expect(writes).toHaveLength(2);
    expect(writes[0]).toEqual([
      {
        'api-key': 'claude-key',
        'base-url': 'https://api.anthropic.com',
        'disable-cooling': false,
        priority: 3,
      },
    ]);
    expect(writes[1]).toEqual([
      {
        name: 'example',
        'base-url': 'https://example.com/v1',
        'api-key-entries': [{ 'api-key': 'key-a' }],
        'disable-cooling': false,
        priority: 4,
      },
    ]);
  });
});
