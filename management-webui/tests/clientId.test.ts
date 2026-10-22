import { afterEach, describe, expect, test } from 'bun:test';
import { makeClientId } from '../src/types/visualConfig';

const originalCrypto = Object.getOwnPropertyDescriptor(globalThis, 'crypto');

afterEach(() => {
  if (originalCrypto) {
    Object.defineProperty(globalThis, 'crypto', originalCrypto);
  } else {
    Reflect.deleteProperty(globalThis, 'crypto');
  }
});

describe('client IDs for editable configuration rows and account pools', () => {
  test('creates distinct IDs when HTTP does not expose randomUUID', () => {
    Object.defineProperty(globalThis, 'crypto', { configurable: true, value: {} });
    const ids = Array.from({ length: 20 }, () => makeClientId());
    expect(ids.every((id) => id.length > 0)).toBe(true);
    expect(new Set(ids).size).toBe(ids.length);
  });

  test('supports environments without crypto', () => {
    Object.defineProperty(globalThis, 'crypto', { configurable: true, value: undefined });
    expect(makeClientId()).not.toBe('');
  });

  test('uses randomUUID when available', () => {
    Object.defineProperty(globalThis, 'crypto', {
      configurable: true,
      value: { randomUUID: () => 'synthetic-pool-uuid' },
    });
    expect(makeClientId()).toBe('synthetic-pool-uuid');
  });
});
