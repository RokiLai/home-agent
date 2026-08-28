import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';

function parseElements(html) {
  const elements = new Map();
  const stack = [];
  const tagPattern = /<\/?([a-z][\w-]*)([^>]*)>/gi;
  let match;

  while ((match = tagPattern.exec(html)) !== null) {
    const [source, tagName, attributes] = match;
    const normalizedTag = tagName.toLowerCase();
    if (source.startsWith('</')) {
      if (stack.at(-1)?.tagName === normalizedTag) stack.pop();
      continue;
    }

    const id = attributes.match(/\bid="([^"]+)"/)?.[1];
    const classes = new Set((attributes.match(/\bclass="([^"]*)"/)?.[1] || '').split(/\s+/).filter(Boolean));
    const element = {
      id,
      tagName: normalizedTag,
      parentElement: stack.at(-1) || null,
      innerText: '',
      innerHTML: '',
      children: [],
      className: [...classes].join(' '),
      insertBefore(child) { this.children.unshift(child); },
      removeChild(child) { this.children = this.children.filter(item => item !== child); },
      setAttribute(name, val) { this[name] = val; },
      getAttribute(name) { return this[name] ?? null; },
      focus() {},
      addEventListener() {},
      classList: {
        add(...names) { names.forEach(name => classes.add(name)); },
        remove(...names) { names.forEach(name => classes.delete(name)); },
        contains(name) { return classes.has(name); }
      }
    };
    if (id) elements.set(id, element);

    if (!source.endsWith('/>') && !['area', 'base', 'br', 'col', 'embed', 'hr', 'img', 'input', 'link', 'meta', 'source', 'track', 'wbr'].includes(normalizedTag)) {
      stack.push(element);
    }
  }

  return elements;
}

test('logout response replaces the authenticated view with a visible login panel', async () => {
  const html = await readFile(new URL('../static/index.html', import.meta.url), 'utf8');
  const elements = parseElements(html);
  const loginOverlay = elements.get('loginOverlay');
  assert.ok(loginOverlay, 'login overlay must exist');

  globalThis.window = { location: { origin: 'http://homeagent.test' } };
  globalThis.localStorage = { getItem() { return null; } };
  globalThis.document = {
    getElementById(id) { return elements.get(id) || null; },
    createElement(tagName) {
      return {
        tagName,
        className: '',
        innerHTML: '',
        children: [],
        classList: { add() {}, remove() {}, contains() { return false; } }
      };
    }
  };

  let logoutRequested = false;
  globalThis.fetch = async (url, options) => {
    assert.equal(url, '/api/v1/auth/logout');
    assert.equal(options.method, 'POST');
    assert.equal(options.credentials, 'same-origin');
    logoutRequested = true;
    return new Response(JSON.stringify({ success: true }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' }
    });
  };

  const { handleLogout } = await import('../static/js/auth.js');
  const { state } = await import('../static/js/state.js');
  state.isAuthenticated = true;

  await assert.doesNotReject(handleLogout());

  assert.equal(logoutRequested, true);
  assert.equal(state.isAuthenticated, false);
  assert.equal(loginOverlay.classList.contains('hidden'), false);
  for (let ancestor = loginOverlay.parentElement; ancestor; ancestor = ancestor.parentElement) {
    assert.equal(
      ancestor.classList.contains('hidden'),
      false,
      `login overlay must not be nested under hidden ancestor #${ancestor.id || '(anonymous)'}`
    );
  }
});

test('app initialization executes checkAuthStatus and shows login overlay when document is already complete', async () => {
  const html = await readFile(new URL('../static/index.html', import.meta.url), 'utf8');
  const elements = parseElements(html);
  const loginOverlay = elements.get('loginOverlay');
  const btnLogout = elements.get('btnLogout');

  let logoutListenerBound = false;
  btnLogout.addEventListener = (event, handler) => {
    if (event === 'click') logoutListenerBound = true;
  };

  globalThis.window = {
    location: { origin: 'http://homeagent.test', hash: '#/dashboard' },
    addEventListener() {}
  };
  globalThis.localStorage = { getItem() { return null; } };
  globalThis.document = {
    readyState: 'complete',
    getElementById(id) { return elements.get(id) || null; },
    querySelectorAll() { return []; },
    addEventListener() {},
    createElement(tagName) {
      return {
        tagName,
        className: '',
        innerHTML: '',
        children: [],
        classList: { add() {}, remove() {}, contains() { return false; } }
      };
    }
  };

  globalThis.fetch = async (url) => {
    if (url.endsWith('/api/v1/auth/me')) {
      return new Response(JSON.stringify({ error: 'unauthorized' }), {
        status: 401,
        headers: { 'Content-Type': 'application/json' }
      });
    }
    return new Response(JSON.stringify({}), { status: 200 });
  };

  const originalSetInterval = globalThis.setInterval;
  const timerIds = [];
  globalThis.setInterval = (fn, delay) => {
    const id = originalSetInterval(fn, delay);
    timerIds.push(id);
    return id;
  };

  try {
    const { init } = await import('../static/js/app.js');
    await init();

    assert.equal(logoutListenerBound, true, 'btnLogout click listener must be bound during init');
    assert.equal(loginOverlay.classList.contains('hidden'), false, 'login overlay must be visible when auth check fails on page load');
  } finally {
    timerIds.forEach(id => clearInterval(id));
    globalThis.setInterval = originalSetInterval;
  }
});
