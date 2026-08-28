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
      removeAttribute(name) { delete this[name]; },
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

test('RBAC: Owner role reveals user management navigation and renders user table actions', async () => {
  const html = await readFile(new URL('../static/index.html', import.meta.url), 'utf8');
  const elements = parseElements(html);

  const navUsers = elements.get('navUsers');
  const adminRoleBadge = elements.get('adminRoleBadge');
  const usersTableBody = elements.get('usersTableBody');
  assert.ok(navUsers, '#navUsers must exist in DOM');
  assert.ok(adminRoleBadge, '#adminRoleBadge must exist in DOM');
  assert.ok(usersTableBody, '#usersTableBody must exist in DOM');

  globalThis.window = { location: { origin: 'http://homeagent.test', hash: '#/users' } };
  globalThis.localStorage = { getItem() { return null; } };
  globalThis.document = {
    getElementById(id) { return elements.get(id) || null; },
    querySelector(sel) {
      if (sel.startsWith('#')) return elements.get(sel.slice(1)) || null;
      return null;
    },
    querySelectorAll() { return []; }
  };

  const usersMock = [
    { id: 'usr-1', username: 'owner_user', role: 'owner', status: 'active', last_login_at: '2026-08-28T10:00:00Z' },
    { id: 'usr-2', username: 'alice_admin', role: 'admin', status: 'active', last_login_at: '2026-08-28T11:00:00Z' },
    { id: 'usr-3', username: 'bob_viewer', role: 'viewer', status: 'disabled', last_login_at: null }
  ];

  globalThis.fetch = async (url) => {
    if (url.includes('/api/v1/auth/me')) {
      return new Response(JSON.stringify({
        user_id: 'usr-1',
        username: 'owner_user',
        role: 'owner',
        permissions: ['system:manage_users', 'devices:view', 'devices:operate']
      }), { status: 200, headers: { 'Content-Type': 'application/json' } });
    }
    if (url.includes('/api/v1/users')) {
      return new Response(JSON.stringify({ users: usersMock }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' }
      });
    }
    return new Response(JSON.stringify({}), { status: 200 });
  };

  const { checkAuthStatus } = await import('../static/js/auth.js');
  const { fetchUsersList } = await import('../static/js/users.js');

  const authOk = await checkAuthStatus();
  assert.equal(authOk, true);
  assert.equal(navUsers.classList.contains('hidden'), false, 'navUsers must be visible for Owner');
  assert.match(adminRoleBadge.innerText, /Owner/i);

  await fetchUsersList();
  assert.ok(usersTableBody.innerHTML.includes('alice_admin'), 'Table must render username alice_admin');
  assert.ok(usersTableBody.innerHTML.includes('bob_viewer'), 'Table must render username bob_viewer');
  assert.ok(usersTableBody.innerHTML.includes('重置密码'), 'Table must contain reset password action for other users');
  assert.ok(usersTableBody.innerHTML.includes('删除'), 'Table must contain delete action for other users');
});

test('RBAC: Viewer role hides user management navigation and restricts dangerous write actions', async () => {
  const html = await readFile(new URL('../static/index.html', import.meta.url), 'utf8');
  const elements = parseElements(html);

  const navUsers = elements.get('navUsers');
  const adminRoleBadge = elements.get('adminRoleBadge');
  const btnUpgradeAll = elements.get('btnUpgradeAll');
  const btnSyncAll = elements.get('btnSyncAll');

  globalThis.window = { location: { origin: 'http://homeagent.test', hash: '#/dashboard' } };
  globalThis.localStorage = { getItem() { return null; } };
  globalThis.document = {
    getElementById(id) { return elements.get(id) || null; },
    querySelector(sel) {
      if (sel.startsWith('#')) return elements.get(sel.slice(1)) || null;
      return null;
    },
    querySelectorAll() { return []; }
  };

  globalThis.fetch = async (url) => {
    if (url.includes('/api/v1/auth/me')) {
      return new Response(JSON.stringify({
        user_id: 'usr-3',
        username: 'bob_viewer',
        role: 'viewer',
        permissions: ['devices:view', 'health:view']
      }), { status: 200, headers: { 'Content-Type': 'application/json' } });
    }
    return new Response(JSON.stringify({}), { status: 200 });
  };

  const { checkAuthStatus } = await import('../static/js/auth.js');
  const authOk = await checkAuthStatus();
  assert.equal(authOk, true);

  assert.equal(navUsers.classList.contains('hidden'), true, 'navUsers must be hidden for Viewer');
  assert.match(adminRoleBadge.innerText, /Viewer/i);

  if (btnUpgradeAll) {
    assert.equal(btnUpgradeAll.classList.contains('hidden-viewer'), true, 'btnUpgradeAll must have hidden-viewer class');
    assert.equal(btnUpgradeAll.disabled, 'true', 'btnUpgradeAll must be disabled');
  }
  if (btnSyncAll) {
    assert.equal(btnSyncAll.classList.contains('hidden-viewer'), true, 'btnSyncAll must have hidden-viewer class');
    assert.equal(btnSyncAll.disabled, 'true', 'btnSyncAll must be disabled');
  }
});
