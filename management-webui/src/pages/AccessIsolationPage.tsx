import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { usePageTransitionLayer } from '@/components/common/PageTransitionLayer';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { EmptyState } from '@/components/ui/EmptyState';
import { Input } from '@/components/ui/Input';
import { LoadingSpinner } from '@/components/ui/LoadingSpinner';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import {
  IconAlertTriangle,
  IconCheck,
  IconPlus,
  IconRefreshCw,
  IconShield,
} from '@/components/ui/icons';
import { PoolEditor } from '@/features/accessIsolation/PoolEditor';
import { KeyBindings } from '@/features/accessIsolation/KeyBindings';
import {
  providerName,
  type AccountPool,
  type IsolationSettings,
  type IsolationView,
  type KeyBinding,
} from '@/features/accessIsolation/types';
import { FloatingSaveBar } from '@/features/config/components/FloatingSaveBar';
import { useUnsavedChangesGuard } from '@/hooks/useUnsavedChangesGuard';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { apiClient } from '@/services/api/client';
import { useNotificationStore } from '@/stores';
import type { ApiError } from '@/types';
import { makeClientId } from '@/types/visualConfig';
import codexIcon from '@/assets/icons/codex.svg';
import claudeIcon from '@/assets/icons/claude.svg';
import styles from './AccessIsolationPage.module.scss';

export function AccessIsolationPage() {
  const { t } = useTranslation();
  const showConfirmation = useNotificationStore((state) => state.showConfirmation);
  const layer = usePageTransitionLayer();
  const isCurrentLayer = layer?.isCurrentLayer ?? true;
  const [view, setView] = useState<IsolationView | null>(null);
  const [settings, setSettings] = useState<IsolationSettings>({
    enabled: false,
    pools: [],
    keys: [],
  });
  const [selectedId, setSelectedId] = useState('');
  const [poolSearch, setPoolSearch] = useState('');
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [saved, setSaved] = useState(false);
  const [needsReload, setNeedsReload] = useState(false);
  const requestId = useRef(0);
  const dirty = view !== null && JSON.stringify(view.settings) !== JSON.stringify(settings);
  const busy = loading || saving;
  const disabled = busy || needsReload;
  const invalid = settings.pools.some((pool) => !pool.name.trim());
  const canSave = view !== null && dirty && !disabled && !invalid;
  const selectedPool = settings.pools.find((pool) => pool.id === selectedId) ?? settings.pools[0];

  useUnsavedChangesGuard({
    enabled: isCurrentLayer,
    shouldBlock: dirty,
    dialog: {
      title: t('common.unsaved_changes_title'),
      message: t('common.unsaved_changes_message'),
      confirmText: t('common.confirm'),
      cancelText: t('common.cancel'),
    },
  });
  useEffect(() => {
    if (!dirty || !isCurrentLayer) return;
    const beforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = '';
    };
    window.addEventListener('beforeunload', beforeUnload);
    return () => window.removeEventListener('beforeunload', beforeUnload);
  }, [dirty, isCurrentLayer]);

  const load = useCallback(async () => {
    const id = ++requestId.current;
    const result = await apiClient.get<IsolationView>('/access-isolation');
    if (id !== requestId.current) return;
    const normalized = {
      ...result,
      accounts: result.accounts ?? [],
      'api-keys': result['api-keys'] ?? [],
      settings: {
        ...result.settings,
        pools: (result.settings.pools ?? []).map((pool) => ({
          ...pool,
          accounts: pool.accounts ?? [],
        })),
        keys: result.settings.keys ?? [],
      },
    };
    setView(normalized);
    setSettings(normalized.settings);
    setNeedsReload(false);
  }, []);

  const refresh = useCallback(async () => {
    setLoading(true);
    setError('');
    setSaved(false);
    try {
      await load();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }, [load]);

  useEffect(() => {
    void refresh();
    return () => {
      requestId.current += 1;
    };
  }, [refresh]);

  const reload = useCallback(() => {
    if (busy) return;
    if (!dirty) {
      void refresh();
      return;
    }
    showConfirmation({
      title: t('common.unsaved_changes_title'),
      message: t('config_management.reload_confirm_message'),
      confirmText: t('isolation.reload'),
      cancelText: t('common.cancel'),
      onConfirm: refresh,
    });
  }, [busy, dirty, refresh, showConfirmation, t]);
  useHeaderRefresh(reload, isCurrentLayer);

  const change = (next: IsolationSettings) => {
    setSettings(next);
    setSaved(false);
  };
  const updatePool = (id: string, patch: Partial<AccountPool>) =>
    change({
      ...settings,
      pools: settings.pools.map((pool) => (pool.id === id ? { ...pool, ...patch } : pool)),
    });
  const updateBinding = (key: string, patch: Partial<KeyBinding>) => {
    const exists = settings.keys.some((binding) => binding.key === key);
    const keys = exists
      ? settings.keys.map((binding) => (binding.key === key ? { ...binding, ...patch } : binding))
      : [...settings.keys, { key, name: '', note: '', pool: '', ...patch }];
    change({ ...settings, keys });
  };
  const addPool = () => {
    const id = makeClientId();
    change({
      ...settings,
      pools: [
        ...settings.pools,
        { id, name: t('isolation.newPool'), provider: 'codex', accounts: [] },
      ],
    });
    setSelectedId(id);
    setPoolSearch('');
  };
  const save = async () => {
    if (!canSave || !view) return;
    setSaving(true);
    setError('');
    setSaved(false);
    let persisted = false;
    try {
      await apiClient.put('/access-isolation', { revision: view.revision, settings });
      persisted = true;
      await load();
      setSaved(true);
    } catch (err: unknown) {
      if (persisted) {
        setNeedsReload(true);
        setError(t('isolation.savedReloadRequired'));
      } else if ((err as ApiError)?.status === 409) {
        setError(t('isolation.conflict'));
      } else {
        setError(err instanceof Error ? err.message : String(err));
      }
    } finally {
      setSaving(false);
    }
  };
  const discard = () =>
    showConfirmation({
      title: t('common.unsaved_changes_title'),
      message: t('config_management.discard_confirm_message'),
      confirmText: t('config_management.actions.discard'),
      cancelText: t('common.cancel'),
      onConfirm: () => {
        if (view) setSettings(view.settings);
        setError('');
        setSaved(false);
      },
    });

  const unassigned =
    view?.['api-keys'].filter((key) => !settings.keys.find((binding) => binding.key === key)?.pool)
      .length ?? 0;
  const empty = settings.pools.filter((pool) => pool.accounts.length === 0).length;
  const visiblePools = settings.pools.filter((pool) =>
    `${pool.name} ${providerName(pool.provider)}`
      .toLocaleLowerCase()
      .includes(poolSearch.trim().toLocaleLowerCase())
  );

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <div>
          <h1>{t('isolation.title')}</h1>
          <p className={styles.description}>{t('isolation.description')}</p>
        </div>
        <div className={styles.headerActions}>
          <Button variant="ghost" disabled={busy} onClick={reload}>
            <span className={styles.buttonContent}>
              <IconRefreshCw size={15} />
              {t('isolation.reload')}
            </span>
          </Button>
          <Button
            className={styles.primaryAction}
            disabled={!canSave}
            loading={saving}
            onClick={save}
          >
            <span className={styles.buttonContent}>
              {!saving && <IconCheck size={15} />}
              {t('isolation.save')}
            </span>
          </Button>
        </div>
      </header>
      {error && (
        <div role="alert" className={styles.error}>
          <IconAlertTriangle size={18} />
          <span>{error}</span>
        </div>
      )}
      {saved && !dirty && (
        <p role="status" className={styles.saved}>
          <IconCheck size={16} />
          {t('isolation.saved')}
        </p>
      )}
      {!view ? (
        <div className={styles.loading} role="status">
          {loading ? (
            <>
              <LoadingSpinner size={22} />
              {t('common.loading')}
            </>
          ) : (
            <Button variant="secondary" onClick={reload}>
              {t('isolation.reload')}
            </Button>
          )}
        </div>
      ) : (
        <>
          <Card className={styles.activationCard}>
            <div className={styles.activationHeader}>
              <span className={styles.shield}>
                <IconShield size={22} />
              </span>
              <div className={styles.activationCopy}>
                <div className={styles.activationTitle}>
                  <h2>{t('isolation.enable')}</h2>
                  <span className={styles.badge}>
                    {t(
                      view.settings.enabled ? 'isolation.statusEnabled' : 'isolation.statusDisabled'
                    )}
                  </span>
                </div>
                <p>{t('isolation.noFallback')}</p>
              </div>
              <ToggleSwitch
                checked={settings.enabled}
                onChange={(enabled) => change({ ...settings, enabled })}
                disabled={disabled}
                ariaLabel={t('isolation.enable')}
              />
            </div>
            <div className={styles.activationFooter}>
              <span>{t('isolation.poolCount', { count: settings.pools.length })}</span>
              <span>
                {t('isolation.boundKeys', {
                  count: view['api-keys'].length - unassigned,
                  total: view['api-keys'].length,
                })}
              </span>
              {(unassigned > 0 || empty > 0) && (
                <span className={styles.attention}>
                  <IconAlertTriangle size={14} />
                  {t('isolation.preflight', { unassigned, empty })}
                </span>
              )}
            </div>
          </Card>
          <Card
            className={styles.sectionCard}
            title={
              <h2>
                {t('isolation.pools')}{' '}
                <span className={styles.countBadge}>{settings.pools.length}</span>
              </h2>
            }
            extra={
              <Button variant="secondary" size="sm" disabled={disabled} onClick={addPool}>
                <span className={styles.buttonContent}>
                  <IconPlus size={15} />
                  {t('isolation.addPool')}
                </span>
              </Button>
            }
          >
            <p className={styles.sectionDescription}>{t('isolation.poolsDescription')}</p>
            {settings.pools.length === 0 ? (
              <EmptyState title={t('isolation.noPools')} description={t('isolation.noPoolsHint')} />
            ) : (
              <div className={styles.workspace}>
                <nav className={styles.poolNav} aria-label={t('isolation.pools')}>
                  <Input
                    type="search"
                    value={poolSearch}
                    onChange={(event) => setPoolSearch(event.target.value)}
                    placeholder={t('isolation.searchPools')}
                    aria-label={t('isolation.searchPools')}
                  />
                  <div className={styles.poolList}>
                    {visiblePools.length === 0 && (
                      <p className={styles.hint}>{t('isolation.noResults')}</p>
                    )}
                    {visiblePools.map((pool) => {
                      const bindings = settings.keys.filter(
                        (binding) =>
                          binding.pool === pool.id && view['api-keys'].includes(binding.key)
                      ).length;
                      return (
                        <button
                          type="button"
                          key={pool.id}
                          className={`${styles.poolItem} ${selectedPool?.id === pool.id ? styles.poolActive : ''}`}
                          aria-pressed={selectedPool?.id === pool.id}
                          onClick={() => setSelectedId(pool.id)}
                        >
                          <img
                            src={pool.provider === 'codex' ? codexIcon : claudeIcon}
                            alt=""
                            width={24}
                            height={24}
                          />
                          <span className={styles.poolItemCopy}>
                            <strong>{pool.name || t('isolation.newPool')}</strong>
                            <span>
                              {providerName(pool.provider)} ·{' '}
                              {t('isolation.memberCount', { count: pool.accounts.length })}
                            </span>
                            <span>
                              {t('isolation.bindingCount', { count: bindings })}
                              {pool.accounts.length === 0 && (
                                <span className={styles.attention}>
                                  {' '}
                                  · {t('isolation.emptyPool')}
                                </span>
                              )}
                            </span>
                          </span>
                        </button>
                      );
                    })}
                  </div>
                </nav>
                {selectedPool && (
                  <PoolEditor
                    key={selectedPool.id}
                    pool={selectedPool}
                    settings={settings}
                    accounts={view.accounts}
                    disabled={disabled}
                    onChange={(patch) => updatePool(selectedPool.id, patch)}
                    onDelete={() =>
                      change({
                        ...settings,
                        pools: settings.pools.filter((pool) => pool.id !== selectedPool.id),
                      })
                    }
                  />
                )}
              </div>
            )}
          </Card>
          <KeyBindings
            settings={settings}
            apiKeys={view['api-keys']}
            accounts={view.accounts}
            disabled={disabled}
            onChange={updateBinding}
          />
          <p className={styles.footerNote}>{t('isolation.boundary')}</p>
          <FloatingSaveBar
            visible={dirty && isCurrentLayer}
            statusText={
              saving
                ? t('isolation.saving')
                : invalid
                  ? t('isolation.nameRequired')
                  : needsReload
                    ? t('isolation.reloadRequired')
                    : t('isolation.unsaved')
            }
            statusTone={invalid || needsReload ? 'error' : saving ? 'busy' : 'warning'}
            saving={saving}
            saveDisabled={!canSave}
            discardDisabled={disabled}
            onSave={save}
            onDiscard={discard}
          />
        </>
      )}
    </div>
  );
}
