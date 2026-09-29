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

test('server domain binding renders actions from config state and hides deleted bindings', async () => {
  const elements = new Map([
    ['serverDomainBindings', { dataset: {} }],
    ['serverDomainBindingProvider', { textContent: '' }],
    ['serverDomainBindingList', { innerHTML: '' }]
  ]);
  globalThis.window = { location: { origin: 'http://homeagent.test' } };
  globalThis.localStorage = { getItem() { return null; } };
  globalThis.document = { getElementById(id) { return elements.get(id) || null; } };
  globalThis.fetch = async () => new Response(JSON.stringify({ revision: 9, provider: { diagnostic: 'ready' }, bindings: [
    { binding_id: 'observing-1', fqdn: 'observe.rokilai.online', config_state: 'observing', runtime_state: 'waiting_report', revision: 1 },
    { binding_id: 'enabled-1', fqdn: 'enabled.rokilai.online', config_state: 'enabled', runtime_state: 'synced', revision: 2 },
    { binding_id: 'disabled-1', fqdn: 'disabled.rokilai.online', config_state: 'disabled', runtime_state: 'waiting_report', revision: 3 },
    { binding_id: 'deleted-1', fqdn: 'deleted.rokilai.online', config_state: 'deleted', runtime_state: 'waiting_report', revision: 4 }
  ] }), { status: 200, headers: { 'Content-Type': 'application/json' } });

  const module = await import('../static/js/settings.js');
  await module.loadServerDomainBindings();
  const rows = elements.get('serverDomainBindingList').innerHTML;
  assert.match(rows, /observe\.rokilai\.online[\s\S]*?确认接管[\s\S]*?删除/);
  assert.match(rows, /enabled\.rokilai\.online[\s\S]*?禁用[\s\S]*?删除/);
  assert.match(rows, /disabled\.rokilai\.online[\s\S]*?重新启用[\s\S]*?删除/);
  assert.doesNotMatch(rows, /deleted\.rokilai\.online/);
  assert.equal((rows.match(/>禁用<\/button>/g) || []).length, 1);
  assert.equal((rows.match(/>删除<\/button>/g) || []).length, 3);
  assert.equal((rows.match(/type="button"/g) || []).length, 6);
  assert.equal(elements.get('serverDomainBindings').dataset.revision, '9');
  assert.equal(elements.get('serverDomainBindingProvider').textContent, 'ready');
});

test('server domain binding actions confirm, send revision, and render refreshed state', async () => {
  const elements = new Map([
    ['serverDomainBindings', { dataset: {} }],
    ['serverDomainBindingProvider', { textContent: '' }],
    ['serverDomainBindingList', { innerHTML: '' }]
  ]);
  globalThis.document = { getElementById(id) { return elements.get(id) || null; } };
  const confirmations = [];
  globalThis.confirm = message => { confirmations.push(message); return true; };
  const requests = [];
  let bindings = [{ binding_id: 'binding-1', fqdn: 'files.rokilai.online', config_state: 'disabled', runtime_state: 'waiting_report', revision: 4 }];
  globalThis.fetch = async (url, options = {}) => {
    requests.push({ url, options });
    if (options.method === 'POST' && url.endsWith('/enable')) {
      bindings = [{ ...bindings[0], config_state: 'enabled', revision: 5 }];
    } else if (options.method === 'POST' && url.endsWith('/disable')) {
      bindings = [{ ...bindings[0], config_state: 'disabled', revision: 6 }];
    } else if (options.method === 'DELETE') {
      bindings = [];
    }
    return new Response(JSON.stringify({ revision: 10, bindings }), { status: 200, headers: { 'Content-Type': 'application/json' } });
  };

  const module = await import('../static/js/settings.js');
  await module.enableServerDomainBinding('binding-1', 4);
  assert.equal(requests[0].url, '/api/v1/server/domain-bindings/binding-1/enable');
  assert.equal(requests[0].options.method, 'POST');
  assert.deepEqual(JSON.parse(requests[0].options.body), { expected_revision: 4, publisher_disabled_confirmed: true });
  assert.equal(requests[1].url, '/api/v1/server/domain-bindings');
  assert.match(elements.get('serverDomainBindingList').innerHTML, /enabled \/ waiting_report/);

  requests.length = 0;
  await module.disableServerDomainBinding('binding-1', 5);
  assert.equal(requests[0].url, '/api/v1/server/domain-bindings/binding-1/disable');
  assert.deepEqual(JSON.parse(requests[0].options.body), { expected_revision: 5 });
  assert.equal(requests[1].url, '/api/v1/server/domain-bindings');
  assert.match(elements.get('serverDomainBindingList').innerHTML, /disabled \/ waiting_report/);

  requests.length = 0;
  await module.deleteServerDomainBinding('binding-1', 6);
  assert.equal(requests[0].url, '/api/v1/server/domain-bindings/binding-1');
  assert.equal(requests[0].options.method, 'DELETE');
  assert.deepEqual(JSON.parse(requests[0].options.body), { expected_revision: 6 });
  assert.equal(requests[1].url, '/api/v1/server/domain-bindings');
  assert.match(elements.get('serverDomainBindingList').innerHTML, /暂无受管域名/);
  assert.equal(elements.get('serverDomainBindings').dataset.revision, '10');
  assert.match(confirmations[0], /其他发布者/);
  assert.match(confirmations[1], /不会自动删除/);
  assert.match(confirmations[2], /不会删除.*AAAA/);
});

test('server domain binding cancellation and failures have no false success', async () => {
  const toast = { classList: { remove() {}, add() {} } };
  const toastMsg = { innerText: '' };
  const list = { innerHTML: 'original' };
  globalThis.document = { getElementById(id) { return { toast, toastMsg, serverDomainBindingList: list, serverDomainBindings: { dataset: {} } }[id] || null; } };
  const module = await import('../static/js/settings.js');
  globalThis.confirm = () => false;
  let requestCount = 0;
  globalThis.fetch = async () => { requestCount++; return new Response('{}'); };
  await module.enableServerDomainBinding('binding-1', 4);
  await module.disableServerDomainBinding('binding-1', 4);
  await module.deleteServerDomainBinding('binding-1', 4);
  assert.equal(requestCount, 0);
  assert.equal(list.innerHTML, 'original');

  globalThis.confirm = () => true;
  for (const status of [403, 404, 409, 500]) {
    requestCount = 0;
    globalThis.fetch = async () => {
      requestCount++;
      return requestCount === 1
        ? new Response(JSON.stringify({ error: `error-${status}` }), { status, headers: { 'Content-Type': 'application/json' } })
        : new Response(JSON.stringify({ revision: 12, bindings: [] }), { status: 200, headers: { 'Content-Type': 'application/json' } });
    };
    list.innerHTML = 'original';
    await module.deleteServerDomainBinding('binding-1', 4);
    assert.equal(toastMsg.innerText, `error-${status}`);
    assert.equal(requestCount, status === 404 || status === 409 ? 2 : 1);
    assert.equal(list.innerHTML, status === 404 || status === 409 ? '<div class="text-muted font-sm">暂无受管域名</div>' : 'original');
  }

  requestCount = 0;
  list.innerHTML = 'original';
  globalThis.fetch = async () => { requestCount++; return new Response('{}', { status: 401 }); };
  await module.deleteServerDomainBinding('binding-1', 4);
  assert.equal(requestCount, 1);
  assert.equal(toastMsg.innerText, '登录状态已失效');
  assert.equal(list.innerHTML, 'original');

  requestCount = 0;
  list.innerHTML = 'original';
  globalThis.fetch = async () => { requestCount++; throw new Error('offline'); };
  await module.deleteServerDomainBinding('binding-1', 4);
  assert.equal(requestCount, 1);
  assert.match(toastMsg.innerText, /offline/);
  assert.equal(list.innerHTML, 'original');
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
