export type Provider = 'codex' | 'claude';

export interface AccountPool {
  id: string;
  name: string;
  provider: Provider;
  accounts: string[];
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
}

export const providerName = (provider: Provider) => (provider === 'codex' ? 'Codex' : 'Claude');
