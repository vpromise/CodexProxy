import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { Input } from '@/components/ui/Input';
import { Select } from '@/components/ui/Select';
import { SelectionCheckbox } from '@/components/ui/SelectionCheckbox';
import { IconAlertTriangle, IconSearch, IconTrash2 } from '@/components/ui/icons';
import { useNotificationStore } from '@/stores';
import type { AccountPool, IsolationSettings, PoolAccount, Provider } from './types';
import { providerName } from './types';
import styles from '@/pages/AccessIsolationPage.module.scss';

interface PoolEditorProps {
  pool: AccountPool;
  settings: IsolationSettings;
  accounts: PoolAccount[];
  disabled: boolean;
  onChange: (patch: Partial<AccountPool>) => void;
  onDelete: () => void;
}

export function PoolEditor({
  pool,
  settings,
  accounts,
  disabled,
  onChange,
  onDelete,
}: PoolEditorProps) {
  const { t } = useTranslation();
  const showConfirmation = useNotificationStore((state) => state.showConfirmation);
  const [search, setSearch] = useState('');
  const [selectedOnly, setSelectedOnly] = useState(false);
  const referenced = settings.keys.some((binding) => binding.pool === pool.id);
  const members = accounts.filter((account) => account.provider === pool.provider);
  const missing: PoolAccount[] = pool.accounts
    .filter((id) => !members.some((account) => account.id === id))
    .map((id) => ({
      id,
      provider: pool.provider,
      label: t('isolation.unavailable'),
      disabled: true,
    }));
  const query = search.trim().toLocaleLowerCase();
  const visible = [...members, ...missing].filter(
    (account) =>
      (!selectedOnly || pool.accounts.includes(account.id)) &&
      `${account.label} ${account.id} ${account.api_key_preview ?? ''} ${account.auth_index ?? ''} ${account.prefix ?? ''}`
        .toLocaleLowerCase()
        .includes(query)
  );
  const inactiveCount = pool.accounts.filter(
    (id) => !members.some((account) => account.id === id && !account.disabled)
  ).length;

  const changeProvider = (provider: Provider) => {
    if (provider === pool.provider) return;
    const apply = () => {
      setSearch('');
      setSelectedOnly(false);
      onChange({ provider, accounts: [] });
    };
    if (pool.accounts.length === 0) {
      apply();
      return;
    }
    showConfirmation({
      title: t('isolation.changeProviderTitle'),
      message: t('isolation.changeProviderMessage', { provider: providerName(provider) }),
      confirmText: t('common.confirm'),
      cancelText: t('common.cancel'),
      onConfirm: apply,
    });
  };

  return (
    <section className={styles.poolEditor} aria-label={t('isolation.editPool')}>
      <div className={styles.editorHeader}>
        <div>
          <span className={styles.eyebrow}>{t('isolation.editPool')}</span>
          <h3>{pool.name || t('isolation.newPool')}</h3>
        </div>
        <Button
          variant="ghost"
          size="sm"
          disabled={disabled || referenced}
          title={referenced ? t('isolation.referenced') : t('isolation.deletePool')}
          onClick={() =>
            showConfirmation({
              title: t('isolation.deletePool'),
              message: t('isolation.deletePoolMessage', { name: pool.name }),
              confirmText: t('isolation.delete'),
              cancelText: t('common.cancel'),
              variant: 'danger',
              onConfirm: onDelete,
            })
          }
        >
          <span className={styles.buttonContent}>
            <IconTrash2 size={15} />
            {t('isolation.delete')}
          </span>
        </Button>
      </div>
      <div className={styles.poolFields}>
        <Input
          label={t('isolation.poolName')}
          placeholder={t('isolation.poolNamePlaceholder')}
          value={pool.name}
          disabled={disabled}
          error={!pool.name.trim() ? t('isolation.nameRequired') : undefined}
          onChange={(event) => onChange({ name: event.target.value })}
        />
        <div className="form-group">
          <label htmlFor={`provider-${pool.id}`}>{t('isolation.provider')}</label>
          <Select
            id={`provider-${pool.id}`}
            value={pool.provider}
            disabled={disabled}
            options={[
              { value: 'codex', label: 'Codex' },
              { value: 'claude', label: 'Claude' },
            ]}
            onChange={(value) => changeProvider(value as Provider)}
          />
        </div>
      </div>
      {referenced && <p className={styles.hint}>{t('isolation.referenced')}</p>}
      <div className={styles.memberHeader}>
        <h4>{t('isolation.members')}</h4>
        <span className={styles.count}>
          {t('isolation.selectedCount', { count: pool.accounts.length })}
        </span>
      </div>
      <div className={styles.memberToolbar}>
        <Input
          type="search"
          value={search}
          onChange={(event) => setSearch(event.target.value)}
          placeholder={t('isolation.searchAccounts')}
          aria-label={t('isolation.searchAccounts')}
          rightElement={<IconSearch size={16} aria-hidden="true" />}
          className={styles.searchInput}
        />
        <SelectionCheckbox
          checked={selectedOnly}
          onChange={setSelectedOnly}
          label={t('isolation.selectedOnly')}
        />
      </div>
      <div className={styles.accountList}>
        {visible.length === 0 ? (
          <EmptyState
            title={
              members.length + missing.length === 0
                ? t('isolation.noAccounts')
                : t('isolation.noResults')
            }
            description={
              members.length + missing.length === 0
                ? t('isolation.identity')
                : t('isolation.adjustSearch')
            }
          />
        ) : (
          visible.map((account) => {
            const selected = pool.accounts.includes(account.id);
            const isMissing = missing.some((item) => item.id === account.id);
            const name = account.api_key_preview || account.label;
            const reference = account.auth_index
              ? `auth: ${account.auth_index}`
              : `…${account.id.slice(-12)}`;
            const shared = settings.pools.filter(
              (item) => item.id !== pool.id && item.accounts.includes(account.id)
            ).length;
            return (
              <SelectionCheckbox
                key={account.id}
                checked={selected}
                disabled={disabled}
                className={`${styles.accountRow} ${selected ? styles.accountSelected : ''}`}
                labelClassName={styles.accountLabel}
                ariaLabel={`${name} · ${reference}${account.prefix ? ` · ${t('common.prefix')}: ${account.prefix}` : ''}`}
                onChange={(checked) =>
                  onChange({
                    accounts: checked
                      ? [...pool.accounts, account.id]
                      : pool.accounts.filter((id) => id !== account.id),
                  })
                }
                label={
                  <>
                    <span className={styles.accountIdentity}>
                      <span className={styles.accountName}>{name}</span>
                      <code title={account.id}>{reference}</code>
                      {account.prefix && (
                        <span className={styles.accountPrefix}>
                          {t('common.prefix')}: {account.prefix}
                        </span>
                      )}
                    </span>
                    <span className={styles.badges}>
                      {shared > 0 && (
                        <span className={styles.badge}>
                          {t('isolation.sharedCount', { count: shared })}
                        </span>
                      )}
                      {account.disabled && (
                        <span className={`${styles.badge} ${styles.warningBadge}`}>
                          {t(isMissing ? 'isolation.unavailable' : 'isolation.disabled')}
                        </span>
                      )}
                    </span>
                  </>
                }
              />
            );
          })
        )}
      </div>
      {pool.accounts.length === 0 ? (
        <p className={styles.inlineNotice}>
          <IconAlertTriangle size={15} />
          {t('isolation.emptyPoolHint')}
        </p>
      ) : inactiveCount > 0 ? (
        <p className={styles.inlineNotice}>
          <IconAlertTriangle size={15} />
          {t('isolation.inactiveCount', { count: inactiveCount })}
        </p>
      ) : null}
      <p className={styles.hint}>{t('isolation.shared')}</p>
    </section>
  );
}
