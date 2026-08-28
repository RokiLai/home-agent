import { state } from './state.js';

let onAuthFailure = null;

export function setAuthFailureHandler(fn) {
  onAuthFailure = fn;
}

export async function apiFetch(url, options = {}) {
  options.credentials = 'same-origin';
  const res = await fetch(url, options);
  if (res.status === 401) {
    state.isAuthenticated = false;
    if (onAuthFailure) {
      onAuthFailure();
    }
    throw new Error('Unauthorized');
  }
  return res;
}
