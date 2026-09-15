import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';

test('file relay page and settings controls are present in the real HTML', async () => {
  const html = await readFile(new URL('../static/index.html', import.meta.url), 'utf8');
  for (const id of ['navFiles', 'pageFiles', 'fileUploadInput', 'fileUploadBtn', 'fileUsageText', 'fileUsageBar', 'filesTableBody', 'settingsSecFiles', 'fileRetentionDaysInput', 'saveFileSettingsBtn']) {
    assert.match(html, new RegExp(`id="${id}"`), `${id} must exist`);
  }
});

test('files route is a primary route', async () => {
  globalThis.window = { location: { hash: '#/files' }, addEventListener() {} };
  globalThis.localStorage = { getItem() { return null; }, setItem() {}, removeItem() {} };
  globalThis.document = { querySelectorAll() { return []; }, getElementById() { return null; }, body: { classList: { remove() {} } } };
  const { parseRoute, pageMeta } = await import('../static/js/router.js');
  assert.ok(pageMeta.files);
  assert.deepEqual(parseRoute('#/files'), { page: 'files', deviceId: null, section: null });
});

test('file responses render escaped names, server usage, expiration, and operations', async () => {
  const nodes = new Map();
  const node = id => ({ id, innerHTML: '', innerText: '', style: {}, classList: { add() {}, remove() {}, toggle() {} }, setAttribute() {}, removeAttribute() {} });
  for (const id of ['filesTableBody', 'fileUsageText', 'fileUsageBar', 'fileEmptyState', 'fileLoadError']) nodes.set(id, node(id));
  globalThis.document = { getElementById(id) { return nodes.get(id) || null; } };
  const { renderFileList, formatBytes, canUploadSize } = await import('../static/js/files.js');
  renderFileList({ files: [{ id: 'file_1', name: '<script>x</script>.txt', size: 1024, created_at: '2026-09-15T10:00:00Z', expires_at: '2026-09-22T10:00:00Z' }], quota_bytes: 20 * 1024 ** 3, used_bytes: 1024, reserved_bytes: 0 });
  assert.equal(formatBytes(1024), '1 KiB');
  assert.match(nodes.get('fileUsageText').innerText, /1 KiB \/ 20 GiB/);
  assert.ok(nodes.get('fileUsageBar').style.width.endsWith('%'));
  assert.doesNotMatch(nodes.get('filesTableBody').innerHTML, /<script>/);
  assert.match(nodes.get('filesTableBody').innerHTML, /&lt;script&gt;/);
  assert.match(nodes.get('filesTableBody').innerHTML, /downloadFile/);
  assert.match(nodes.get('filesTableBody').innerHTML, /openFileLinks/);
  assert.match(nodes.get('filesTableBody').innerHTML, /deleteFile/);
  assert.equal(canUploadSize(20 * 1024 ** 3), false, 'client must reject files larger than current remaining capacity');
  assert.equal(canUploadSize(1024), true);
});

test('file settings are editable only for owner and preserve revision', async () => {
  const input = { value: '', disabled: false };
  const button = { disabled: false };
  const usage = { innerText: '' };
  globalThis.document = { getElementById(id) { return { fileRetentionDaysInput: input, saveFileSettingsBtn: button, fileSettingsUsage: usage }[id] || null; } };
  const { renderFileSettings } = await import('../static/js/files.js');
  renderFileSettings({ retention_days: 7, revision: 4, used_bytes: 0, quota_bytes: 20 * 1024 ** 3 }, { role: 'viewer' });
  assert.equal(input.value, 7);
  assert.equal(input.disabled, true);
  assert.equal(button.disabled, true);
  renderFileSettings({ retention_days: 30, revision: 5, used_bytes: 0, quota_bytes: 20 * 1024 ** 3 }, { role: 'owner' });
  assert.equal(input.disabled, false);
  assert.equal(button.disabled, false);
});
