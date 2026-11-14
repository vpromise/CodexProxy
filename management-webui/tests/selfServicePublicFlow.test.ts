import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { runInNewContext } from 'node:vm';

const source = readFileSync(
  new URL('../../internal/api/handlers/management/selfservice/app.js', import.meta.url),
  'utf8'
);
const invitation = `${'a'.repeat(32)}.${'b'.repeat(64)}`;

function page(authUrl = 'https://claude.ai/oauth/authorize?state=test') {
  type Element = {
    textContent: string;
    hidden: boolean;
    value: string;
    checked: boolean;
    href: string;
    focus: () => void;
    onclick?: () => Promise<void>;
    onsubmit?: (event: { preventDefault: () => void }) => Promise<void>;
  };
  const elements = new Map<string, Element>();
  const element = (id: string) => {
    if (!elements.has(id)) {
      elements.set(id, {
        textContent: '',
        hidden: true,
        value: '',
        checked: false,
        href: '',
        focus() {},
      });
    }
    return elements.get(id)!;
  };
  const calls: { url: string; options: RequestInit }[] = [];
  const history: string[] = [];
  runInNewContext(source, {
    document: { getElementById: element, documentElement: { lang: '' } },
    location: { hash: `#${invitation}`, pathname: '/oauth-enroll' },
    navigator: { language: 'en' },
    history: { replaceState: (_a: unknown, _b: unknown, path: string) => history.push(path) },
    URL,
    setTimeout,
    clearTimeout,
    fetch: async (url: string, options: RequestInit) => {
      calls.push({ url, options });
      return {
        ok: true,
        json: async () =>
          url.endsWith('start')
            ? { url: authUrl }
            : { provider: 'claude', status: url.endsWith('complete') ? 'received' : 'invited' },
      };
    },
  });
  return { element, calls, history };
}

describe('public OAuth enrollment', () => {
  test('requires consent and sends only its invitation capability to enrollment endpoints', async () => {
    const p = page();
    await p.element('start').onclick!();
    expect(p.calls).toHaveLength(1);
    p.element('consent').checked = true;
    await p.element('start').onclick!();
    expect(p.element('authorize').href).toStartWith('https://claude.ai/');
    expect(p.element('callback-section').hidden).toBe(false);
    p.element('callback').value = 'test-code#state';
    await p.element('callback-form').onsubmit!({ preventDefault() {} });
    expect(p.calls.map((r) => r.url)).toEqual([
      '/oauth-enroll/session',
      '/oauth-enroll/start',
      '/oauth-enroll/complete',
    ]);
    for (const call of p.calls) {
      expect(call.options.credentials).toBe('omit');
      expect(call.options.redirect).toBe('error');
      expect(call.options.headers).toMatchObject({ Authorization: `Bearer ${invitation}` });
    }
    expect(JSON.parse(p.calls[2].options.body as string)).toEqual({ callback: 'test-code#state' });
    expect(p.history).toEqual(['/oauth-enroll']);
    expect(p.element('callback').value).toBe('');
    expect(p.element('callback-section').hidden).toBe(true);
    expect(p.element('status').textContent).toContain('administrator review');
  });

  test('does not expose arbitrary authorization links to visitors', async () => {
    const p = page('https://attacker.example/oauth');
    p.element('consent').checked = true;
    await p.element('start').onclick!();
    expect(p.element('authorize').href).toBe('');
    expect(p.element('callback-section').hidden).toBe(true);
  });
});
