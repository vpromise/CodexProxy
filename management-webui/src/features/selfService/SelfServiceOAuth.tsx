import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { useAuthStore, useNotificationStore } from '@/stores';
import { selfServiceOAuthApi, type SelfServiceRecord } from '@/services/api/selfServiceOAuth';
import { copyToClipboard } from '@/utils/clipboard';
import { normalizeApiBase } from '@/utils/connection';
import { getErrorMessage } from '@/utils/helpers';
import { notifyAuthFilesChanged } from '@/features/authFiles/authFilesEvents';
import styles from './SelfServiceOAuth.module.scss';

export function SelfServiceOAuth() {
  const { t } = useTranslation();
  const apiBase = useAuthStore((state) => state.apiBase);
  const { showNotification, showConfirmation } = useNotificationStore();
  const [records, setRecords] = useState<SelfServiceRecord[]>([]);
  const [provider, setProvider] = useState<'claude' | 'codex'>('claude');
  const [label, setLabel] = useState('');
  const [invitation, setInvitation] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    try {
      const result = await selfServiceOAuthApi.list();
      setRecords(result.records);
      setError('');
    } catch (error) {
      setError(getErrorMessage(error));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function create() {
    setBusy(true);
    try {
      const result = await selfServiceOAuthApi.create(provider, label.trim());
      setInvitation(`${normalizeApiBase(apiBase)}/oauth-enroll#${result.invitation_token}`);
      setLabel('');
      await load();
    } catch (error) {
      showNotification(getErrorMessage(error), 'error');
    } finally {
      setBusy(false);
    }
  }

  async function act(record: SelfServiceRecord, accept: boolean) {
    setBusy(true);
    try {
      if (accept) {
        await selfServiceOAuthApi.accept(record.id);
        notifyAuthFilesChanged();
        showNotification(t('self_service.accepted_hint'), 'success');
      } else {
        await selfServiceOAuthApi.reject(record.id);
      }
      await load();
    } catch (error) {
      showNotification(getErrorMessage(error), 'error');
      await load();
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card
      title={t('self_service.title')}
      extra={
        <Button variant="secondary" size="sm" disabled={busy} onClick={() => void load()}>
          {t('self_service.refresh')}
        </Button>
      }
    >
      <p className={styles.hint}>{t('self_service.description')}</p>
      <div className={styles.create}>
        <label className={styles.provider}>
          {t('self_service.provider')}
          <select
            className="input"
            value={provider}
            disabled={busy}
            onChange={(event) => setProvider(event.target.value as 'claude' | 'codex')}
          >
            <option value="claude">Claude</option>
            <option value="codex">Codex</option>
          </select>
        </label>
        <Input
          label={t('self_service.label')}
          value={label}
          maxLength={200}
          disabled={busy}
          onChange={(event) => setLabel(event.target.value)}
        />
        <Button disabled={busy} onClick={() => void create()}>
          {t('self_service.create')}
        </Button>
      </div>
      {invitation && (
        <div className={styles.invitation}>
          <Input label={t('self_service.link')} value={invitation} readOnly />
          <Button
            variant="secondary"
            onClick={() =>
              void copyToClipboard(invitation).then((copied) =>
                showNotification(
                  t(copied ? 'self_service.copied' : 'self_service.copy_failed'),
                  copied ? 'success' : 'error'
                )
              )
            }
          >
            {t('self_service.copy')}
          </Button>
          <Button variant="ghost" onClick={() => setInvitation('')}>
            {t('self_service.hide')}
          </Button>
        </div>
      )}
      {error && <p role="alert">{error}</p>}
      {!error && records.length === 0 && <p className={styles.hint}>{t('self_service.empty')}</p>}
      <div className={styles.records}>
        {records.map((record) => (
          <article key={record.id} className={styles.record}>
            <div className={styles.details}>
              <strong>{record.label || record.email || record.provider}</strong>
              <span>
                {record.provider} ·{' '}
                {t(`self_service.status.${record.status}`, { defaultValue: record.status })}
              </span>
              {record.email && record.label && <span>{record.email}</span>}
              {record.account_id && <span className={styles.identifier}>{record.account_id}</span>}
              <span>
                {t('self_service.created')} {new Date(record.created_at).toLocaleString()}
              </span>
              <span>
                {t('self_service.expires')} {new Date(record.expires_at).toLocaleString()}
              </span>
            </div>
            <div className={styles.actions}>
              {['pending', 'importing'].includes(record.status) && (
                <Button
                  disabled={busy}
                  size="sm"
                  onClick={() =>
                    showConfirmation({
                      title: t('self_service.accept'),
                      message: t('self_service.accept_confirm'),
                      onConfirm: () => act(record, true),
                    })
                  }
                >
                  {t('self_service.accept')}
                </Button>
              )}
              {['invited', 'authorizing', 'exchanging', 'pending', 'failed'].includes(
                record.status
              ) && (
                <Button
                  disabled={busy}
                  variant="danger"
                  size="sm"
                  onClick={() =>
                    showConfirmation({
                      title: t('self_service.reject'),
                      message: t('self_service.reject_confirm'),
                      variant: 'danger',
                      onConfirm: () => act(record, false),
                    })
                  }
                >
                  {t('self_service.reject')}
                </Button>
              )}
              {record.status === 'accepted' && (
                <Link to="/auth-files">{t('self_service.accounts')}</Link>
              )}
            </div>
          </article>
        ))}
      </div>
      <p className={styles.hint}>
        {t('self_service.next')} <Link to="/access-isolation">{t('self_service.pools')}</Link>
      </p>
    </Card>
  );
}
