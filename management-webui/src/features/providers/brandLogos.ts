import claudeLogo from '@/assets/icons/claude.svg';
import codexLogo from '@/assets/icons/codex.svg';
import openaiLightLogo from '@/assets/icons/openai-light.svg';
import openaiDarkLogo from '@/assets/icons/openai-dark.svg';
import type { ProviderBrand } from './types';

export interface ProviderBrandLogo {
  src: string;
  darkSrc?: string;
  transparent?: boolean;
  themeSurface?: boolean;
  invertOnDark?: boolean;
}

export const PROVIDER_LOGOS: Record<ProviderBrand, ProviderBrandLogo> = {
  codex: { src: codexLogo },
  claude: { src: claudeLogo },
  openaiCompatibility: {
    src: openaiLightLogo,
    darkSrc: openaiDarkLogo,
    transparent: true,
  },
};
