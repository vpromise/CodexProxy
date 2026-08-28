/**
 * 配置相关类型定义
 * 与基线 /config 返回结构保持一致（内部使用驼峰形式）
 */

import type { ProviderKeyConfig, OpenAIProviderConfig } from './provider';

export interface Config {
  debug?: boolean;
  proxyUrl?: string;
  requestRetry?: number;
  requestLog?: boolean;
  loggingToFile?: boolean;
  logsMaxTotalSizeMb?: number;
  forceModelPrefix?: boolean;
  routingStrategy?: string;
  apiKeys?: string[];
  codexApiKeys?: ProviderKeyConfig[];
  claudeApiKeys?: ProviderKeyConfig[];
  openaiCompatibility?: OpenAIProviderConfig[];
  oauthExcludedModels?: Record<string, string[]>;
  raw?: Record<string, unknown>;
}

export type RawConfigSection =
  | 'debug'
  | 'proxy-url'
  | 'request-retry'
  | 'request-log'
  | 'logging-to-file'
  | 'logs-max-total-size-mb'
  | 'force-model-prefix'
  | 'routing/strategy'
  | 'api-keys'
  | 'codex-api-key'
  | 'claude-api-key'
  | 'openai-compatibility'
  | 'oauth-excluded-models';
