import assert from 'node:assert/strict';
import test from 'node:test';

globalThis.localStorage = { getItem: () => null };
globalThis.window = { location: { origin: 'https://homeagent.test' } };

class MockElement {
  constructor(id = '', tag = 'div') {
    this.id = id;
    this.tagName = tag.toUpperCase();
    this._textContent = '';
    this.className = '';
    this.classList = new Set();
    this.children = [];
    this.attributes = new Map();
    this.disabled = false;
    this.onclick = null;
  }
  get textContent() {
    if (this.children.length === 0) return this._textContent;
    return this.children.map(c => c.textContent).join('');
  }
  set textContent(v) {
    this._textContent = String(v);
    this.children = [];
  }
  setAttribute(k, v) { this.attributes.set(k, String(v)); }
  getAttribute(k) { return this.attributes.get(k); }
  removeAttribute(k) { this.attributes.delete(k); }
  replaceChildren(...items) {
    this.children = [];
    for (const item of items) this.append(item);
  }
  append(...items) {
    for (const item of items) {
      if (typeof item === 'string') {
        const textNode = new MockElement('', '#text');
        textNode.textContent = item;
        this.children.push(textNode);
      } else {
        this.children.push(item);
      }
    }
  }
  querySelector(sel) {
    for (const child of this.children) {
      if (sel.startsWith('.') && child.className.includes(sel.slice(1))) return child;
      if (sel.startsWith('#') && child.id === sel.slice(1)) return child;
      const found = child.querySelector?.(sel);
      if (found) return found;
    }
    return null;
  }
  querySelectorAll(sel) {
    const res = [];
    for (const child of this.children) {
      if (sel.startsWith('.') && child.className.includes(sel.slice(1))) res.push(child);
      if (sel.startsWith('#') && child.id === sel.slice(1)) res.push(child);
      if (child.querySelectorAll) res.push(...child.querySelectorAll(sel));
    }
    return res;
  }
}

const nodeIds = [
  'routerMonitorStatus', 'routerMonitorUpdated', 'routerMonitorChecks', 'routerMonitorRefresh',
  'routerMonitorSummary', 'routerMonitorOverviewHealth', 'routerMonitorOverviewMode',
  'routerMonitorOverviewReceived', 'routerMonitorOverviewSuccess', 'routerMonitorFlow',
  'routerMonitorPipeline', 'routerMonitorCards', 'routerMonitorRecovery', 'routerMonitorRecoveryNotice'
];

const nodes = new Map();
for (const id of nodeIds) nodes.set(id, new MockElement(id));
globalThis.document = {
  getElementById: id => nodes.get(id) || null,
  createElement: tag => new MockElement('', tag)
};

const { state } = await import('../static/js/state.js');
const { openRouterMonitor } = await import('../static/js/router-monitor.js');

function resetMocks() {
  for (const el of nodes.values()) {
    el.textContent = '';
    el.className = '';
    el.children = [];
    el.disabled = false;
  }
}

test('unconfigured state displays unconfigured text, unobservable flow and retains existing entries', async () => {
  resetMocks();
  state.isAuthenticated = true;
  state.currentPage = 'routerMonitor';
  state.currentUser.role = 'owner';
  globalThis.fetch = async () => new Response(JSON.stringify({
    state: 'unconfigured',
    received: '',
    last_success: '',
    snapshot: null
  }));

  await openRouterMonitor();
  assert.equal(nodes.get('routerMonitorStatus').textContent, '尚未配置采集节点');
  assert.match(nodes.get('routerMonitorOverviewHealth').textContent, /未配置/);
  assert.match(nodes.get('routerMonitorRecoveryNotice').textContent, /恢复事件尚未接入/);
  assert.equal(nodes.get('routerMonitorRefresh').disabled, false);
});

test('monitoring state with all healthy components displays all healthy, renders pipeline and cards', async () => {
  resetMocks();
  state.isAuthenticated = true;
  state.currentPage = 'routerMonitor';
  state.currentUser.role = 'admin';
  const payload = {
    state: 'monitoring',
    received: '2026-11-18T08:00:05Z',
    last_success: '2026-11-18T08:00:00Z',
    snapshot: {
      mode: 'monitoring',
      current_uptime: 1000,
      checked_uptime: 995,
      checks: [
        { component: 'lan', status: 'healthy', detail: 'ok' },
        { component: 'wifi', status: 'healthy', detail: 'ok' },
        { component: 'resources', status: 'healthy', detail: 'ok' },
        { component: 'wan', status: 'healthy', detail: 'ok' },
        { component: 'dhcp', status: 'healthy', detail: 'ok' },
        { component: 'ipv6', status: 'healthy', detail: 'ok' },
        { component: 'shell', status: 'healthy', detail: 'ok' },
        { component: 'dns', status: 'healthy', detail: 'ok' },
        { component: 'proxy', status: 'healthy', detail: 'ok' },
        { component: 'learner', status: 'healthy', detail: 'ok' },
        { component: 'learned_sets', status: 'healthy', detail: 'ok' }
      ]
    }
  };
  globalThis.fetch = async () => new Response(JSON.stringify(payload));

  await openRouterMonitor();
  assert.equal(nodes.get('routerMonitorStatus').textContent, '监控运行中');
  assert.equal(nodes.get('routerMonitorOverviewHealth').textContent, '全部正常');
  assert.match(nodes.get('routerMonitorUpdated').textContent, /最后成功采集/);
  // 兼容旧入口
  assert.equal(nodes.get('routerMonitorChecks').children.length, 11);
  // 新卡片区域
  assert.ok(nodes.get('routerMonitorCards').children.length >= 11);
  // 流程节点检查
  const pipeline = nodes.get('routerMonitorPipeline');
  assert.ok(pipeline.children.length >= 5);
  // 恢复区域明确标明尚未接入
  assert.match(nodes.get('routerMonitorRecoveryNotice').textContent, /恢复事件尚未接入/);
});

test('missing, skipped or unknown component prevents all-healthy summary and maps status correctly', async () => {
  resetMocks();
  state.isAuthenticated = true;
  state.currentPage = 'routerMonitor';
  state.currentUser.role = 'owner';
  const payload = {
    state: 'monitoring',
    received: '2026-11-18T08:00:05Z',
    last_success: '2026-11-18T08:00:00Z',
    snapshot: {
      mode: 'monitoring',
      checks: [
        { component: 'lan', status: 'healthy', detail: 'ok' },
        { component: 'proxy', status: 'skipped', detail: 'disabled' },
        { component: 'custom_daemon', status: 'weird_val', detail: 'testing' }
      ]
    }
  };
  globalThis.fetch = async () => new Response(JSON.stringify(payload));

  await openRouterMonitor();
  assert.equal(nodes.get('routerMonitorOverviewHealth').textContent, '检查不完整');
  const cards = nodes.get('routerMonitorCards');
  const cardTexts = cards.children.map(c => c.textContent).join(' ');
  assert.match(cardTexts, /跳过/);
  assert.match(cardTexts, /未知/);
  assert.match(cardTexts, /custom_daemon/);
});

test('failure or stale retains last success snapshot, marks historical, does not claim live health', async () => {
  resetMocks();
  state.isAuthenticated = true;
  state.currentPage = 'routerMonitor';
  state.currentUser.role = 'owner';
  const payload = {
    state: 'collection_failed',
    received: '2026-11-18T08:05:00Z',
    last_success: '2026-11-18T08:00:00Z',
    snapshot: {
      checks: [{ component: 'wan', status: 'healthy', detail: 'ok' }]
    }
  };
  globalThis.fetch = async () => new Response(JSON.stringify(payload));

  await openRouterMonitor();
  assert.equal(nodes.get('routerMonitorStatus').textContent, '采集失败');
  assert.match(nodes.get('routerMonitorOverviewHealth').textContent, /采集异常/);
  assert.match(nodes.get('routerMonitorChecks').children[0].textContent, /上次 正常/);
});

test('concurrent requests, page switch, logout and unauthorized role protection', async () => {
  resetMocks();
  state.isAuthenticated = false;
  await openRouterMonitor();
  assert.equal(nodes.get('routerMonitorStatus').textContent, '请先登录');

  state.isAuthenticated = true;
  state.currentUser.role = 'viewer';
  await openRouterMonitor();
  assert.equal(nodes.get('routerMonitorStatus').textContent, '无权查看路由器监控');

  // 页面离开时旧响应丢弃
  state.currentUser.role = 'owner';
  let finishFetch;
  globalThis.fetch = () => new Promise(resolve => { finishFetch = resolve; });
  const pendingPromise = openRouterMonitor();
  state.currentPage = 'dashboard';
  finishFetch(new Response(JSON.stringify({ state: 'monitoring' })));
  await pendingPromise;
  assert.notEqual(nodes.get('routerMonitorStatus').textContent, '监控运行中');
});
