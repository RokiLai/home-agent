import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', 'static');
const html = fs.readFileSync(path.join(root, 'index.html'), 'utf8');
const settings = fs.readFileSync(path.join(root, 'js/settings.js'), 'utf8');
const actions = fs.readFileSync(path.join(root, 'js/devices/actions.js'), 'utf8');
const render = fs.readFileSync(path.join(root, 'js/devices/render.js'), 'utf8');

test('server and device domain binding UI expose preflight and explicit confirmation flow', () => {
  assert.match(html, /serverDomainBindings/);
  assert.match(html, /DNS AAAA 不会自动删除/);
  assert.match(settings, /domain-bindings\/preflight/);
  assert.match(settings, /existing_record/);
  assert.match(render, /deviceDomainBindings/);
  assert.match(actions, /domain-bindings\/preflight/);
  assert.match(actions, /publisher_disabled_confirmed/);
});

test('device domain binding renders lifecycle actions and executes revision-safe requests', async () => {
  const elements = new Map([
    ['deviceDomainBindings', { dataset: {} }],
    ['deviceDomainBindingList', { innerHTML: '', textContent: '' }]
  ]);
  globalThis.window = { location: { origin: 'http://homeagent.test' } };
  globalThis.localStorage = { getItem() { return null; } };
  globalThis.document = { getElementById(id) { return elements.get(id) || null; } };
  globalThis.confirm = () => true;

  const requests = [];
  const bindings = [
    { binding_id: 'observing-1', fqdn: 'observe.rokilai.online', config_state: 'observing', runtime_state: 'waiting_report', revision: 1 },
    { binding_id: 'enabled-1', fqdn: 'enabled.rokilai.online', config_state: 'enabled', runtime_state: 'synced', revision: 2 },
    { binding_id: 'disabled-1', fqdn: 'disabled.rokilai.online', config_state: 'disabled', runtime_state: 'waiting_report', revision: 3 }
  ];
  globalThis.fetch = async (url, options = {}) => {
    requests.push({ url, options });
    return new Response(JSON.stringify({ revision: 9, bindings }), { status: 200, headers: { 'Content-Type': 'application/json' } });
  };

  const module = await import('../static/js/devices/actions.js');
  await module.loadDeviceDomainBindings('device-1');
  const html = elements.get('deviceDomainBindingList').innerHTML;
  assert.match(html, /确认接管/);
  assert.match(html, /禁用/);
  assert.match(html, /重新启用/);
  assert.equal((html.match(/删除/g) || []).length, 3);
  assert.equal(elements.get('deviceDomainBindings').dataset.revision, '9');

  requests.length = 0;
  await module.disableDeviceDomainBinding('device-1', 'enabled-1', 2);
  assert.equal(requests[0].url, '/api/v1/devices/device-1/domain-bindings/enabled-1/disable');
  assert.deepEqual(JSON.parse(requests[0].options.body), { expected_revision: 2 });
  assert.match(requests[1].url, /\/api\/v1\/devices\/device-1\/domain-bindings$/);

  requests.length = 0;
  await module.enableDeviceDomainBinding('device-1', 'disabled-1', 3);
  assert.equal(requests[0].url, '/api/v1/devices/device-1/domain-bindings/disabled-1/enable');
  assert.deepEqual(JSON.parse(requests[0].options.body), { expected_revision: 3, publisher_disabled_confirmed: true });

  requests.length = 0;
  await module.deleteDeviceDomainBinding('device-1', 'observing-1', 1);
  assert.equal(requests[0].options.method, 'DELETE');
  assert.deepEqual(JSON.parse(requests[0].options.body), { expected_revision: 1 });
});

test('device domain binding cancellation performs no write request', async () => {
  globalThis.confirm = () => false;
  let called = false;
  globalThis.fetch = async () => { called = true; return new Response('{}'); };
  const module = await import('../static/js/devices/actions.js');
  await module.disableDeviceDomainBinding('device-1', 'binding-1', 1);
  await module.enableDeviceDomainBinding('device-1', 'binding-1', 1);
  await module.deleteDeviceDomainBinding('device-1', 'binding-1', 1);
  assert.equal(called, false);
});

test('device domain binding surfaces API conflict without speculative reload', async () => {
  const toast = { classList: { remove() {}, add() {} } };
  const toastMsg = { innerText: '' };
  globalThis.document = { getElementById(id) { return { toast, toastMsg }[id] || null; } };
  globalThis.confirm = () => true;
  let requestCount = 0;
  globalThis.fetch = async () => {
    requestCount++;
    return new Response(JSON.stringify({ error: 'binding revision conflict' }), { status: 409, headers: { 'Content-Type': 'application/json' } });
  };
  const module = await import('../static/js/devices/actions.js');
  await module.disableDeviceDomainBinding('device-1', 'binding-1', 4);
  assert.equal(requestCount, 1);
  assert.equal(toastMsg.innerText, 'binding revision conflict');
});
