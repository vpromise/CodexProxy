const MANAGEMENT_OAUTH_PROVIDER_PATTERN = /^[a-z0-9-]+$/;

export const normalizeOAuthProviderKey = (value: string): string => {
  return value.trim().toLowerCase().replace(/_/g, '-');
};

export const normalizeManagementOAuthProviderKey = (value: string): string =>
  value.trim().toLowerCase();

export const isManagementOAuthProviderKey = (value: string): boolean =>
  MANAGEMENT_OAUTH_PROVIDER_PATTERN.test(value);
