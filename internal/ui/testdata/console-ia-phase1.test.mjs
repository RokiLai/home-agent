import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';

// Global mocks for Node environment
globalThis.localStorage = {
  getItem() { return null; },
  setItem() {},
  removeItem() {}
};
globalThis.window = {
  location: { origin: 'http://homeagent.test', hash: '#/dashboard' },
  addEventListener() {},
  removeEventListener() {}
};

// Helper to mock DOM elements
function createElement(initial = {}) {
  const classes = new Set((initial.className || '').split(/\s+/).filter(Boolean));
  const attrs = new Map();
  const children = [];

  const el = {
    id: initial.id || '',
    tagName: (initial.tagName || 'div').toUpperCase(),
    dataset: initial.dataset || {},
    innerText: initial.innerText || '',
    innerHTML: initial.innerHTML || '',
    value: initial.value || '',
    title: initial.title || '',
    children,
    parentElement: null,
    className: initial.className || '',
    classList: {
      add(...names) {
        names.forEach(n => classes.add(n));
        el.className = [...classes].join(' ');
      },
      remove(...names) {
        names.forEach(n => classes.delete(n));
        el.className = [...classes].join(' ');
      },
      contains(n) {
        return classes.has(n);
      },
      toggle(n, force) {
        if (force === undefined) {
          if (classes.has(n)) classes.delete(n);
          else classes.add(n);
        } else if (force) {
          classes.add(n);
        } else {
          classes.delete(n);
        }
        el.className = [...classes].join(' ');
      }
    },
    setAttribute(k, v) { attrs.set(k, String(v)); },
    getAttribute(k) { return attrs.get(k) ?? null; },
    removeAttribute(k) { attrs.delete(k); },
    hasAttribute(k) { return attrs.has(k); },
    appendChild(c) {
      c.parentElement = el;
      children.push(c);
      return c;
    },
    removeChild(c) {
      const idx = children.indexOf(c);
      if (idx !== -1) {
        children.splice(idx, 1);
        c.parentElement = null;
      }
      return c;
    },
    querySelector(sel) {
      if (sel.startsWith('.')) {
        const c = sel.slice(1);
        return children.find(ch => ch.classList.contains(c)) || null;
      }
      return null;
    },
    querySelectorAll(sel) {
      if (sel.startsWith('.')) {
        const c = sel.slice(1);
        return children.filter(ch => ch.classList.contains(c));
      }
      return [];
    },
    focus() { el._focused = true; },
    blur() { el._focused = false; },
    addEventListener() {},
    removeEventListener() {},
    ...initial
  };
  return el;
}

// ---------------------------------------------------------------------------
// 1. Command and Sync Status Mapping Tests (Negative & Comprehensive Enum Tests)
// ---------------------------------------------------------------------------

test('command status mapping maps all model enums correctly and protects against unknown/undefined', async () => {
  const { mapCommandStatus, isTerminalCommandStatus } = await import('../static/js/commands.js');

  // Authoritative statuses from internal/command/model.go
  assert.equal(mapCommandStatus('queued'), '排队中');
  assert.equal(mapCommandStatus('dispatching'), '正在下发');
  assert.equal(mapCommandStatus('dispatched'), '已下发');
  assert.equal(mapCommandStatus('accepted'), '设备已接受');
  assert.equal(mapCommandStatus('succeeded'), '成功');
  assert.equal(mapCommandStatus('failed'), '失败');
  assert.equal(mapCommandStatus('timed_out'), '超时');
  assert.equal(mapCommandStatus('canceled'), '已取消');
  assert.equal(mapCommandStatus('interrupted'), '已中断');
  assert.equal(mapCommandStatus('legacy_untracked'), '旧客户端未关联');

  // Negative assertions: unknown, empty or malformed strings must NEVER report success
  assert.equal(mapCommandStatus('unknown_status'), '未知状态 (unknown_status)');
  assert.equal(mapCommandStatus(''), '未知状态');
  assert.equal(mapCommandStatus(null), '未知状态');
  assert.equal(mapCommandStatus(undefined), '未知状态');

  // Terminal status assertions per internal/command/model.go
  assert.equal(isTerminalCommandStatus('succeeded'), true);
  assert.equal(isTerminalCommandStatus('failed'), true);
  assert.equal(isTerminalCommandStatus('timed_out'), true);
  assert.equal(isTerminalCommandStatus('canceled'), true);
  assert.equal(isTerminalCommandStatus('interrupted'), true);
  assert.equal(isTerminalCommandStatus('legacy_untracked'), true); // terminal, but not success

  // Negative assertions for non-terminal statuses
  assert.equal(isTerminalCommandStatus('queued'), false);
  assert.equal(isTerminalCommandStatus('dispatching'), false);
  assert.equal(isTerminalCommandStatus('dispatched'), false);
  assert.equal(isTerminalCommandStatus('accepted'), false);
  assert.equal(isTerminalCommandStatus('arbitrary_unknown'), false);
});

// ---------------------------------------------------------------------------
// 2. Navigation & Routing Architecture Tests
// ---------------------------------------------------------------------------

test('router parses 4 primary navigations, backward-compatible redirects, and device detail route', async () => {
  const { parseRoute, switchPage, pageMeta } = await import('../static/js/router.js');

  // 1. Primary 4 tabs exist in pageMeta
  assert.ok(pageMeta.dashboard, 'dashboard must exist in pageMeta');
  assert.ok(pageMeta.devices, 'devices must exist in pageMeta');
  assert.ok(pageMeta.commands, 'commands must exist in pageMeta');
  assert.ok(pageMeta.settings, 'settings must exist in pageMeta');

  // 2. Route parsing
  assert.deepEqual(parseRoute('dashboard'), { page: 'dashboard', deviceId: null, section: null });
  assert.deepEqual(parseRoute('devices'), { page: 'devices', deviceId: null, section: null });
  assert.deepEqual(parseRoute('commands'), { page: 'commands', deviceId: null, section: null });
  assert.deepEqual(parseRoute('settings'), { page: 'settings', deviceId: null, section: null });

  // 3. Backward compatible routes
  assert.deepEqual(parseRoute('onboarding'), { page: 'onboarding', deviceId: null, section: null });
  assert.deepEqual(parseRoute('github'), { page: 'github', deviceId: null, section: null });
  assert.deepEqual(parseRoute('users'), { page: 'users', deviceId: null, section: null });

  // 4. Device detail routes: #/devices/<deviceId>/<section>
  assert.deepEqual(parseRoute('devices/mac-book-1/overview'), {
    page: 'deviceDetail',
    deviceId: 'mac-book-1',
    section: 'overview'
  });
  assert.deepEqual(parseRoute('devices/win-pc-2/health'), {
    page: 'deviceDetail',
    deviceId: 'win-pc-2',
    section: 'health'
  });

  // Default section when omitted
  assert.deepEqual(parseRoute('devices/server-linux-3'), {
    page: 'deviceDetail',
    deviceId: 'server-linux-3',
    section: 'overview'
  });

  // Negative test: invalid hash falls back to dashboard
  assert.deepEqual(parseRoute('non-existent-xyz-foo'), {
    page: 'dashboard',
    deviceId: null,
    section: null
  });
});

test('switchPage activates corresponding views and manages navigation classes', async () => {
  const domNodes = new Map();
  const pageViews = ['Dashboard', 'Devices', 'Commands', 'Settings', 'Onboarding', 'Github', 'Users', 'DeviceDetail'];

  pageViews.forEach(name => {
    domNodes.set(`page${name}`, createElement({ id: `page${name}`, className: 'page-view' }));
  });

  const navDashboard = createElement({ id: 'navDashboard', className: 'nav-item', dataset: { page: 'dashboard' } });
  const navDevices = createElement({ id: 'navDevices', className: 'nav-item', dataset: { page: 'devices' } });
  const navCommands = createElement({ id: 'navCommands', className: 'nav-item', dataset: { page: 'commands' } });
  const navSettings = createElement({ id: 'navSettings', className: 'nav-item', dataset: { page: 'settings' } });

  domNodes.set('navDashboard', navDashboard);
  domNodes.set('navDevices', navDevices);
  domNodes.set('navCommands', navCommands);
  domNodes.set('navSettings', navSettings);

  const titleEl = createElement({ id: 'currentPageTitle' });
  const descEl = createElement({ id: 'currentPageDesc' });
  domNodes.set('currentPageTitle', titleEl);
  domNodes.set('currentPageDesc', descEl);

  globalThis.document = {
    getElementById(id) { return domNodes.get(id) || null; },
    querySelectorAll(selector) {
      if (selector === '.nav-item') return [navDashboard, navDevices, navCommands, navSettings];
      if (selector === '.page-view') return pageViews.map(n => domNodes.get(`page${n}`));
      return [];
    }
  };

  const { switchPage } = await import('../static/js/router.js');
  const { state } = await import('../static/js/state.js');

  // Test 1: Switch to devices
  switchPage('devices');
  assert.equal(state.currentPage, 'devices');
  assert.equal(navDevices.classList.contains('active'), true);
  assert.equal(navDashboard.classList.contains('active'), false);
  assert.equal(domNodes.get('pageDevices').classList.contains('active'), true);
  assert.equal(domNodes.get('pageDashboard').classList.contains('active'), false);

  // Test 2: Switch to device detail -> parent nav remains devices
  switchPage('deviceDetail', { deviceId: 'dev-123', section: 'overview' });
  assert.equal(state.currentPage, 'deviceDetail');
  assert.equal(navDevices.classList.contains('active'), true, 'Parent nav for deviceDetail must be navDevices');
  assert.equal(domNodes.get('pageDeviceDetail').classList.contains('active'), true);
});

// ---------------------------------------------------------------------------
// 3. Dashboard Reduction & Focus Assertions
// ---------------------------------------------------------------------------

test('renderDashboardFocus renders actionable devices (<=10) sorted by offline then degraded, and recent commands (<=5)', async () => {
  const { renderDashboardFocus } = await import('../static/js/devices/render.js');
  const { state } = await import('../static/js/state.js');

  const pendingListEl = createElement({ id: 'dashboardActionableList' });
  const recentCommandsEl = createElement({ id: 'dashboardRecentCommandsList' });
  const healthStatsEl = createElement({ id: 'dashboardHealthStatsSummary' });

  globalThis.document = {
    getElementById(id) {
      if (id === 'dashboardActionableList') return pendingListEl;
      if (id === 'dashboardRecentCommandsList') return recentCommandsEl;
      if (id === 'dashboardHealthStatsSummary') return healthStatsEl;
      return null;
    },
    querySelectorAll() { return []; }
  };

  // Seed 12 devices with varying health
  state.devices = [
    { id: 'd-ok-1', hostname: 'host-ok-1', health: { status: 'healthy', reasons: [] } },
    { id: 'd-deg-1', hostname: 'host-deg-1', health: { status: 'degraded', reasons: [{ summary: '高内存占用' }, { summary: '磁盘接近已满' }] } },
    { id: 'd-off-1', hostname: 'host-off-1', health: { status: 'offline', reasons: [{ summary: '心跳超时' }] } },
    { id: 'd-off-2', hostname: 'host-off-2', health: { status: 'offline', reasons: [{ summary: '不可达' }] } },
    { id: 'd-deg-2', hostname: 'host-deg-2', health: { status: 'degraded', reasons: [{ summary: 'CPU 负载高' }] } }
  ];

  state.recentCommands = [
    { id: 'cmd-1', device_id: 'd-off-1', kind: 'wake', status: 'accepted', created_at: '2026-09-09T10:00:00Z' },
    { id: 'cmd-2', device_id: 'd-deg-1', kind: 'upgrade', status: 'succeeded', created_at: '2026-09-09T09:30:00Z' },
    { id: 'cmd-3', device_id: 'd-ok-1', kind: 'sync', status: 'failed', created_at: '2026-09-09T09:00:00Z' }
  ];

  renderDashboardFocus();

  // Assert actionable list order: offline first (d-off-1, d-off-2), then degraded (d-deg-1, d-deg-2)
  assert.match(pendingListEl.innerHTML, /host-off-1/);
  assert.match(pendingListEl.innerHTML, /host-off-2/);
  assert.match(pendingListEl.innerHTML, /host-deg-1/);
  assert.match(pendingListEl.innerHTML, /高内存占用/);
  assert.match(pendingListEl.innerHTML, /还有 1 项/); // remaining reasons count
  assert.doesNotMatch(pendingListEl.innerHTML, /host-ok-1/, 'Healthy device must not appear in actionable list');

  // Assert recent commands mapping
  assert.match(recentCommandsEl.innerHTML, /设备已接受/);
  assert.match(recentCommandsEl.innerHTML, /成功/);
  assert.match(recentCommandsEl.innerHTML, /失败/);

  // Assert stats summary
  assert.match(healthStatsEl.innerHTML, /正常 1/);
  assert.match(healthStatsEl.innerHTML, /异常 2/);
  assert.match(healthStatsEl.innerHTML, /离线 2/);
});

// ---------------------------------------------------------------------------
// 4. Device Detail Page Rendering & Tab Switching
// ---------------------------------------------------------------------------

test('renderDeviceDetail renders overview and health sections with missing metrics safeguard', async () => {
  const { renderDeviceDetailView } = await import('../static/js/devices/render.js');
  const { state } = await import('../static/js/state.js');

  const container = createElement({ id: 'deviceDetailContainer' });
  const title = createElement({ id: 'deviceDetailTitle' });
  const badge = createElement({ id: 'deviceDetailBadge' });

  globalThis.document = {
    getElementById(id) {
      if (id === 'deviceDetailContainer') return container;
      if (id === 'deviceDetailTitle') return title;
      if (id === 'deviceDetailBadge') return badge;
      return null;
    },
    querySelectorAll() { return []; }
  };

  const dev = {
    id: 'mac-prod-01',
    hostname: 'MacBook-Pro.local',
    alias: '开发笔记本',
    os: 'darwin',
    arch: 'arm64',
    mac: '02:42:ac:11:00:02',
    ssh_user: 'developer',
    ssh_port: 22,
    addresses: ['192.168.1.50'],
    connected: true,
    sync_status: 'synced',
    health: {
      status: 'degraded',
      reasons: [{ code: 'high_mem', severity: 'warning', summary: '内存使用率 89%' }],
      metrics: {
        cpu_usage: 12.5,
        sampled_at: '2026-09-09T10:00:00Z'
        // mem_usage is missing!
      }
    }
  };

  state.devices = [dev];

  // Render overview section
  renderDeviceDetailView('mac-prod-01', 'overview');
  assert.match(title.innerText, /开发笔记本/);
  assert.match(container.innerHTML, /MacBook-Pro\.local/);
  assert.match(container.innerHTML, /192\.168\.1\.50/);
  assert.match(container.innerHTML, /内存使用率 89%/);

  // Render health section
  renderDeviceDetailView('mac-prod-01', 'health');
  assert.match(container.innerHTML, /内存使用率 89%/);
  assert.match(container.innerHTML, /CPU/);
  // Negative assertion: missing memory metric must show fallback, not '0' or crash
  assert.match(container.innerHTML, /未上报/);

  // Negative assertion: non-existent device ID displays error state and provides back link
  renderDeviceDetailView('unknown-device-id-999', 'overview');
  assert.match(container.innerHTML, /未找到该设备或暂无访问权限/);
  assert.match(container.innerHTML, /href="#\/devices"/);
});
