import { describe, expect, test } from 'bun:test';
import { parseApiErrorResponse } from '../src/services/api/apiError';

describe('Management API error parsing', () => {
  test('prefers the human-readable message and preserves the API error code', () => {
    const result = parseApiErrorResponse(
      {
        error: 'upstream_request_failed',
        message: 'upstream request: 404 Not Found',
      },
      'Request failed with status code 502'
    );

    expect(result).toEqual({
      message: 'upstream request: 404 Not Found',
      apiCode: 'upstream_request_failed',
    });
  });

  test('falls back to a string error used by legacy endpoints', () => {
    expect(parseApiErrorResponse({ error: 'invalid body' }, 'Bad Request')).toEqual({
      message: 'invalid body',
      apiCode: 'invalid body',
    });
  });

  test('supports nested error messages and codes', () => {
    expect(
      parseApiErrorResponse(
        { error: { code: 'invalid_config', message: 'auth-dir is invalid' } },
        'Bad Request'
      )
    ).toEqual({
      message: 'auth-dir is invalid',
      apiCode: 'invalid_config',
    });
  });

  test('uses a text response body before the transport fallback', () => {
    expect(parseApiErrorResponse('upstream unavailable', 'Network Error')).toEqual({
      message: 'upstream unavailable',
    });
  });

  test('uses the transport message for an unknown response shape', () => {
    expect(parseApiErrorResponse({ error: null }, 'Network Error')).toEqual({
      message: 'Network Error',
      apiCode: undefined,
    });
  });
});
