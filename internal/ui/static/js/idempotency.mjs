let fallbackCounter = 0;

function bytesToUUID(bytes) {
  bytes[6] = (bytes[6] & 0x0f) | 0x40;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;
  const hex = Array.from(bytes, value => value.toString(16).padStart(2, '0')).join('');
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

// createIdempotencyKey works in HTTPS and localhost via randomUUID, and in
// insecure HTTP contexts via getRandomValues. The final fallback only needs
// request uniqueness; the key is not used as an authentication credential.
export function createIdempotencyKey(
  cryptoProvider = globalThis.crypto,
  now = Date.now,
  random = Math.random
) {
  if (typeof cryptoProvider?.randomUUID === 'function') {
    try {
      return cryptoProvider.randomUUID();
    } catch (_) {
      // Continue with the HTTP-compatible path.
    }
  }

  if (typeof cryptoProvider?.getRandomValues === 'function') {
    try {
      return bytesToUUID(cryptoProvider.getRandomValues(new Uint8Array(16)));
    } catch (_) {
      // Continue with a non-cryptographic uniqueness fallback.
    }
  }

  fallbackCounter += 1;
  const randomPart = Math.floor(random() * Number.MAX_SAFE_INTEGER).toString(36);
  return `fallback-${now().toString(36)}-${fallbackCounter.toString(36)}-${randomPart}`;
}
