import logo from './assets/brand/codexproxy-logo.webp';

export const BRAND_NAME = 'CodexProxy';
export const BRAND_LOGO = logo;

const repository = 'https://github.com/vpromise/CodexProxy';

export const BRAND_LINKS = {
  repository,
  documentation: `${repository}#readme`,
  issues: `${repository}/issues`,
  releases: `${repository}/releases`,
  upstream: 'https://github.com/router-for-me/CLIProxyAPI',
} as const;
