import { describe, expect, test } from 'bun:test';
import { parseLogLine } from '../src/pages/hooks/logParsing';

const requestId = '019994a8-7623-7b51-9a29-0123456789ab';

describe('complete request IDs in log lines', () => {
  for (const id of [requestId, 'abcdef12']) {
    test(`bracketed ${id}`, () => {
      const parsed = parseLogLine(
        `[2026-09-29 12:00:00] [${id}] [info ] [gin_logger.go:90] 200 | 25ms | 127.0.0.1 | GET "/v1/models"`
      );
      expect(parsed.requestId).toBe(id);
      expect(parsed.level).toBe('info');
      expect(parsed.statusCode).toBe(200);
      expect(parsed.source).toBe('gin_logger.go:90');
    });

    test(`pipe-delimited ${id}`, () => {
      const parsed = parseLogLine(
        `[2026-09-29 12:00:00] [info ] | ${id} | 200 | 25ms | 127.0.0.1 | GET /v1/models`
      );
      expect(parsed.requestId).toBe(id);
      expect(parsed.level).toBe('info');
      expect(parsed.statusCode).toBe(200);
    });
  }

  test('named IDs stay complete', () => {
    expect(parseLogLine(`request_id=${requestId} | 200 | GET /v1/models`).requestId).toBe(
      requestId
    );
  });

  test('placeholder does not become a request ID', () => {
    const parsed = parseLogLine('[2026-09-29 12:00:00] [--------] [info ] startup');
    expect(parsed.requestId).toBeUndefined();
    expect(parsed.level).toBe('info');
  });
});
