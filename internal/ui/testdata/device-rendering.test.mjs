import test from 'node:test';
import assert from 'node:assert/strict';

function element(initial = {}) {
  return {
    innerHTML: '',
    innerText: '',
    value: '',
    title: '',
    className: '',
    classList: {
      add() {},
      remove() {},
      contains() { return false; }
    },
    ...initial
  };
}

test('a real device-list response replaces the loading state without render errors', async () => {
  const nodes = new Map([
    ['deviceContainer', element({
      innerHTML: '<div class="loading-state">正在拉取设备接入列表...</div>'
    })],
    ['deviceSearchInput', element()],
    ['dashboardDeviceSummary', element()],
    ['liveStatusText', element()]
  ]);

  globalThis.window = { location: { origin: 'http://homeagent.test' } };
  globalThis.localStorage = { getItem() { return null; } };
  globalThis.document = {
    getElementById(id) { return nodes.get(id) || null; },
    querySelectorAll() { return []; }
  };

  const apiResponse = {
    devices: [{
      id: 'macbook-pro-8-local-0e93c101',
      hostname: 'MacBook-Pro-8.local',
      alias: 'MacBook Pro',
      mac: '02:00:00:aa:bb:cc',
      agent_version: 'v0.5.4',
      os: 'darwin',
      arch: 'amd64',
      ssh_user: 'exampleuser',
      ssh_port: 22,
      addresses: ['192.168.50.174', '2001:db8:1234:10:cfd:54f6:b6bd:b196'],
      sync_status: 'synced',
      applied_hash: '7f1de429d25f72af51a698ff107e1e9f83f9d5c28078604c34ab1594dc2863da',
      github_sync_enabled: true,
      github_status: 'synced',
      connected: true,
      health: {
        status: 'degraded',
        reasons: [{
          code: 'agent_version_outdated',
          severity: 'warning',
          summary: 'Agent 版本落后于推荐版本'
        }]
      }
    }],
    server_hash: '7f1de429d25f72af51a698ff107e1e9f83f9d5c28078604c34ab1594dc2863da'
  };

  globalThis.fetch = async url => {
    assert.equal(url, '/api/v1/devices');
    return new Response(JSON.stringify(apiResponse), {
      status: 200,
      headers: { 'Content-Type': 'application/json' }
    });
  };

  const renderErrors = [];
  const originalConsoleError = console.error;
  console.error = (...args) => renderErrors.push(args);

  try {
    const { fetchDevices } = await import('../static/js/devices/actions.js');
    const { state } = await import('../static/js/state.js');

    await fetchDevices();

    assert.deepEqual(state.devices, apiResponse.devices);
    assert.equal(state.serverHash, apiResponse.server_hash);
    assert.equal(renderErrors.length, 0, `unexpected render errors: ${renderErrors.flat().join(' ')}`);
    assert.doesNotMatch(nodes.get('deviceContainer').innerHTML, /正在拉取设备接入列表/);
    assert.match(nodes.get('deviceContainer').innerHTML, /MacBook Pro/);
    assert.match(nodes.get('deviceContainer').innerHTML, /ssh exampleuser@192\.168\.50\.174/);
  } finally {
    console.error = originalConsoleError;
  }
});

test('device with succeeded sync_status is treated as synced in UI helpers', async () => {
  const { getDeviceSyncStatus, isDeviceSynced } = await import('../static/js/devices/render.js');

  const devSucceeded = { id: 'dev-1', sync_status: 'succeeded' };
  const devSynced = { id: 'dev-2', sync_status: 'synced' };
  const devPending = { id: 'dev-3', sync_status: 'pending' };
  const devEmpty = { id: 'dev-4' };

  assert.equal(getDeviceSyncStatus(devSucceeded), 'synced');
  assert.equal(isDeviceSynced(devSucceeded), true);

  assert.equal(getDeviceSyncStatus(devSynced), 'synced');
  assert.equal(isDeviceSynced(devSynced), true);

  assert.equal(getDeviceSyncStatus(devPending), 'pending');
  assert.equal(isDeviceSynced(devPending), false);

  assert.equal(getDeviceSyncStatus(devEmpty), 'pending');
  assert.equal(isDeviceSynced(devEmpty), false);
});

test('createDeviceCardHTML omits online-badge and retains health badge and sync badge (negative assertions)', async () => {
  const { createDeviceCardHTML } = await import('../static/js/devices/render.js');

  // Case 1: Online device
  const onlineDev = {
    id: 'dev-online-1',
    hostname: 'macbook.local',
    alias: '开发机',
    mac: '11:22:33:44:55:66',
    connected: true,
    sync_status: 'synced',
    health: { status: 'healthy', reasons: [] }
  };
  const htmlOnline = createDeviceCardHTML(onlineDev);
  assert.doesNotMatch(htmlOnline, /online-badge/i, 'Device card must NOT contain .online-badge');
  assert.doesNotMatch(htmlOnline, />\s*在线\s*</, 'Device card header must NOT display "在线" badge text');
  assert.match(htmlOnline, /class="[^"]*btn-view-health[^"]*"/, 'Device card must retain .btn-view-health');
  assert.match(htmlOnline, /class="[^"]*status-badge[^"]*"/, 'Device card must retain .status-badge');

  // Case 2: Disconnected device
  const offlineDev = {
    id: 'dev-offline-2',
    hostname: 'server.local',
    connected: false,
    sync_status: 'pending',
    health: { status: 'offline', reasons: [{ summary: 'Agent 活跃度心跳丢失' }] }
  };
  const htmlOffline = createDeviceCardHTML(offlineDev);
  assert.doesNotMatch(htmlOffline, /online-badge/i, 'Device card must NOT contain .online-badge');
  assert.doesNotMatch(htmlOffline, />\s*离线\s*<\/span>/, 'Device card header must NOT display "离线" connectivity badge');
  // Health OFFLINE badge should still be rendered as health status
  assert.match(htmlOffline, /health-offline/, 'Health OFFLINE status must continue to be rendered');
  assert.match(htmlOffline, />\s*OFFLINE\s*</, 'Health status OFFLINE text must be rendered in health badge');
});

test('renderDevices normalizes invalid or legacy filter values (online/offline/unknown) to all', async () => {
  const { renderDevices } = await import('../static/js/devices/render.js');
  const { state } = await import('../static/js/state.js');

  function makePill(filter, active = false) {
    const classes = new Set(active ? ['active'] : []);
    return {
      dataset: { filter },
      classList: {
        add(...names) { names.forEach(n => classes.add(n)); },
        remove(...names) { names.forEach(n => classes.delete(n)); },
        contains(n) { return classes.has(n); },
        has(n) { return classes.has(n); }
      }
    };
  }

  const pillNodes = [
    makePill('all', true),
    makePill('healthy', false),
    makePill('degraded', false),
    makePill('synced', false),
    makePill('pending', false)
  ];

  const container = element();
  const searchInput = element({ value: '' });

  globalThis.document = {
    getElementById(id) {
      if (id === 'deviceContainer') return container;
      if (id === 'deviceSearchInput') return searchInput;
      return null;
    },
    querySelectorAll(selector) {
      if (selector && selector.includes('filter-pill')) {
        return pillNodes;
      }
      return [];
    }
  };

  state.devices = [
    { id: 'd1', hostname: 'host-1', connected: true, health: { status: 'healthy' } },
    { id: 'd2', hostname: 'host-2', connected: false, health: { status: 'degraded' } }
  ];

  // Test legacy 'online' filter normalization
  state.currentFilter = 'online';
  pillNodes[0].classList.remove('active');
  pillNodes[1].classList.add('active');

  renderDevices();

  assert.equal(state.currentFilter, 'all', 'state.currentFilter must be normalized to "all"');
  assert.equal(pillNodes[0].classList.contains('active'), true, '"all" pill must have active class');
  assert.equal(pillNodes[1].classList.contains('active'), false, 'non-all pill must not have active class');
  assert.match(container.innerHTML, /host-1/);
  assert.match(container.innerHTML, /host-2/);

  // Test legacy 'offline' filter normalization
  state.currentFilter = 'offline';
  renderDevices();
  assert.equal(state.currentFilter, 'all');

  // Test arbitrary invalid filter normalization
  state.currentFilter = 'unknown_filter_xxx';
  renderDevices();
  assert.equal(state.currentFilter, 'all');
});

test('isDeviceOnline evaluates connected property and falls back to updated_at when missing', async () => {
  const { isDeviceOnline } = await import('../static/js/devices/render.js');

  // Explicit connected
  assert.equal(isDeviceOnline({ connected: true }), true);
  assert.equal(isDeviceOnline({ connected: false }), false);

  // Fallback to updated_at (within 90s is online)
  const recent = new Date(Date.now() - 30 * 1000).toISOString();
  const stale = new Date(Date.now() - 180 * 1000).toISOString();

  assert.equal(isDeviceOnline({ updated_at: recent }), true);
  assert.equal(isDeviceOnline({ updated_at: stale }), false);
  assert.equal(isDeviceOnline({}), false);
});

test('createDeviceCardHTML renders explicit Administrator and non-default SSH ports accurately', async () => {
  const { createDeviceCardHTML } = await import('../static/js/devices/render.js');

  const winDev = {
    id: 'win-pc-1',
    hostname: 'win-pc',
    alias: 'Windows 工作站',
    os: 'windows',
    arch: 'amd64',
    mac: 'aa:bb:cc:dd:ee:ff',
    ssh_user: 'Administrator',
    ssh_port: 22,
    addresses: ['192.168.1.100'],
    connected: true,
    sync_status: 'synced',
    health: { status: 'healthy', reasons: [] }
  };

  const html = createDeviceCardHTML(winDev);
  assert.match(html, /ssh Administrator@192\.168\.1\.100/, 'Must render ssh Administrator@192.168.1.100 for port 22');
  assert.doesNotMatch(html, /ROKILAI\$/, 'Must NOT render machine account');

  const winCustomPort = {
    ...winDev,
    ssh_port: 2222
  };
  const htmlCustom = createDeviceCardHTML(winCustomPort);
  assert.match(htmlCustom, /ssh -p 2222 Administrator@192\.168\.1\.100/, 'Must render ssh -p 2222 Administrator@192.168.1.100 for custom port');
});
