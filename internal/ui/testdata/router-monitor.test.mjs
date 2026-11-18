import assert from 'node:assert/strict';
import test from 'node:test';

globalThis.localStorage = { getItem: () => null };
globalThis.window = { location: { origin: 'https://homeagent.test' } };
const nodes = new Map();
const element = () => ({
  textContent: '', children: [],
  replaceChildren() { this.children = []; },
  append(item) { this.children.push(item); }
});
for (const id of ['Status', 'Updated', 'Checks', 'Refresh']) nodes.set('routerMonitor' + id, element());
globalThis.document = { getElementById: id => nodes.get(id), createElement: element };
const { state } = await import('../static/js/state.js');
const { openRouterMonitor } = await import('../static/js/router-monitor.js');

test('snapshot renders, refresh labels retained data and viewer cannot request', async () => {
  state.isAuthenticated = true;
  state.currentPage = 'routerMonitor';
  state.currentUser.role = 'owner';
  const view = {
    state: 'monitoring', last_success: '2026-11-18T08:00:00Z',
    snapshot: { checks: [{ component: 'learned_sets', status: 'healthy', detail: 'ok' }] }
  };
  let requests = 0;
  globalThis.fetch = async (url, opts) => {
    assert.equal(url, '/api/v1/router-monitor');
    assert.equal(opts.credentials, 'same-origin');
    requests++;
    return new Response(JSON.stringify(view));
  };
  await openRouterMonitor();
  assert.equal(nodes.get('routerMonitorChecks').children[0].textContent, 'IPv4/IPv6 HTTPS 学习分流：正常');
  assert.match(nodes.get('routerMonitorUpdated').textContent, /最后成功采集/);
  view.state = 'collection_failed';
  await nodes.get('routerMonitorRefresh').onclick();
  assert.equal(nodes.get('routerMonitorStatus').textContent, '采集失败');
  assert.match(nodes.get('routerMonitorChecks').children[0].textContent, /上次 正常/);
  state.currentUser.role = 'viewer';
  await nodes.get('routerMonitorRefresh').onclick();
  assert.equal(nodes.get('routerMonitorStatus').textContent, '无权查看路由器监控');
  assert.equal(requests, 2);
});
