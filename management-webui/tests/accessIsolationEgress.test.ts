import { describe, expect, test } from 'bun:test';
import { validPoolEgress, type AccountPool } from '../src/features/accessIsolation/types';

const pool: AccountPool = { id: 'one', name: 'One', provider: 'codex', accounts: [] };

describe('pool IPv4 egress', () => {
  test('legacy pools inherit their existing network settings', () => {
    expect(validPoolEgress(pool)).toBe(true);
    expect(validPoolEgress({ ...pool, egress: { mode: 'inherit' } })).toBe(true);
  });

  test('a direct route requires a specific IPv4', () => {
    for (const ip of ['203.0.113.10', '10.0.0.2', '127.0.0.1']) {
      expect(validPoolEgress({ ...pool, egress: { mode: 'direct-ip', 'source-ip': ip } })).toBe(true);
    }
    for (const ip of ['', '0.0.0.0', '::1', 'hostname', '224.1.2.3', '255.255.255.255', '1.2.3.999', '01.2.3.4']) {
      expect(validPoolEgress({ ...pool, egress: { mode: 'direct-ip', 'source-ip': ip } })).toBe(false);
    }
  });

  test('inherit cannot silently retain a source binding', () => {
    expect(validPoolEgress({ ...pool, egress: { mode: 'inherit', 'source-ip': '203.0.113.10' } })).toBe(false);
  });
});
