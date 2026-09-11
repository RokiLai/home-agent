import test from 'node:test';
import assert from 'node:assert/strict';

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
      }
    },
    setAttribute(k, v) { attrs.set(k, String(v)); },
    getAttribute(k) { return attrs.get(k) ?? null; },
    removeAttribute(k) { attrs.delete(k); },
    appendChild(c) {
      c.parentElement = el;
      children.push(c);
      return c;
    },
    querySelectorAll(sel) {
      if (sel.startsWith('.')) {
        const c = sel.slice(1);
        return children.filter(ch => ch.classList && ch.classList.contains(c));
      }
      return [];
    },
    ...initial
  };
  return el;
}

// ---------------------------------------------------------------------------
// 1. Router Route Parsing for all 6 Detail Sections
// ---------------------------------------------------------------------------

test('router parseRoute parses all 6 device detail sections and normalizes invalid section to overview', async () => {
  const { parseRoute } = await import('../static/js/router.js');

  const validSections = ['overview', 'health', 'ssh', 'network', 'commands', 'settings'];
  for (const sec of validSections) {
    const route = parseRoute(`devices/dev-node-1/${sec}`);
    assert.equal(route.page, 'deviceDetail');
    assert.equal(route.deviceId, 'dev-node-1');
    assert.equal(route.section, sec);
  }

  // Invalid section fallback
  const fallbackRoute = parseRoute('devices/dev-node-1/unknown_sub_tab');
  assert.equal(fallbackRoute.page, 'deviceDetail');
  assert.equal(fallbackRoute.deviceId, 'dev-node-1');
  assert.equal(fallbackRoute.section, 'overview');
});

// ---------------------------------------------------------------------------
// 2. Rendering of SSH, Network, Commands, and Settings Sections
// ---------------------------------------------------------------------------

test('renderDeviceDetailView renders ssh, network, commands, and settings sections with data contracts', async () => {
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
    id: 'server-nas-01',
    hostname: 'NAS-Storage.local',
    alias: '家庭 NAS',
    os: 'linux',
    arch: 'arm64',
    mac: '12:34:56:78:9a:bc',
    ssh_user: 'admin',
    ssh_port: 2200,
    addresses: ['192.168.2.10', '2408:8207::1'],
    ddns_domain: 'nas.homeagent.io',
    sync_status: 'synced',
    owner_user_id: 'user-owner-1',
    shared_users: [{ user_id: 'user-guest-2', username: 'guest', permission: 'view' }],
    health: {
      status: 'healthy',
      reasons: [],
      metrics: { cpu_usage: 5.2, mem_usage: 42.0, sampled_at: '2026-09-11T12:00:00Z' }
    }
  };

  state.devices = [dev];
  state.recentCommands = [
    { id: 'cmd-1', device_id: 'server-nas-01', kind: 'ssh_keys', status: 'succeeded', created_at: '2026-09-11T10:00:00Z' },
    { id: 'cmd-2', device_id: 'other-device-99', kind: 'upgrade', status: 'failed', created_at: '2026-09-11T09:00:00Z' },
    { id: 'cmd-3', device_id: 'server-nas-01', kind: 'shutdown', status: 'accepted', created_at: '2026-09-11T08:00:00Z' }
  ];

  // A. Test SSH Section
  renderDeviceDetailView('server-nas-01', 'ssh');
  assert.match(container.innerHTML, /admin:2200/);
  assert.match(container.innerHTML, /ssh -p 2200 admin@192\.168\.2\.10/);
  assert.match(container.innerHTML, /SYNCED|已同步/);
  assert.match(container.innerHTML, /btn-detail-sync/);
  assert.match(container.innerHTML, /guest/);

  // B. Test Network Section
  renderDeviceDetailView('server-nas-01', 'network');
  assert.match(container.innerHTML, /192\.168\.2\.10/);
  assert.match(container.innerHTML, /2408:8207::1/);
  assert.match(container.innerHTML, /12:34:56:78:9a:bc/);
  assert.match(container.innerHTML, /nas\.homeagent\.io/);

  // C. Test Commands Section (Filtered only for this device!)
  renderDeviceDetailView('server-nas-01', 'commands');
  assert.match(container.innerHTML, /SSH 密钥同步|ssh_keys/);
  assert.match(container.innerHTML, /成功/);
  assert.match(container.innerHTML, /远程关机|shutdown/);
  assert.match(container.innerHTML, /设备已接受/);
  // Negative assertion: other device's commands must NOT leak into this view
  assert.doesNotMatch(container.innerHTML, /other-device-99/);

  // D. Test Settings Section (Danger zone exists)
  renderDeviceDetailView('server-nas-01', 'settings');
  assert.match(container.innerHTML, /家庭 NAS/);
  assert.match(container.innerHTML, /user-owner-1/);
  assert.match(container.innerHTML, /danger-zone/);
  assert.match(container.innerHTML, /btn-detail-shutdown/);
  assert.match(container.innerHTML, /btn-detail-remove/);
});

// ---------------------------------------------------------------------------
// 3. Danger Action Confirmation Safeguards (Negative Assertions)
// ---------------------------------------------------------------------------

test('handleDetailShutdown and handleDetailRemove require explicit confirm, cancellation issues zero requests', async () => {
  const { handleDetailShutdown, handleDetailRemove } = await import('../static/js/devices/actions.js');

  let fetchCalled = false;
  globalThis.fetch = async () => {
    fetchCalled = true;
    return new Response(JSON.stringify({ success: true }));
  };

  // Mock confirm returning FALSE (user cancels dialog)
  globalThis.confirm = () => false;

  // Attempt shutdown with user cancel
  await handleDetailShutdown('server-nas-01', 'NAS-Storage');
  assert.equal(fetchCalled, false, 'Canceling confirmation must NOT send any shutdown request');

  // Attempt remove with user cancel
  await handleDetailRemove('server-nas-01', 'NAS-Storage');
  assert.equal(fetchCalled, false, 'Canceling confirmation must NOT send any remove request');
});
