import test from 'node:test';
import assert from 'node:assert/strict';

import { createIdempotencyKey } from '../static/js/idempotency.mjs';

test('uses randomUUID when available in a secure context', () => {
  const expected = '11111111-2222-4333-8444-555555555555';
  const actual = createIdempotencyKey({ randomUUID: () => expected });
  assert.equal(actual, expected);
});

test('uses getRandomValues when randomUUID is unavailable over HTTP', () => {
  const cryptoProvider = {
    getRandomValues(bytes) {
      bytes.fill(0xab);
      return bytes;
    }
  };

  const actual = createIdempotencyKey(cryptoProvider);
  assert.match(actual, /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
});

test('falls back without Web Crypto and still produces unique keys', () => {
  const first = createIdempotencyKey(null, () => 1234, () => 0.25);
  const second = createIdempotencyKey(null, () => 1234, () => 0.25);

  assert.match(first, /^fallback-/);
  assert.notEqual(first, second);
});

test('falls back when randomUUID exists but throws', () => {
  const actual = createIdempotencyKey(
    { randomUUID: () => { throw new Error('not a secure context'); } },
    () => 5678,
    () => 0.5
  );

  assert.match(actual, /^fallback-/);
});
