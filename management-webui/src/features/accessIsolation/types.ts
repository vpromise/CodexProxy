export type Provider = 'codex' | 'claude';

export interface AccountPool {
  id: string;
  name: string;
  provider: Provider;
  accounts: string[];
  egress?: { mode: 'inherit' | 'direct-ip'; 'source-ip'?: string };
}

export interface KeyBinding {
  key: string;
  name: string;
  note: string;
  pool: string;
}

export interface IsolationSettings {
  enabled: boolean;
  pools: AccountPool[];
  keys: KeyBinding[];
}

export interface PoolAccount {
  id: string;
  provider: Provider;
  label: string;
  disabled: boolean;
  api_key_preview?: string;
  auth_index?: string;
  prefix?: string;
}

export interface IsolationView {
  revision: string;
  settings: IsolationSettings;
  accounts: PoolAccount[];
  'api-keys': string[];
  'egress-addresses'?: Array<{ ip: string; interface: string }>;
}

export const validPoolEgress = (pool: AccountPool): boolean => {
  if (!pool.egress || pool.egress.mode === 'inherit') return !pool.egress?.['source-ip'];
  if (pool.egress.mode !== 'direct-ip') return false;
  const ip = pool.egress['source-ip'] ?? '';
  const parts = ip.split('.');
  if (
    parts.length !== 4 ||
    parts.some((part) => !/^\d+$/.test(part) || String(Number(part)) !== part || Number(part) > 255)
  )
    return false;
  return (
    ip !== '0.0.0.0' &&
    ip !== '255.255.255.255' &&
    (Number(parts[0]) < 224 || Number(parts[0]) > 239)
  );
};

export const providerName = (provider: Provider) => (provider === 'codex' ? 'Codex' : 'Claude');
