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
      value: '',
      disabled: false,
      children: [],
      className: [...classes].join(' '),
      appendChild(child) { this.children.push(child); },
      insertBefore(child) { this.children.unshift(child); },
      removeChild(child) { this.children = this.children.filter(item => item !== child); },
      reset() {
        this.value = '';
      },
      focus() {
        this.focused = true;
      },
      setAttribute(name, val) { this[name] = val; },
      getAttribute(name) { return this[name] ?? null; },
      removeAttribute(name) { delete this[name]; },
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

globalThis.localStorage = {
  getItem() { return null; },
  setItem() {},
  removeItem() {}
};
globalThis.window = {
  location: { origin: 'http://homeagent.test', hash: '' }
};

test('Password Consolidation: DOM structure contracts', async () => {
  const html = await readFile(new URL('../static/index.html', import.meta.url), 'utf8');
  const elements = parseElements(html);

  // 1. settingsSecPassword must be removed from general settings
  assert.equal(elements.has('settingsSecPassword'), false, '#settingsSecPassword must be removed from index.html');

  // 2. changePasswordModal and required form elements must exist
  assert.ok(elements.has('changePasswordModal'), '#changePasswordModal must exist in index.html');
  assert.ok(elements.get('changePasswordModal').classList.contains('hidden'), '#changePasswordModal must be hidden by default');

  const requiredIds = [
    'changePasswordForm',
    'oldPasswordInput',
    'newPasswordInput',
    'confirmPasswordInput',
    'savePasswordBtn',
    'changePasswordAlert',
    'btnOpenChangePassword'
  ];

  for (const id of requiredIds) {
    assert.ok(elements.has(id), `#${id} must exist in index.html`);
  }
});

test('Password Consolidation: open and close change password modal', async () => {
  const html = await readFile(new URL('../static/index.html', import.meta.url), 'utf8');
  const elements = parseElements(html);

  globalThis.document = {
    getElementById(id) { return elements.get(id) || null; },
    createElement(tag) {
      return {
        tagName: tag,
        className: '',
        innerText: '',
        innerHTML: '',
        classList: { add() {}, remove() {}, contains() { return false; } }
      };
    }
  };

  const { openChangePasswordModal, closeChangePasswordModal } = await import('../static/js/auth.js');

  const modal = elements.get('changePasswordModal');
  const alert = elements.get('changePasswordAlert');
  const oldPass = elements.get('oldPasswordInput');

  // Open modal
  openChangePasswordModal();
  assert.equal(modal.classList.contains('hidden'), false, 'Modal should not have hidden class after open');
  assert.equal(alert.classList.contains('hidden'), true, 'Alert should be hidden on open');
  assert.equal(oldPass.focused, true, 'oldPasswordInput should be focused on open');

  // Close modal
  closeChangePasswordModal();
  assert.equal(modal.classList.contains('hidden'), true, 'Modal should have hidden class after close');
});

test('Password Consolidation: handleChangePassword input validation and failure paths', async () => {
  const html = await readFile(new URL('../static/index.html', import.meta.url), 'utf8');
  const elements = parseElements(html);

  globalThis.document = {
    getElementById(id) { return elements.get(id) || null; },
    createElement(tag) {
      return {
        tagName: tag,
        className: '',
        innerText: '',
        innerHTML: '',
        classList: { add() {}, remove() {}, contains() { return false; } }
      };
    }
  };

  const { handleChangePassword } = await import('../static/js/auth.js');

  const oldPass = elements.get('oldPasswordInput');
  const newPass = elements.get('newPasswordInput');
  const confirmPass = elements.get('confirmPasswordInput');
  const alert = elements.get('changePasswordAlert');

  // Case 1: Empty input
  oldPass.value = '';
  newPass.value = '';
  confirmPass.value = '';
  await handleChangePassword({ preventDefault() {} });
  assert.equal(alert.classList.contains('hidden'), false);
  assert.match(alert.innerText, /请输入当前旧密码和新密码/);

  // Case 2: Short password
  oldPass.value = 'old123';
  newPass.value = '12345';
  confirmPass.value = '12345';
  await handleChangePassword({ preventDefault() {} });
  assert.equal(alert.classList.contains('hidden'), false);
  assert.match(alert.innerText, /不能少于 6 位/);

  // Case 3: Password mismatch
  oldPass.value = 'old123';
  newPass.value = 'newPass123';
  confirmPass.value = 'mismatchPass';
  await handleChangePassword({ preventDefault() {} });
  assert.equal(alert.classList.contains('hidden'), false);
  assert.match(alert.innerText, /两次输入的新密码不一致/);
});

test('Password Consolidation: handleChangePassword API success and error handling', async () => {
  const html = await readFile(new URL('../static/index.html', import.meta.url), 'utf8');
  const elements = parseElements(html);

  const modal = elements.get('changePasswordModal');
  const oldPass = elements.get('oldPasswordInput');
  const newPass = elements.get('newPasswordInput');
  const confirmPass = elements.get('confirmPasswordInput');
  const alert = elements.get('changePasswordAlert');
  const saveBtn = elements.get('savePasswordBtn');

  globalThis.document = {
    getElementById(id) { return elements.get(id) || null; },
    createElement(tag) {
      return {
        tagName: tag,
        className: '',
        innerText: '',
        innerHTML: '',
        classList: { add() {}, remove() {}, contains() { return false; } }
      };
    }
  };

  const { handleChangePassword } = await import('../static/js/auth.js');

  // Case 4: API returns 400 wrong old password
  globalThis.fetch = async () => new Response(JSON.stringify({
    success: false,
    message: '旧密码错误，请重新输入'
  }), { status: 400, headers: { 'Content-Type': 'application/json' } });

  oldPass.value = 'wrongPass';
  newPass.value = 'validNewPass123';
  confirmPass.value = 'validNewPass123';

  await handleChangePassword({ preventDefault() {} });
  assert.equal(alert.classList.contains('hidden'), false);
  assert.match(alert.innerText, /旧密码错误/);
  assert.equal(saveBtn.disabled, false);

  // Case 5: API returns 200 success
  modal.classList.remove('hidden');
  globalThis.fetch = async () => new Response(JSON.stringify({
    success: true,
    message: '密码更新成功'
  }), { status: 200, headers: { 'Content-Type': 'application/json' } });

  oldPass.value = 'correctOldPass';
  newPass.value = 'brandNewPass456';
  confirmPass.value = 'brandNewPass456';

  await handleChangePassword({ preventDefault() {} });
  assert.equal(modal.classList.contains('hidden'), true, 'Modal must close on success');
});

test('Password Consolidation: renderUsersTable displays change password button for self', async () => {
  const html = await readFile(new URL('../static/index.html', import.meta.url), 'utf8');
  const elements = parseElements(html);

  globalThis.document = {
    getElementById(id) { return elements.get(id) || null; },
    createElement(tag) {
      return {
        tagName: tag,
        className: '',
        innerText: '',
        innerHTML: '',
        classList: { add() {}, remove() {}, contains() { return false; } }
      };
    }
  };

  const { state } = await import('../static/js/state.js');
  state.currentUser = { id: 'usr-1', username: 'owner_user', role: 'owner' };

  const { renderUsersTable } = await import('../static/js/users.js');

  const usersMock = [
    { id: 'usr-1', username: 'owner_user', role: 'owner', status: 'active', last_login_at: '2026-08-28T10:00:00Z' },
    { id: 'usr-2', username: 'alice_admin', role: 'admin', status: 'active', last_login_at: '2026-08-28T11:00:00Z' }
  ];

  renderUsersTable(usersMock);

  const tbody = elements.get('usersTableBody');
  assert.ok(tbody.innerHTML.includes('openChangePasswordModal()'), 'Table must contain openChangePasswordModal for current user');
  assert.ok(tbody.innerHTML.includes('修改密码'), 'Table must contain 修改密码 button');
  assert.ok(tbody.innerHTML.includes('openResetUserPasswordModal'), 'Table must contain openResetUserPasswordModal for other users');
});
