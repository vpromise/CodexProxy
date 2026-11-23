import { apiClient } from './client';

export interface SelfServiceRecord {
  id: string;
  provider: 'claude' | 'codex';
  label: string;
  status: string;
  created_at: string;
  expires_at: string;
  submitted_at?: string;
  reviewed_at?: string;
  email?: string;
  account_id?: string;
  file_name?: string;
}

export const selfServiceOAuthApi = {
  list: () => apiClient.get<{ records: SelfServiceRecord[] }>('/oauth-intake'),
  create: (provider: 'claude' | 'codex', label: string) =>
    apiClient.post<{ record: SelfServiceRecord; invitation_token: string }>('/oauth-intake', {
      provider,
      label,
      ttl_hours: 24,
    }),
  accept: (id: string) =>
    apiClient.post<{ record: SelfServiceRecord }>(`/oauth-intake/${encodeURIComponent(id)}/accept`),
  reject: (id: string) =>
    apiClient.post<{ record: SelfServiceRecord }>(`/oauth-intake/${encodeURIComponent(id)}/reject`),
  remove: (id: string) =>
    apiClient.delete<{ deleted: boolean }>(`/oauth-intake/${encodeURIComponent(id)}`),
};
