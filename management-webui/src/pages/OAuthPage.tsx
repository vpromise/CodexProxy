import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { useNotificationStore, useThemeStore } from '@/stores';
import { oauthApi, type BuiltInOAuthProvider } from '@/services/api';
import { copyToClipboard } from '@/utils/clipboard';
import { getErrorMessage, isRecord } from '@/utils/helpers';
import { notifyAuthFilesChanged } from '@/features/authFiles/authFilesEvents';
import styles from './OAuthPage.module.scss';
import iconCodex from '@/assets/icons/codex.svg';
import iconClaude from '@/assets/icons/claude.svg';

interface ProviderState {
  url?: string;
  state?: string;
  status?: 'idle' | 'waiting' | 'success' | 'error';
  error?: string;
  polling?: boolean;
  callbackUrl?: string;
  callbackSubmitting?: boolean;
  callbackStatus?: 'success' | 'error';
  callbackError?: string;
}

interface BuiltInOAuthProviderCard {
  kind: 'builtin';
  id: BuiltInOAuthProvider;
  titleKey: string;
  icon: string | { light: string; dark: string };
}

type OAuthProviderCard = BuiltInOAuthProviderCard;

function getErrorStatus(error: unknown): number | undefined {
  if (!isRecord(error)) return undefined;
  return typeof error.status === 'number' ? error.status : undefined;
}

const PROVIDERS: BuiltInOAuthProviderCard[] = [
  {
    kind: 'builtin',
    id: 'codex',
    titleKey: 'auth_login.codex_oauth_title',
    icon: iconCodex,
  },
  {
    kind: 'builtin',
    id: 'anthropic',
    titleKey: 'auth_login.anthropic_oauth_title',
    icon: iconClaude,
  },
];

const CALLBACK_SUPPORTED = new Set<string>(['codex', 'anthropic']);
const SUCCESS_RESET_DELAY_MS = 5000;
const getProviderI18nPrefix = (provider: string) => provider.replace('-', '_');
const getAuthKey = (provider: string, suffix: string) =>
  `auth_login.${getProviderI18nPrefix(provider)}_${suffix}`;

const getIcon = (icon: string | { light: string; dark: string }, theme: 'light' | 'dark') => {
  return typeof icon === 'string' ? icon : icon[theme];
};

function OAuthProviderIcon({
  provider,
  theme,
}: {
  provider: OAuthProviderCard;
  theme: 'light' | 'dark';
}) {
  return <img src={getIcon(provider.icon, theme)} alt="" className={styles.cardTitleIcon} />;
}

export function OAuthPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { showNotification } = useNotificationStore();
  const resolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const [states, setStates] = useState<Record<string, ProviderState>>({});
  const pollingTimers = useRef<Partial<Record<string, number>>>({});
  const successResetTimers = useRef<Partial<Record<string, number>>>({});

  const clearTimers = useCallback(() => {
    Object.values(pollingTimers.current).forEach((timer) => {
      if (timer !== undefined) window.clearInterval(timer);
    });
    Object.values(successResetTimers.current).forEach((timer) => {
      if (timer !== undefined) window.clearTimeout(timer);
    });
    pollingTimers.current = {};
    successResetTimers.current = {};
  }, []);

  useEffect(() => {
    return () => {
      clearTimers();
    };
  }, [clearTimers]);

  const providerCards: OAuthProviderCard[] = PROVIDERS;

  const getProviderTitleText = (provider: OAuthProviderCard) => t(provider.titleKey);

  const getProviderText = (provider: OAuthProviderCard, suffix: string) =>
    t(getAuthKey(provider.id, suffix));

  const getProviderTextByID = (provider: string, suffix: string) => {
    const card = providerCards.find((item) => item.id === provider);
    return card ? getProviderText(card, suffix) : t(getAuthKey(provider, suffix));
  };

  const updateProviderState = (provider: string, next: Partial<ProviderState>) => {
    setStates((prev) => ({
      ...prev,
      [provider]: { ...(prev[provider] ?? {}), ...next },
    }));
  };

  const clearPollingTimer = (provider: string) => {
    const timer = pollingTimers.current[provider];
    if (timer !== undefined) {
      window.clearInterval(timer);
      delete pollingTimers.current[provider];
    }
  };

  const clearSuccessResetTimer = (provider: string) => {
    const timer = successResetTimers.current[provider];
    if (timer !== undefined) {
      window.clearTimeout(timer);
      delete successResetTimers.current[provider];
    }
  };

  const clearProviderTimers = (provider: string) => {
    clearPollingTimer(provider);
    clearSuccessResetTimer(provider);
  };

  const resetProviderAttempt = (provider: string) => {
    clearProviderTimers(provider);
    setStates((prev) => {
      return {
        ...prev,
        [provider]: {},
      };
    });
  };

  const completeProviderAuth = (provider: string) => {
    clearPollingTimer(provider);
    clearSuccessResetTimer(provider);
    notifyAuthFilesChanged();
    updateProviderState(provider, {
      url: undefined,
      state: undefined,
      status: 'success',
      error: undefined,
      polling: false,
      callbackUrl: '',
      callbackSubmitting: false,
      callbackStatus: undefined,
      callbackError: undefined,
    });
    successResetTimers.current[provider] = window.setTimeout(() => {
      resetProviderAttempt(provider);
    }, SUCCESS_RESET_DELAY_MS);
  };

  const startPolling = (provider: string, state: string) => {
    clearPollingTimer(provider);
    const timer = window.setInterval(async () => {
      try {
        const res = await oauthApi.getAuthStatus(state);
        if (res.status === 'ok') {
          completeProviderAuth(provider);
          showNotification(getProviderTextByID(provider, 'oauth_status_success'), 'success');
        } else if (res.status === 'error') {
          updateProviderState(provider, { status: 'error', error: res.error, polling: false });
          showNotification(
            `${getProviderTextByID(provider, 'oauth_status_error')} ${res.error || ''}`,
            'error'
          );
          window.clearInterval(timer);
          delete pollingTimers.current[provider];
        }
      } catch (err: unknown) {
        updateProviderState(provider, {
          status: 'error',
          error: getErrorMessage(err),
          polling: false,
        });
        window.clearInterval(timer);
        delete pollingTimers.current[provider];
      }
    }, 3000);
    pollingTimers.current[provider] = timer;
  };

  const startAuth = async (provider: string) => {
    clearProviderTimers(provider);
    updateProviderState(provider, {
      url: undefined,
      state: undefined,
      status: 'waiting',
      polling: true,
      error: undefined,
      callbackStatus: undefined,
      callbackError: undefined,
      callbackUrl: '',
    });
    try {
      const res = await oauthApi.startAuth(provider);
      if (!res.state) {
        const message = t('auth_login.missing_state');
        updateProviderState(provider, {
          url: res.url,
          state: undefined,
          status: 'error',
          error: message,
          polling: false,
        });
        showNotification(message, 'error');
        return;
      }
      updateProviderState(provider, {
        url: res.url,
        state: res.state,
        status: 'waiting',
        polling: true,
      });
      startPolling(provider, res.state);
    } catch (err: unknown) {
      const message = getErrorMessage(err);
      updateProviderState(provider, { status: 'error', error: message, polling: false });
      showNotification(
        `${getProviderTextByID(provider, 'oauth_start_error')}${message ? ` ${message}` : ''}`,
        'error'
      );
    }
  };

  const copyLink = async (url?: string) => {
    if (!url) return;
    const copied = await copyToClipboard(url);
    showNotification(
      t(copied ? 'notification.link_copied' : 'notification.copy_failed'),
      copied ? 'success' : 'error'
    );
  };

  const submitCallback = async (provider: string) => {
    const callbackInput = (states[provider]?.callbackUrl || '').trim();
    if (!callbackInput) {
      showNotification(t('auth_login.oauth_callback_required'), 'warning');
      return;
    }
    const redirectUrl = callbackInput;
    if (!redirectUrl) {
      showNotification(t('auth_login.missing_state'), 'warning');
      return;
    }
    updateProviderState(provider, {
      callbackSubmitting: true,
      callbackStatus: undefined,
      callbackError: undefined,
    });
    try {
      await oauthApi.submitCallback(provider, redirectUrl);
      updateProviderState(provider, { callbackSubmitting: false, callbackStatus: 'success' });
      showNotification(t('auth_login.oauth_callback_success'), 'success');
    } catch (err: unknown) {
      const status = getErrorStatus(err);
      const message = getErrorMessage(err);
      const errorMessage =
        status === 404
          ? t('auth_login.oauth_callback_upgrade_hint', {
              defaultValue: 'Please update CLI Proxy API or check the connection.',
            })
          : message || undefined;
      updateProviderState(provider, {
        callbackSubmitting: false,
        callbackStatus: 'error',
        callbackError: errorMessage,
      });
      const notificationMessage = errorMessage
        ? `${t('auth_login.oauth_callback_error')} ${errorMessage}`
        : t('auth_login.oauth_callback_error');
      showNotification(notificationMessage, 'error');
    }
  };

  const renderOAuthProviderCard = (provider: OAuthProviderCard) => {
    const state = states[provider.id] || {};
    const canSubmitCallback = CALLBACK_SUPPORTED.has(provider.id) && Boolean(state.url);
    const loginButtonLabel =
      state.status === 'success'
        ? t('auth_login.login_another_account')
        : getProviderText(provider, 'oauth_button');
    const statusBadgeClassName = [
      'status-badge',
      state.status === 'success' ? 'success' : '',
      state.status === 'error' ? 'error' : '',
    ]
      .filter(Boolean)
      .join(' ');

    return (
      <Card
        key={provider.id}
        title={
          <span className={styles.cardTitle}>
            <OAuthProviderIcon provider={provider} theme={resolvedTheme} />
            <span>{getProviderTitleText(provider)}</span>
          </span>
        }
        extra={
          <Button onClick={() => startAuth(provider.id)} loading={state.polling}>
            {loginButtonLabel}
          </Button>
        }
      >
        <div className={styles.cardContent}>
          <div className={styles.cardHint}>{getProviderText(provider, 'oauth_hint')}</div>
          {state.url && (
            <div className={styles.authUrlBox}>
              <div className={styles.authUrlLabel}>
                {getProviderText(provider, 'oauth_url_label')}
              </div>
              <div className={styles.authUrlValue}>{state.url}</div>
              <div className={styles.authUrlActions}>
                <Button variant="secondary" size="sm" onClick={() => copyLink(state.url!)}>
                  {getProviderText(provider, 'copy_link')}
                </Button>
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => window.open(state.url, '_blank', 'noopener,noreferrer')}
                >
                  {getProviderText(provider, 'open_link')}
                </Button>
              </div>
            </div>
          )}
          {canSubmitCallback && (
            <div className={styles.callbackSection}>
              <Input
                label={t('auth_login.oauth_callback_label')}
                hint={t('auth_login.oauth_callback_hint')}
                value={state.callbackUrl || ''}
                onChange={(e) =>
                  updateProviderState(provider.id, {
                    callbackUrl: e.target.value,
                    callbackStatus: undefined,
                    callbackError: undefined,
                  })
                }
                placeholder={t('auth_login.oauth_callback_placeholder')}
              />
              <div className={styles.callbackActions}>
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => submitCallback(provider.id)}
                  loading={state.callbackSubmitting}
                >
                  {t('auth_login.oauth_callback_button')}
                </Button>
              </div>
              {state.callbackStatus === 'success' && state.status === 'waiting' && (
                <div className="status-badge success">
                  {t('auth_login.oauth_callback_status_success')}
                </div>
              )}
              {state.callbackStatus === 'error' && (
                <div className="status-badge error">
                  {t('auth_login.oauth_callback_status_error')} {state.callbackError || ''}
                </div>
              )}
            </div>
          )}
          {state.status && state.status !== 'idle' && (
            <div className={statusBadgeClassName}>
              {state.status === 'success'
                ? getProviderText(provider, 'oauth_status_success')
                : state.status === 'error'
                  ? `${getProviderText(provider, 'oauth_status_error')} ${state.error || ''}`
                  : getProviderText(provider, 'oauth_status_waiting')}
            </div>
          )}
          {state.status === 'success' && (
            <div className={styles.successActions}>
              <Button variant="secondary" size="sm" onClick={() => navigate('/auth-files')}>
                {t('auth_login.view_auth_files')}
              </Button>
            </div>
          )}
        </div>
      </Card>
    );
  };

  return (
    <div className={styles.container}>
      <h1 className={styles.pageTitle}>{t('nav.oauth', { defaultValue: 'OAuth' })}</h1>

      <div className={styles.content}>
        <section className={styles.providerSection}>
          <div className={styles.providerList}>
            {providerCards.map((provider) => renderOAuthProviderCard(provider))}
          </div>
        </section>
      </div>
    </div>
  );
}
