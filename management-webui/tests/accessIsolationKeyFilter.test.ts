import { describe, expect, test } from 'bun:test';
import {
  getVisibleBindingKeys,
  initialKeyBindingFilter,
  keyBindingFilterReducer,
} from '../src/features/accessIsolation/keyBindingFilter';
import type { IsolationSettings } from '../src/features/accessIsolation/types';

const apiKeys = ['synthetic-key-0001', 'synthetic-key-0002'];
const makeSettings = (): IsolationSettings => ({
  enabled: true,
  pools: [{ id: 'pool-one', name: 'Codex pool', provider: 'codex', accounts: [] }],
  keys: [
    { key: apiKeys[0], name: 'Alice', note: 'Original note', pool: '' },
    { key: apiKeys[1], name: 'Carol', note: '', pool: 'pool-one' },
    { key: 'synthetic-revoked-0003', name: 'Revoked user', note: '', pool: '' },
  ],
});

describe('account isolation key filter editing', () => {
  test('keeps a matching name row visible through replacement and then reapplies a new search', () => {
    const settings = makeSettings();
    let filter = keyBindingFilterReducer(initialKeyBindingFilter, {
      type: 'search',
      value: 'Alice',
    });
    expect(getVisibleBindingKeys(settings, apiKeys, filter)).toEqual([apiKeys[0]]);

    filter = keyBindingFilterReducer(filter, { type: 'edit', key: apiKeys[0] });
    for (const name of ['B', 'Bo', 'Bob']) {
      settings.keys[0].name = name;
      expect(getVisibleBindingKeys(settings, apiKeys, filter)).toEqual([apiKeys[0]]);
    }

    filter = keyBindingFilterReducer(filter, { type: 'search', value: 'Carol' });
    expect(getVisibleBindingKeys(settings, apiKeys, filter)).toEqual([apiKeys[1]]);
  });

  test('keeps note edits visible even after they stop matching the search', () => {
    const settings = makeSettings();
    let filter = keyBindingFilterReducer(initialKeyBindingFilter, {
      type: 'search',
      value: 'original',
    });
    expect(getVisibleBindingKeys(settings, apiKeys, filter)).toEqual([apiKeys[0]]);
    filter = keyBindingFilterReducer(filter, { type: 'edit', key: apiKeys[0] });
    settings.keys[0].note = '';
    expect(getVisibleBindingKeys(settings, apiKeys, filter)).toEqual([apiKeys[0]]);
  });

  test('keeps a newly assigned row in the unassigned view until the filter changes', () => {
    const settings = makeSettings();
    let filter = keyBindingFilterReducer(initialKeyBindingFilter, {
      type: 'filter',
      value: 'unassigned',
    });
    expect(getVisibleBindingKeys(settings, apiKeys, filter)).toEqual([apiKeys[0]]);
    filter = keyBindingFilterReducer(filter, { type: 'edit', key: apiKeys[0] });
    settings.keys[0].pool = 'pool-one';
    expect(getVisibleBindingKeys(settings, apiKeys, filter)).toEqual([apiKeys[0]]);

    filter = keyBindingFilterReducer(filter, { type: 'filter', value: 'revoked' });
    expect(getVisibleBindingKeys(settings, apiKeys, filter)).toEqual(['synthetic-revoked-0003']);
    filter = keyBindingFilterReducer(filter, { type: 'filter', value: 'unassigned' });
    expect(getVisibleBindingKeys(settings, apiKeys, filter)).toEqual([]);
  });

  test('keeps a pool-name match visible while changing pools without retaining unrelated rows', () => {
    const settings = makeSettings();
    let filter = keyBindingFilterReducer(initialKeyBindingFilter, {
      type: 'search',
      value: 'codex',
    });
    expect(getVisibleBindingKeys(settings, apiKeys, filter)).toEqual([apiKeys[1]]);
    filter = keyBindingFilterReducer(filter, { type: 'edit', key: apiKeys[1] });
    settings.keys[1].pool = '';
    expect(getVisibleBindingKeys(settings, apiKeys, filter)).toEqual([apiKeys[1]]);

    settings.keys = settings.keys.filter((binding) => binding.key !== apiKeys[1]);
    expect(getVisibleBindingKeys(settings, [apiKeys[0]], filter)).toEqual([]);
  });
});
