/**
 * Configuration API
 */

import { apiClient } from './client';
import type { Config } from '@/types';
import { normalizeConfigResponse } from './transformers';

export const configApi = {
  /**
   * Fetch and normalize configuration.
   */
  async getConfig(): Promise<Config> {
    const raw = await apiClient.get('/config');
    return normalizeConfigResponse(raw);
  },

  /**
   * Toggle full request logging.
   */
  updateLoggingToFile: (enabled: boolean) => apiClient.put('/logging-to-file', { value: enabled }),

  updateRequestLog: (enabled: boolean) => apiClient.put('/request-log', { value: enabled }),
};
