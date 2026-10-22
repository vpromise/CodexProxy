import { useReducer } from 'react';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { EmptyState } from '@/components/ui/EmptyState';
import { Input } from '@/components/ui/Input';
import { Select } from '@/components/ui/Select';
import { IconExternalLink, IconKey, IconSearch } from '@/components/ui/icons';
import { providerName } from './types';
import {
  getVisibleBindingKeys,
  initialKeyBindingFilter,
  keyBindingFilterReducer,
} from './keyBindingFilter';
import type { IsolationSettings, KeyBinding, PoolAccount } from './types';
import styles from '@/pages/AccessIsolationPage.module.scss';

interface KeyBindingsProps {
  settings: IsolationSettings;
  apiKeys: string[];
  accounts: PoolAccount[];
  disabled: boolean;
  onChange: (key: string, patch: Partial<KeyBinding>) => void;
}

export function KeyBindings({ settings, apiKeys, accounts, disabled, onChange }: KeyBindingsProps) {
  const { t } = useTranslation();
  const [filterState, dispatchFilter] = useReducer(
    keyBindingFilterReducer,
    initialKeyBindingFilter
  );
  const allKeys = [...new Set([...apiKeys, ...settings.keys.map((binding) => binding.key)])];
  const visible = getVisibleBindingKeys(settings, apiKeys, filterState);
  const updateBinding = (key: string, patch: Partial<KeyBinding>) => {
    dispatchFilter({ type: 'edit', key });
    onChange(key, patch);
  };
  return (
    <Card
      className={styles.sectionCard}
      title={
        <h2>
          {t('isolation.keys')} <span className={styles.countBadge}>{apiKeys.length}</span>
        </h2>
      }
      extra={
        <Link className={styles.textLink} to="/config">
          {t('isolation.manageKeys')}
          <IconExternalLink size={14} />
        </Link>
      }
    >
      <p className={styles.sectionDescription}>{t('isolation.keysDescription')}</p>
      {allKeys.length === 0 ? (
        <EmptyState title={t('isolation.noKeys')} />
      ) : (
        <>
          <div className={styles.keyToolbar}>
            <Input
              type="search"
              value={filterState.search}
              onChange={(event) => dispatchFilter({ type: 'search', value: event.target.value })}
              placeholder={t('isolation.searchKeys')}
              aria-label={t('isolation.searchKeys')}
              rightElement={<IconSearch size={16} aria-hidden="true" />}
              className={styles.searchInput}
            />
            <Select
              value={filterState.filter}
              onChange={(value) => dispatchFilter({ type: 'filter', value })}
              ariaLabel={t('isolation.filterKeys')}
              options={[
                { value: 'all', label: t('isolation.allKeys') },
                { value: 'unassigned', label: t('isolation.unassigned') },
                { value: 'revoked', label: t('isolation.revoked') },
              ]}
            />
          </div>
          {visible.length === 0 ? (
            <EmptyState
              title={t('isolation.noResults')}
              description={t('isolation.adjustSearch')}
            />
          ) : (
            <div className={styles.keyRows}>
              {visible.map((key) => {
                const binding = settings.keys.find((item) => item.key === key);
                const pool = settings.pools.find((item) => item.id === binding?.pool);
                const revoked = !apiKeys.includes(key);
                const available = pool?.accounts.some((id) =>
                  accounts.some((account) => account.id === id && !account.disabled)
                );
                const status = revoked
                  ? 'revoked'
                  : !pool
                    ? 'unassigned'
                    : !available
                      ? 'poolUnavailable'
                      : 'assigned';
                const keyLabel = `${t('isolation.keyNumber', { count: allKeys.indexOf(key) + 1 })} · …${key.slice(-4)}`;
                return (
                  <div className={styles.keyRow} key={key}>
                    <div className={styles.keyIdentity}>
                      <span className={styles.keyLabel}>
                        <IconKey size={15} />
                        <code>{keyLabel}</code>
                      </span>
                      <span
                        className={`${styles.badge} ${!revoked && status !== 'assigned' ? styles.warningBadge : ''}`}
                      >
                        {t(`isolation.${status}`)}
                      </span>
                    </div>
                    <Input
                      label={t('isolation.name')}
                      aria-label={`${keyLabel} · ${t('isolation.name')}`}
                      placeholder={t('isolation.keyNamePlaceholder')}
                      value={binding?.name ?? ''}
                      disabled={disabled}
                      onChange={(event) => updateBinding(key, { name: event.target.value })}
                    />
                    <Input
                      label={t('isolation.note')}
                      aria-label={`${keyLabel} · ${t('isolation.note')}`}
                      placeholder={t('isolation.optional')}
                      value={binding?.note ?? ''}
                      disabled={disabled}
                      onChange={(event) => updateBinding(key, { note: event.target.value })}
                    />
                    <div className="form-group">
                      <span className={styles.fieldLabel}>{t('isolation.pool')}</span>
                      <Select
                        ariaLabel={`${keyLabel} · ${t('isolation.pool')}`}
                        value={binding?.pool ?? ''}
                        disabled={disabled}
                        onChange={(poolId) => updateBinding(key, { pool: poolId })}
                        options={[
                          { value: '', label: t('isolation.unassigned') },
                          ...settings.pools.map((item) => ({
                            value: item.id,
                            label: `${item.name || t('isolation.newPool')} · ${providerName(item.provider)}`,
                          })),
                        ]}
                      />
                    </div>
                  </div>
                );
              })}
            </div>
          )}
        </>
      )}
    </Card>
  );
}
