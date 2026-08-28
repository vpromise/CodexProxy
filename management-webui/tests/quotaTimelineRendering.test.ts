import { describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import '../src/i18n/index';
import { QuotaTimeline } from '../src/features/quota/components/QuotaTimeline';
import type { QuotaFileEntry } from '../src/features/quota/logic';

const entries: QuotaFileEntry[] = [
  {
    file: { name: 'weekly-only.json', type: 'claude' },
    type: 'claude',
  },
];

const baseProps = {
  entries,
  displayNameFor: (name: string) => name,
  resolvedTheme: 'light' as const,
  now: new Date(2026, 6, 29, 12).getTime(),
};

describe('QuotaTimeline rendering', () => {
  test('shows the selected period date instead of always labelling it Today', () => {
    const markup = renderToStaticMarkup(
      createElement(QuotaTimeline, {
        ...baseProps,
        initialOffset: 1,
        quotaFor: () => ({
          status: 'success',
          windows: [
            {
              label: '7-day',
              usedPercent: 25,
              resetAtMs: new Date(2026, 7, 1, 12).getTime(),
              periodHours: 168,
            },
          ],
        }),
      })
    );

    // The next weekly period starts on Sunday 08/02. The button remains the
    // shortcut back to Today (aria-label/title), but its visible label now
    // reflects the period selected with the previous/next arrows.
    expect(markup).toMatch(
      /<button type="button" aria-label="[^"]+" title="[^"]+">08\/02<\/button>/
    );
  });

  test('keeps the panel and controls visible when 5-hour mode has no matching lanes', () => {
    const weeklyOnlyQuota = {
      status: 'success' as const,
      windows: [
        {
          label: '7-day',
          usedPercent: 25,
          resetAtMs: new Date(2026, 7, 1, 12).getTime(),
          periodHours: 168,
        },
      ],
    };

    const markup = renderToStaticMarkup(
      createElement(QuotaTimeline, {
        ...baseProps,
        initialMode: 'session',
        quotaFor: () => weeklyOnlyQuota,
      })
    );

    expect(markup).toContain('<section');
    expect(markup).toContain('aria-pressed="true"');
    expect(markup).toContain('role="status"');
  });

  test('renders an unexpired Codex reset credit as an expiry tick', () => {
    const markup = renderToStaticMarkup(
      createElement(QuotaTimeline, {
        entries: [
          {
            file: { name: 'codex-credit.json', type: 'codex' },
            type: 'codex',
          },
        ],
        displayNameFor: (name: string) => name,
        resolvedTheme: 'light',
        now: new Date(2026, 6, 29, 12).getTime(),
        quotaFor: () => ({
          status: 'success',
          windows: [
            {
              label: '7-day',
              usedPercent: 90,
              resetAtMs: new Date(2026, 7, 1, 12).getTime(),
              periodHours: 168,
            },
          ],
          rateLimitResetCredits: [
            {
              id: 'credit-1',
              status: 'available',
              grantedAt: '2026-07-20T12:00:00Z',
              expiresAt: '2026-08-03T12:00:00Z',
            },
          ],
        }),
      })
    );

    expect(markup).toContain('role="img"');
    expect(markup).toContain('08/03 12:00');
  });

  test('stays hidden before any credential exposes a usable quota window', () => {
    const markup = renderToStaticMarkup(
      createElement(QuotaTimeline, {
        ...baseProps,
        quotaFor: () => undefined,
      })
    );

    expect(markup).toBe('');
  });
});
