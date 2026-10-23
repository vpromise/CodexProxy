import { describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { MemoryRouter } from 'react-router-dom';
import i18n from '../src/i18n/index';
import { KeyBindings } from '../src/features/accessIsolation/KeyBindings';
import type { IsolationSettings } from '../src/features/accessIsolation/types';

const oldKey = 'synthetic-old-client-0001';
const newKey = 'synthetic-new-client-0002';
const settings: IsolationSettings = {
  enabled: true,
  pools: [{ id: 'pool-one', name: 'Codex pool', provider: 'codex', accounts: [] }],
  keys: [{ key: oldKey, name: 'Old client', note: '', pool: 'pool-one' }],
};

function renderKeys(apiKeys: string[], disabled = false) {
  return renderToStaticMarkup(
    createElement(
      MemoryRouter,
      null,
      createElement(KeyBindings, {
        settings,
        apiKeys,
        accounts: [],
        disabled,
        onChange: () => {},
        onRemove: () => {},
      })
    )
  );
}

describe('revoked client key record removal', () => {
  test('offers removal of the old binding after a key has been regenerated', async () => {
    await i18n.changeLanguage('en');
    const markup = renderKeys([newKey]);
    expect(markup).toContain('Revoked');
    expect(markup).toContain('aria-label="Remove record for Old client"');
    expect(markup).not.toContain(oldKey);
    expect(markup).not.toContain(newKey);
  });

  test('does not offer record removal for an active key', async () => {
    await i18n.changeLanguage('en');
    expect(renderKeys([oldKey])).not.toContain('aria-label="Remove record for Old client"');
  });

  test('disables revoked record removal while the page cannot accept edits', async () => {
    await i18n.changeLanguage('en');
    const markup = renderKeys([newKey], true);
    const button = markup.match(/<button[^>]*aria-label="Remove record for Old client"[^>]*>/)?.[0];
    expect(button).toBeDefined();
    expect(button).toContain('disabled=""');
  });
});
