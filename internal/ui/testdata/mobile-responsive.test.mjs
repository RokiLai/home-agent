import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';

// Helper to load file content
async function loadCSS() {
  return await readFile(new URL('../static/style.css', import.meta.url), 'utf8');
}

async function loadHTML() {
  return await readFile(new URL('../static/index.html', import.meta.url), 'utf8');
}

// ---------------------------------------------------------------------------
// 1. Static CSS Breakpoints and Accessibility Rules Tests
// ---------------------------------------------------------------------------

test('CSS contains required breakpoints and responsive hierarchy', async () => {
  const css = await loadCSS();

  // Check defined breakpoints
  assert.match(css, /@media\s*\(\s*max-width:\s*1100px\s*\)/, 'CSS must contain 1100px breakpoint');
  assert.match(css, /@media\s*\(\s*max-width:\s*900px\s*\)/, 'CSS must contain 900px breakpoint');
  assert.match(css, /@media\s*\(\s*max-width:\s*640px\s*\)/, 'CSS must contain 640px breakpoint');
  assert.match(css, /@media\s*\(\s*max-width:\s*400px\s*\)/, 'CSS must contain 400px breakpoint');

  // Check prefers-reduced-motion
  assert.match(css, /@media\s*\(\s*prefers-reduced-motion:\s*reduce\s*\)/, 'CSS must support prefers-reduced-motion: reduce');

  // Check safe-area-inset
  assert.match(css, /env\(\s*safe-area-inset-top/i, 'CSS must support safe-area-inset-top');
  assert.match(css, /env\(\s*safe-area-inset-bottom/i, 'CSS must support safe-area-inset-bottom');
  assert.match(css, /env\(\s*safe-area-inset-left/i, 'CSS must support safe-area-inset-left');
  assert.match(css, /env\(\s*safe-area-inset-right/i, 'CSS must support safe-area-inset-right');

  // Check dvh with vh fallback
  assert.match(css, /100dvh/, 'CSS must use 100dvh for dynamic mobile viewport');
  assert.match(css, /100vh/, 'CSS must provide 100vh fallback');

  // Check inputs have font-size >= 16px on mobile to prevent iOS zoom
  assert.match(css, /input.*16px|font-size:\s*16px|\.form-control/i, 'Form inputs must have 16px font-size protection');

  // Check table cardification CSS for mobile
  assert.match(css, /data-table|commands-table|table-responsive/, 'Table styling must exist');
  assert.match(css, /data-label/, 'CSS must support data-label for mobile cardification');

  // Check device card mobile responsive rules
  assert.match(css, /\.device-grid\s*\{[^}]*grid-template-columns:\s*minmax\(0,\s*1fr\)/, 'CSS must enforce minmax(0, 1fr) on mobile device grid');
  assert.match(css, /\.device-card\s*\{[^}]*min-width:\s*0/, 'CSS must enforce min-width: 0 on device-card');
  assert.match(css, /\.device-card-header|\.header-badges/, 'CSS must adapt device card header for mobile');
  assert.match(css, /\.device-actions/, 'CSS must adapt device card actions for mobile');
  assert.match(css, /\.btn-ssh-box/, 'CSS must style SSH box for mobile');

  // Check top header single row and dashboard actions responsive layout
  assert.match(css, /\.top-header\s*\{[^}]*flex-direction:\s*row/, 'CSS must maintain single-row top-header on mobile <= 640px');
  assert.match(css, /\.dashboard-actions-group\s*\{[^}]*grid-template-columns:\s*1fr\s+1fr/, 'CSS must style dashboard actions as 2-column grid on mobile <= 640px');
  assert.match(css, /\.dashboard-actions-group\s*\{[^}]*grid-template-columns:\s*1fr;/, 'CSS must style dashboard actions as single column on mobile <= 400px');

  // Check body scroll lock class
  assert.match(css, /body\.modal-open|body\.sidebar-open|overflow:\s*hidden/, 'Body scroll lock rule must exist');

  // Verify CSS brace balancing to prevent nested selector or unclosed @media traps
  let openBraces = 0;
  for (let i = 0; i < css.length; i++) {
    if (css[i] === '{') openBraces++;
    else if (css[i] === '}') openBraces--;
    assert.ok(openBraces >= 0, `CSS has unexpected closing brace at character ${i}`);
  }
  assert.equal(openBraces, 0, 'CSS must have fully balanced open and closing braces');

  // Verify .login-overlay is defined globally (outside media queries)
  const loginOverlayDef = css.match(/\.login-overlay\s*\{[^}]*position:\s*fixed[^}]*\}/);
  assert.ok(loginOverlayDef, '.login-overlay must define position: fixed globally');
});

// ---------------------------------------------------------------------------
// 2. HTML DOM Compatibility and Semantic Attribute Contracts
// ---------------------------------------------------------------------------

test('HTML preserves all required DOM IDs and structural contracts', async () => {
  const html = await loadHTML();

  const requiredIDs = [
    // Pages
    'pageDashboard', 'pageDevices', 'pageOnboarding', 'pageGithub', 'pageCommands', 'pageSettings',
    // Nav
    'navDashboard', 'navDevices', 'navOnboarding', 'navGithub', 'navCommands', 'navSettings', 'navDeviceBadge',
    // Sidebar
    'appSidebar', 'sidebarToggleBtn', 'sidebarCloseBtn', 'sidebarBackdrop', 'liveStatusText', 'sidebarTokenStatus',
    // Top Bar
    'currentPageTitle', 'currentPageDesc', 'btnSyncAll', 'btnUpgradeAll', 'refreshBtn', 'btnLogout', 'adminUsernameDisplay',
    // Global Stats
    'statTotalDevices', 'statSyncedDevices', 'statPendingDevices', 'statLatestVersion',
    // Dashboard Page
    'dashboardDeviceCount', 'dashboardDeviceSummary', 'dashboardInstallPreviewCode', 'dashboardCopyBtn',
    'dashboardGithubContent', 'clearLogsBtn', 'eventLogContainer',
    // Devices Page
    'deviceListCount', 'deviceFilterPills', 'deviceSearchInput', 'deviceContainer',
    // Onboarding Page
    'claimTokenCountdown', 'btnRefreshClaimToken', 'installCommandCode', 'copyCommandBtn',
    // GitHub Page
    'githubCardBody', 'githubLoading', 'githubConnected', 'githubDisconnected',
    'githubUsername', 'githubTokenHint', 'githubSyncedCount', 'githubTotalCount',
    'githubAvatar', 'githubAvatarFallback', 'btnGithubLogin', 'btnGithubDisconnect', 'githubDeviceMatrix',
    // Commands Page
    'commandCount', 'commandsTableBody',
    // Settings Page
    'settingsServerUrlInput', 'settingsSaveBtn', 'settingsClearBtn', 'changePasswordForm',
    'oldPasswordInput', 'newPasswordInput', 'confirmPasswordInput', 'savePasswordBtn', 'changePasswordAlert',
    // Modals
    'ipModal', 'ipModalTitle', 'ipModalDesc', 'ipModalList', 'closeIpModalBtn', 'doneIpModalBtn',
    'renameModal', 'renameDeviceInfo', 'renameDeviceMac', 'deviceAliasInput', 'closeRenameModalBtn', 'cancelRenameModalBtn', 'saveRenameBtn',
    'githubDeviceModal', 'githubUserCodeDisplay', 'copyGithubUserCodeBtn', 'githubVerifyLink', 'closeGithubDeviceModalBtn', 'cancelGithubDeviceModalBtn',
    'deviceHealthModal', 'healthModalTitle', 'healthModalSubtitle', 'healthModalStatusBadge',
    'healthModalReasonsList', 'healthModalFactsGrid', 'healthModalEventsTimeline', 'closeHealthModalBtn', 'closeHealthModalBtnFooter',
    // Login & Toast
    'loginOverlay', 'loginForm', 'loginUsername', 'loginPassword', 'loginRememberMe', 'loginSubmitBtn', 'loginError',
    'toast', 'toastMsg'
  ];

  for (const id of requiredIDs) {
    const pattern = new RegExp(`id="${id}"`);
    assert.ok(pattern.test(html), `index.html must retain id="${id}"`);
  }

  // Verify health facts grid does not have inline grid-template-columns
  assert.doesNotMatch(html, /style="[^"]*grid-template-columns:\s*1fr\s+1fr/i, 'Inline grid-template-columns must be migrated to CSS class');

  // Verify sidebar toggle button has accessibility attributes
  assert.match(html, /id="sidebarToggleBtn"[^>]*aria-label|aria-expanded/i, 'sidebarToggleBtn should have accessibility attributes');

  // Verify currentPageTitle has tabindex="-1" for programmatic focus
  assert.match(html, /id="currentPageTitle"[^>]*tabindex="-1"/i, 'currentPageTitle must declare tabindex="-1" for programmatic focus');

  // Verify dashboard batch actions panel exists and contains batch buttons
  assert.match(html, /class="[^"]*dashboard-actions-panel[^"]*"[^>]*>[\s\S]*?id="btnSyncAll"[\s\S]*?id="btnUpgradeAll"/, 'index.html must contain dashboard-actions-panel enclosing btnSyncAll and btnUpgradeAll');
});

// ---------------------------------------------------------------------------
// 3. Operation History (Commands) Table DOM Cardification Test
// ---------------------------------------------------------------------------

test('fetchCommands populates data-label on each td for mobile responsive card presentation', async () => {
  const tableBody = {
    innerHTML: '',
    children: []
  };
  const countSpan = { textContent: '' };

  globalThis.window = { location: { origin: 'http://homeagent.test' } };
  globalThis.localStorage = { getItem() { return null; } };
  globalThis.document = {
    getElementById(id) {
      if (id === 'commandsTableBody') return tableBody;
      if (id === 'commandCount') return countSpan;
      return null;
    }
  };

  const sampleCommandsResponse = {
    commands: [
      {
        id: 'cmd-001',
        device_id: 'macbook-pro-8-local-0e93c101',
        kind: 'ssh_keys',
        status: 'succeeded',
        created_at: '2026-08-25T04:00:00Z',
        error_message: ''
      },
      {
        id: 'cmd-002',
        device_id: 'debian-server-fa8102',
        kind: 'upgrade',
        status: 'failed',
        created_at: '2026-08-25T04:05:00Z',
        error_message: 'network timeout connecting to mirror'
      }
    ]
  };

  globalThis.fetch = async (url) => {
    assert.match(url, /\/api\/v1\/commands/);
    return new Response(JSON.stringify(sampleCommandsResponse), {
      status: 200,
      headers: { 'Content-Type': 'application/json' }
    });
  };

  const { fetchCommands } = await import('../static/js/commands.js');
  await fetchCommands();

  assert.equal(countSpan.textContent, '2 条');
  assert.match(tableBody.innerHTML, /data-label="时间"/);
  assert.match(tableBody.innerHTML, /data-label="设备"/);
  assert.match(tableBody.innerHTML, /data-label="类型"/);
  assert.match(tableBody.innerHTML, /data-label="状态"/);
  assert.match(tableBody.innerHTML, /data-label="结果"/);
  assert.match(tableBody.innerHTML, /SSH 密钥同步/);
  assert.match(tableBody.innerHTML, /network timeout/);
});

// ---------------------------------------------------------------------------
// 4. Modal Manager Singleton Conflict Rejection & Scroll Lock Tests
// ---------------------------------------------------------------------------

test('modal manager enforces single active modal and controls scroll lock', async () => {
  const bodyClasses = new Set();
  const elementMap = new Map();

  function makeEl(id) {
    const classes = new Set(['hidden']);
    const el = {
      id,
      innerText: '',
      innerHTML: '',
      value: '',
      focus() {},
      classList: {
        add(...names) { names.forEach(n => classes.add(n)); },
        remove(...names) { names.forEach(n => classes.delete(n)); },
        contains(n) { return classes.has(n); }
      },
      appendChild() {},
      querySelector() {
        return {
          addEventListener() {},
          focus() {}
        };
      }
    };
    elementMap.set(id, el);
    return el;
  }

  ['ipModal', 'renameModal', 'githubDeviceModal', 'deviceHealthModal', 'toast', 'toastMsg',
   'renameDeviceInfo', 'renameDeviceMac', 'deviceAliasInput', 'ipModalTitle', 'ipModalDesc', 'ipModalList',
   'healthModalTitle', 'healthModalSubtitle', 'healthModalStatusBadge', 'healthModalReasonsList',
   'healthModalFactsGrid', 'healthModalEventsTimeline'].forEach(id => makeEl(id));

  globalThis.window = { location: { origin: 'http://homeagent.test' } };
  globalThis.localStorage = { getItem() { return null; } };
  globalThis.document = {
    body: {
      classList: {
        add(...names) { names.forEach(n => bodyClasses.add(n)); },
        remove(...names) { names.forEach(n => bodyClasses.delete(n)); },
        contains(n) { return bodyClasses.has(n); }
      }
    },
    getElementById(id) {
      return elementMap.get(id) || null;
    },
    createElement() {
      return makeEl('temp');
    },
    querySelectorAll() {
      return [];
    }
  };

  const { openRenameModal, closeRenameModal, openIpModal, closeIpModal, isAnyModalOpen } = await import('../static/js/modals.js');

  // Initially no modal is open
  assert.equal(isAnyModalOpen(), false);
  assert.equal(bodyClasses.has('modal-open'), false);

  // Open rename modal
  openRenameModal('dev-1', 'host-1', 'alias-1', 'mac-1');
  assert.equal(isAnyModalOpen(), true);
  assert.equal(elementMap.get('renameModal').classList.contains('hidden'), false);
  assert.equal(bodyClasses.has('modal-open'), true);

  // Attempting to open IP modal while rename modal is open should be rejected
  elementMap.get('toastMsg').innerText = '';
  openIpModal('host-1', 'IPv4', ['192.168.1.1']);

  // IP modal must remain hidden because rename modal is already active
  assert.equal(elementMap.get('ipModal').classList.contains('hidden'), true);
  assert.equal(elementMap.get('renameModal').classList.contains('hidden'), false);

  // Close rename modal
  closeRenameModal();
  assert.equal(isAnyModalOpen(), false);
  assert.equal(elementMap.get('renameModal').classList.contains('hidden'), true);
  assert.equal(bodyClasses.has('modal-open'), false);

  // Now IP modal can be opened
  openIpModal('host-1', 'IPv4', ['192.168.1.1']);
  assert.equal(isAnyModalOpen(), true);
  assert.equal(elementMap.get('ipModal').classList.contains('hidden'), false);
  assert.equal(bodyClasses.has('modal-open'), true);

  // Clean up
  closeIpModal();
  assert.equal(isAnyModalOpen(), false);
});

// ---------------------------------------------------------------------------
// 5. Sidebar Drawer State & Scroll Lock Tests
// ---------------------------------------------------------------------------

test('mobile sidebar toggle updates aria-expanded and locks body scroll', async () => {
  const bodyClasses = new Set();
  const sidebarClasses = new Set();
  const backdropClasses = new Set(['hidden']);
  let toggleAriaExpanded = 'false';

  const appSidebar = {
    classList: {
      add(...names) { names.forEach(n => sidebarClasses.add(n)); },
      remove(...names) { names.forEach(n => sidebarClasses.delete(n)); },
      contains(n) { return sidebarClasses.has(n); }
    }
  };
  const sidebarBackdrop = {
    classList: {
      add(...names) { names.forEach(n => backdropClasses.add(n)); },
      remove(...names) { names.forEach(n => backdropClasses.delete(n)); },
      contains(n) { return backdropClasses.has(n); }
    }
  };
  const sidebarToggleBtn = {
    setAttribute(name, val) {
      if (name === 'aria-expanded') toggleAriaExpanded = String(val);
    },
    getAttribute(name) {
      if (name === 'aria-expanded') return toggleAriaExpanded;
      return null;
    }
  };

  globalThis.window = { location: { origin: 'http://homeagent.test', hash: '' }, addEventListener() {} };
  globalThis.localStorage = { getItem() { return null; } };
  globalThis.document = {
    body: {
      classList: {
        add(...names) { names.forEach(n => bodyClasses.add(n)); },
        remove(...names) { names.forEach(n => bodyClasses.delete(n)); },
        contains(n) { return bodyClasses.has(n); }
      }
    },
    getElementById(id) {
      if (id === 'appSidebar') return appSidebar;
      if (id === 'sidebarBackdrop') return sidebarBackdrop;
      if (id === 'sidebarToggleBtn') return sidebarToggleBtn;
      return null;
    },
    querySelectorAll() {
      return [];
    }
  };

  const { openMobileSidebar, closeMobileSidebar } = await import('../static/js/router.js');

  // Open sidebar
  openMobileSidebar();
  assert.equal(sidebarClasses.has('open'), true);
  assert.equal(backdropClasses.has('hidden'), false);
  assert.equal(bodyClasses.has('sidebar-open'), true);
  assert.equal(toggleAriaExpanded, 'true');

  // Close sidebar
  closeMobileSidebar();
  assert.equal(sidebarClasses.has('open'), false);
  assert.equal(backdropClasses.has('hidden'), true);
  assert.equal(bodyClasses.has('sidebar-open'), false);
  assert.equal(toggleAriaExpanded, 'false');
});

// ---------------------------------------------------------------------------
// 6. Onboarding Command Generation & Expired State Tests
// ---------------------------------------------------------------------------

test('onboarding updates install commands across OS tabs and handles expired state', async () => {
  const installCode = { innerText: '' };
  const previewCode = { innerText: '' };
  const countdownEl = { textContent: '' };

  globalThis.window = { location: { origin: 'http://127.0.0.1:8888' } };
  globalThis.localStorage = { getItem() { return null; } };
  globalThis.document = {
    getElementById(id) {
      if (id === 'installCommandCode') return installCode;
      if (id === 'dashboardInstallPreviewCode') return previewCode;
      if (id === 'claimTokenCountdown') return countdownEl;
      return null;
    }
  };

  const { state } = await import('../static/js/state.js');
  const { updateInstallCommand, startClaimCountdown } = await import('../static/js/onboarding.js');

  state.currentClaimToken = 'test-claim-token-123';
  state.serverHost = 'http://127.0.0.1:8888';

  // Test macOS / Linux / OpenWrt tab
  state.activeOSTab = 'darwin';
  updateInstallCommand();
  assert.match(installCode.innerText, /curl -fsSL https:\/\/raw\.githubusercontent\.com\/RokiLai\/home-agent\/main\/scripts\/install\.sh/);
  assert.match(installCode.innerText, /HOMEAGENT_SERVER="http:\/\/127\.0\.0\.1:8888"/);
  assert.match(installCode.innerText, /HOMEAGENT_CLAIM_TOKEN="test-claim-token-123"/);

  // Test Windows tab
  state.activeOSTab = 'windows';
  updateInstallCommand();
  assert.match(installCode.innerText, /\$env:HOMEAGENT_SERVER="http:\/\/127\.0\.0\.1:8888"/);
  assert.match(installCode.innerText, /\$env:HOMEAGENT_CLAIM_TOKEN="test-claim-token-123"/);
  assert.match(installCode.innerText, /irm https:\/\/raw\.githubusercontent\.com\/RokiLai\/home-agent\/main\/scripts\/install\.ps1 \| iex/);

  // Test default server omission (shortest command)
  state.publicUrl = 'https://homeagent.rokilai.online';
  state.activeOSTab = 'darwin';
  updateInstallCommand();
  assert.match(installCode.innerText, /curl -fsSL https:\/\/raw\.githubusercontent\.com\/RokiLai\/home-agent\/main\/scripts\/install\.sh \| HOMEAGENT_CLAIM_TOKEN="test-claim-token-123" sh/);

  // Test custom publicUrl inclusion
  state.publicUrl = 'https://custom.myagent.org';
  state.activeOSTab = 'darwin';
  updateInstallCommand();
  assert.match(installCode.innerText, /HOMEAGENT_SERVER="https:\/\/custom\.myagent\.org" HOMEAGENT_CLAIM_TOKEN="test-claim-token-123"/);

  // Test countdown expired state
  state.claimTokenExpiresAt = new Date(Date.now() - 10000); // 10s in the past
  startClaimCountdown();
  assert.match(countdownEl.textContent, /凭据已过期/);
  if (state.claimCountdownTimer) clearInterval(state.claimCountdownTimer);
});

// ---------------------------------------------------------------------------
// 7. Settings Form Parameters & Password Validation Tests
// ---------------------------------------------------------------------------

test('settings form saves host and validates password change rules', async () => {
  const store = new Map();
  const inputEl = { value: '  http://192.168.1.100:8888/  ' };
  const oldPassEl = { value: 'old123' };
  const newPassEl = { value: 'short' };
  const confirmPassEl = { value: 'short' };
  const alertEl = {
    innerText: '',
    classList: {
      add() {},
      remove() {}
    }
  };

  globalThis.window = { location: { origin: 'http://127.0.0.1:8888' } };
  globalThis.localStorage = {
    getItem(k) { return store.get(k) || null; },
    setItem(k, v) { store.set(k, v); },
    removeItem(k) { store.delete(k); }
  };
  globalThis.document = {
    getElementById(id) {
      if (id === 'settingsServerUrlInput') return inputEl;
      if (id === 'oldPasswordInput') return oldPassEl;
      if (id === 'newPasswordInput') return newPassEl;
      if (id === 'confirmPasswordInput') return confirmPassEl;
      if (id === 'changePasswordAlert') return alertEl;
      return null;
    }
  };

  const { state } = await import('../static/js/state.js');
  const { saveSettingsForm, clearSettingsToken } = await import('../static/js/settings.js');
  const { handleChangePassword } = await import('../static/js/auth.js');

  // Save server host
  saveSettingsForm();
  assert.equal(store.get('homeagent_server_url'), 'http://192.168.1.100:8888');
  assert.equal(state.serverHost, 'http://192.168.1.100:8888');

  // Clear server host
  clearSettingsToken();
  assert.equal(store.has('homeagent_server_url'), false);
  assert.equal(state.serverHost, '');

  // Password change validation (< 6 chars)
  await handleChangePassword({ preventDefault() {} });
  assert.match(alertEl.innerText, /少于 6 位/);

  // Password mismatch
  newPassEl.value = 'newpass123';
  confirmPassEl.value = 'differentpass123';
  await handleChangePassword({ preventDefault() {} });
  assert.match(alertEl.innerText, /两次输入的新密码不一致/);
});

// ---------------------------------------------------------------------------
// 8. Device Card HTML Rendering with Long Names & Edge Cases
// ---------------------------------------------------------------------------

test('createDeviceCardHTML renders full information for edge cases', async () => {
  const { createDeviceCardHTML } = await import('../static/js/devices/render.js');

  const edgeDevice = {
    id: 'very-long-device-identifier-with-special-characters-xyz-998877',
    hostname: 'Very-Long-Enterprise-Server-Hostname-01.corp.internal',
    alias: '客厅主路由 / NAS 存储一体机',
    mac: '02:00:00:00:00:01',
    agent_version: 'v0.6.0',
    os: 'linux',
    arch: 'arm64',
    ssh_user: 'admin',
    ssh_port: 2222,
    addresses: ['10.0.0.15', '2001:db8:8207:1234:5678:9abc:def0:1234'],
    ddns_domain: 'nas.home.internal',
    sync_status: 'synced',
    applied_hash: 'abcdef1234567890abcdef1234567890',
    sync_updated_at: new Date(Date.now() - 30000).toISOString(),
    github_sync_enabled: true,
    github_status: 'synced',
    connected: true,
    health: {
      status: 'healthy',
      reasons: []
    }
  };

  const html = createDeviceCardHTML(edgeDevice);

  assert.match(html, /客厅主路由 \/ NAS 存储一体机/);
  assert.match(html, /Very-Long-Enterprise-Server-Hostname-01\.corp\.internal/);
  assert.match(html, /ssh -p 2222 admin@10\.0\.0\.15/);
  assert.match(html, /nas\.home\.internal/);
  assert.match(html, /02:00:00:00:00:01/);
  assert.match(html, /v0\.6\.0/);
  assert.match(html, /arm64/);
  assert.match(html, /HEALTHY/);
  assert.match(html, /btn-wake-device/);
  assert.match(html, /btn-shutdown-device/);
});

// ---------------------------------------------------------------------------
// 9. GitHub Sync Connected / Disconnected State & Matrix Rendering
// ---------------------------------------------------------------------------

test('renderGitHubStatus and renderGitHubDeviceMatrix handle authentication states', async () => {
  const elements = new Map([
    ['githubLoading', { classList: { add() {}, remove() {} } }],
    ['githubConnected', { classList: { add() {}, remove() {}, contains() { return false; } } }],
    ['githubDisconnected', { classList: { add() {}, remove() {}, contains() { return false; } } }],
    ['githubUsername', { innerText: '' }],
    ['githubTokenHint', { innerText: '' }],
    ['githubSyncedCount', { innerText: '' }],
    ['githubTotalCount', { innerText: '' }],
    ['githubAvatar', { src: '', classList: { add() {}, remove() {} } }],
    ['githubAvatarFallback', { classList: { add() {}, remove() {} } }],
    ['githubDeviceMatrix', { innerHTML: '', appendChild(c) { this.children = this.children || []; this.children.push(c); } }]
  ]);

  globalThis.document = {
    getElementById(id) { return elements.get(id) || null; },
    createElement(tag) {
      return {
        className: '',
        innerHTML: '',
        querySelector() { return { addEventListener() {} }; }
      };
    }
  };

  const { state } = await import('../static/js/state.js');
  const { renderGitHubStatus, renderGitHubDeviceMatrix } = await import('../static/js/github.js');

  // Connected state
  const connectedData = {
    connected: true,
    user: { login: 'octocat', avatar_url: 'https://github.com/images/error/octocat_happy.gif' },
    token_preview: 'ghp_xxxx',
    synced_devices_count: 3,
    total_devices_count: 5
  };
  renderGitHubStatus(connectedData);
  assert.equal(elements.get('githubUsername').innerText, 'octocat');
  assert.equal(elements.get('githubTokenHint').innerText, 'Token: ghp_xxxx');
  assert.equal(elements.get('githubSyncedCount').innerText, 3);
  assert.equal(elements.get('githubTotalCount').innerText, 5);

  // Disconnected state
  const disconnectedData = { connected: false };
  renderGitHubStatus(disconnectedData);

  // Device matrix
  state.devices = [
    { id: 'dev-1', hostname: 'MacBook', github_sync_enabled: true },
    { id: 'dev-2', hostname: 'LinuxServer', github_sync_enabled: false }
  ];
  renderGitHubDeviceMatrix();
  assert.ok(elements.get('githubDeviceMatrix').children.length >= 2);
});

// ---------------------------------------------------------------------------
// 10. GitHub Device Flow Modal Conflict Rejection Test
// ---------------------------------------------------------------------------

test('startGitHubDeviceFlow rejects flow before making API calls when another modal is open', async () => {
  const toastMsg = { innerText: '' };
  const toast = { classList: { add() {}, remove() {} } };
  const btnGithubLogin = { disabled: false };
  const renameModal = { classList: { add() {}, remove() {} } };

  globalThis.document = {
    body: {
      classList: { add() {}, remove() {} },
      contains() { return false; }
    },
    getElementById(id) {
      if (id === 'toastMsg') return toastMsg;
      if (id === 'toast') return toast;
      if (id === 'btnGithubLogin') return btnGithubLogin;
      if (id === 'renameModal') return renameModal;
      if (id === 'renameDeviceInfo') return { innerText: '' };
      if (id === 'renameDeviceMac') return { innerText: '' };
      if (id === 'deviceAliasInput') return { value: '', focus() {} };
      return null;
    }
  };

  let fetchCalled = false;
  globalThis.fetch = async () => {
    fetchCalled = true;
    throw new Error('fetch should not be called when modal is blocked');
  };

  const { openRenameModal, closeRenameModal } = await import('../static/js/modals.js');
  const { startGitHubDeviceFlow } = await import('../static/js/github.js');

  // Open rename modal first
  openRenameModal('dev-99', 'host-99', 'alias-99', 'mac-99');

  // Attempt to start GitHub Device Flow
  await startGitHubDeviceFlow();

  assert.equal(fetchCalled, false, 'API request must NOT be sent when modal conflict occurs');
  assert.match(toastMsg.innerText, /请先完成或关闭当前操作/);

  // Clean up
  closeRenameModal();
});

// ---------------------------------------------------------------------------
// 11. Escape / closeActiveModal Cleanup & Focus Restoration Test
// ---------------------------------------------------------------------------

test('closeActiveModal cleans up modal state and restores focus to trigger element', async () => {
  let triggerFocused = false;
  const triggerEl = {
    focus() { triggerFocused = true; }
  };

  const renameModal = { classList: { add() {}, remove() {} } };
  const renameDeviceInfo = { innerText: '' };
  const renameDeviceMac = { innerText: '' };
  const deviceAliasInput = { value: '', focus() {} };

  globalThis.document = {
    body: {
      classList: { add() {}, remove() {} },
      contains(el) { return el === triggerEl; }
    },
    getElementById(id) {
      if (id === 'renameModal') return renameModal;
      if (id === 'renameDeviceInfo') return renameDeviceInfo;
      if (id === 'renameDeviceMac') return renameDeviceMac;
      if (id === 'deviceAliasInput') return deviceAliasInput;
      return null;
    }
  };

  const { state } = await import('../static/js/state.js');
  const { openRenameModal, closeActiveModal, isAnyModalOpen } = await import('../static/js/modals.js');

  openRenameModal('dev-42', 'my-laptop', 'Laptop', 'aa:bb:cc:dd:ee:ff', triggerEl);
  assert.equal(state.currentRenameDeviceId, 'dev-42');
  assert.equal(isAnyModalOpen(), true);

  // Close via closeActiveModal (simulating Escape key or backdrop)
  closeActiveModal();

  assert.equal(state.currentRenameDeviceId, '', 'currentRenameDeviceId must be reset');
  assert.equal(isAnyModalOpen(), false, 'modal state must be closed');
  assert.equal(triggerFocused, true, 'focus must be restored to trigger element');
});

// ---------------------------------------------------------------------------
// 12. Programmatic Focus Fallback to #currentPageTitle Test
// ---------------------------------------------------------------------------

test('requestCloseModal falls back to #currentPageTitle when trigger element is detached', async () => {
  let pageTitleFocused = false;
  const pageTitleEl = {
    focus() { pageTitleFocused = true; }
  };
  const detachedTrigger = {
    focus() { throw new Error('detached element should not be focused'); }
  };

  globalThis.document = {
    body: {
      classList: { add() {}, remove() {} },
      contains(el) { return el !== detachedTrigger; } // trigger is detached
    },
    getElementById(id) {
      if (id === 'currentPageTitle') return pageTitleEl;
      if (id === 'ipModal') return { classList: { add() {}, remove() {} } };
      if (id === 'ipModalTitle') return { innerText: '' };
      if (id === 'ipModalDesc') return { innerText: '' };
      if (id === 'ipModalList') return { innerHTML: '', appendChild() {} };
      return null;
    },
    createElement() {
      return {
        className: '',
        innerHTML: '',
        appendChild() {},
        querySelector() { return { addEventListener() {} }; }
      };
    }
  };

  const { openIpModal, closeIpModal } = await import('../static/js/modals.js');

  openIpModal('host-1', 'IPv4', ['1.2.3.4'], detachedTrigger);
  closeIpModal();

  assert.equal(pageTitleFocused, true, 'focus must fall back to #currentPageTitle when trigger is detached');
});

// ---------------------------------------------------------------------------
// 13. Mobile Sidebar Focus Entrance and Return Test
// ---------------------------------------------------------------------------

test('openMobileSidebar focuses sidebar close button and closeMobileSidebar returns focus to toggle button', async () => {
  let closeBtnFocused = false;
  let toggleBtnFocused = false;

  const sidebarCloseBtn = {
    focus() { closeBtnFocused = true; }
  };
  const sidebarToggleBtn = {
    setAttribute() {},
    focus() { toggleBtnFocused = true; }
  };
  const appSidebar = {
    classList: { add() {}, remove() {} }
  };
  const sidebarBackdrop = {
    classList: { add() {}, remove() {} }
  };

  globalThis.document = {
    body: {
      classList: { add() {}, remove() {} },
      contains(el) { return el === sidebarToggleBtn; }
    },
    getElementById(id) {
      if (id === 'sidebarCloseBtn') return sidebarCloseBtn;
      if (id === 'sidebarToggleBtn') return sidebarToggleBtn;
      if (id === 'appSidebar') return appSidebar;
      if (id === 'sidebarBackdrop') return sidebarBackdrop;
      return null;
    },
    activeElement: sidebarToggleBtn
  };

  const { openMobileSidebar, closeMobileSidebar } = await import('../static/js/router.js');

  // Open mobile sidebar
  openMobileSidebar(sidebarToggleBtn);
  assert.equal(closeBtnFocused, true, 'focus must move to #sidebarCloseBtn when sidebar opens');

  // Close mobile sidebar
  closeMobileSidebar();
  assert.equal(toggleBtnFocused, true, 'focus must return to #sidebarToggleBtn when sidebar closes');
});

// ---------------------------------------------------------------------------
// 14. GitHub Device Flow In-Flight Race Condition Test
// ---------------------------------------------------------------------------

test('startGitHubDeviceFlow aborts flow when another modal opens while request is in flight', async () => {
  const toastMsg = { innerText: '' };
  const toast = { classList: { add() {}, remove() {} } };
  const btnGithubLogin = { disabled: false };
  const githubUserCodeDisplay = { innerText: '' };
  const githubVerifyLink = { href: '' };
  const renameModal = { classList: { add() {}, remove() {} } };
  const githubDeviceModal = { classList: { add() {}, remove() {} } };

  globalThis.document = {
    body: {
      classList: { add() {}, remove() {} },
      contains() { return true; }
    },
    getElementById(id) {
      if (id === 'toastMsg') return toastMsg;
      if (id === 'toast') return toast;
      if (id === 'btnGithubLogin') return btnGithubLogin;
      if (id === 'githubUserCodeDisplay') return githubUserCodeDisplay;
      if (id === 'githubVerifyLink') return githubVerifyLink;
      if (id === 'renameModal') return renameModal;
      if (id === 'renameDeviceInfo') return { innerText: '' };
      if (id === 'renameDeviceMac') return { innerText: '' };
      if (id === 'deviceAliasInput') return { value: '', focus() {} };
      if (id === 'githubDeviceModal') return githubDeviceModal;
      return null;
    }
  };

  let resolveDeviceCode;
  let pollCalled = false;

  globalThis.fetch = async (url) => {
    if (url.includes('/device-code')) {
      return new Promise((resolve) => {
        resolveDeviceCode = () => resolve({
          ok: true,
          json: async () => ({ user_code: 'ABCD-1234', verification_uri: 'https://github.com/login/device' })
        });
      });
    }
    if (url.includes('/poll')) {
      pollCalled = true;
      return { ok: true, json: async () => ({ status: 'pending' }) };
    }
    return { ok: true, json: async () => ({}) };
  };

  const { openRenameModal, closeRenameModal, isAnyModalOpen, getActiveModalId } = await import('../static/js/modals.js');
  const { startGitHubDeviceFlow } = await import('../static/js/github.js');

  // Step 1: Start GitHub device flow (no modal open initially)
  assert.equal(isAnyModalOpen(), false);
  const flowPromise = startGitHubDeviceFlow();

  // Step 2: While device-code fetch is in flight, user opens renameModal
  openRenameModal('dev-race-1', 'host-race-1', 'alias-race-1', 'mac-race-1');
  assert.equal(getActiveModalId(), 'renameModal');

  // Step 3: Now the device-code request resolves
  resolveDeviceCode();
  await flowPromise;

  // Step 4: Assertions
  // Active modal must still be renameModal, not githubDeviceModal
  assert.equal(getActiveModalId(), 'renameModal', 'activeModalId must remain renameModal');
  assert.equal(pollCalled, false, 'pollGitHubAuth must NOT be started when in-flight modal collision occurs');

  // Clean up
  closeRenameModal();
  assert.equal(isAnyModalOpen(), false);
});

// ---------------------------------------------------------------------------
// 15. Device Card Mobile Actions & Health Modal Interaction Test
// ---------------------------------------------------------------------------

test('device card actions, health modal, and dropdowns interact properly in mobile DOM', async () => {
  const elements = new Map();

  function makeEl(id) {
    const classes = new Set(['hidden']);
    const el = {
      id,
      innerText: '',
      innerHTML: '',
      value: '',
      style: {},
      focus() {},
      classList: {
        add(...names) { names.forEach(n => classes.add(n)); },
        remove(...names) { names.forEach(n => classes.delete(n)); },
        contains(n) { return classes.has(n); }
      },
      appendChild() {},
      querySelector() {
        return {
          addEventListener() {},
          focus() {}
        };
      }
    };
    elements.set(id, el);
    return el;
  }

  ['deviceHealthModal', 'healthModalTitle', 'healthModalSubtitle', 'healthModalStatusBadge',
   'healthModalReasonsList', 'healthModalFactsGrid', 'healthModalEventsTimeline',
   'toast', 'toastMsg'].forEach(id => makeEl(id));

  globalThis.window = { location: { origin: 'http://homeagent.test' } };
  globalThis.localStorage = { getItem() { return null; } };
  globalThis.document = {
    body: {
      classList: {
        add() {},
        remove() {},
        contains() { return false; }
      }
    },
    getElementById(id) {
      return elements.get(id) || null;
    },
    createElement() {
      return makeEl('temp');
    },
    querySelectorAll() {
      return [];
    }
  };

  const { state } = await import('../static/js/state.js');
  const { openHealthModal, closeHealthModal } = await import('../static/js/devices/actions.js');
  const { isAnyModalOpen, getActiveModalId } = await import('../static/js/modals.js');

  state.devices = [{
    id: 'mobile-dev-001',
    hostname: 'test-mobile-host',
    alias: '移动测试设备',
    mac: '11:22:33:44:55:66',
    agent_version: 'v0.7.0',
    os: 'linux',
    arch: 'arm64',
    health: {
      status: 'degraded',
      facts: {
        observed_at: '2026-08-26T12:00:00Z',
        uptime_seconds: 7200,
        logical_cpu_count: 8
      },
      reasons: [{
        code: 'memory_high',
        severity: 'warning',
        summary: '内存使用率过高 (88%)',
        suggested_action: '检查后台进程占用'
      }]
    }
  }];

  // Open Health Modal from device card
  await openHealthModal('mobile-dev-001');

  assert.equal(isAnyModalOpen(), true);
  assert.equal(getActiveModalId(), 'deviceHealthModal');
  assert.match(elements.get('healthModalTitle').innerText, /移动测试设备/);
  assert.match(elements.get('healthModalSubtitle').innerText, /mobile-dev-001/);
  assert.match(elements.get('healthModalStatusBadge').innerText, /DEGRADED/);
  assert.match(elements.get('healthModalReasonsList').innerHTML, /memory_high/);
  assert.match(elements.get('healthModalReasonsList').innerHTML, /内存使用率过高/);
  assert.match(elements.get('healthModalFactsGrid').innerHTML, /运行时间 \(Uptime\)/);
  assert.match(elements.get('healthModalFactsGrid').innerHTML, /2 小时/);
  assert.doesNotMatch(elements.get('healthModalFactsGrid').innerHTML, /0 \/ 0 MiB/);
  assert.doesNotMatch(elements.get('healthModalFactsGrid').innerHTML, /0\.0 \/ 0 GiB/);
  assert.match(elements.get('healthModalFactsGrid').innerHTML, /内存使用率[\s\S]*>-<\/div>/);
  assert.match(elements.get('healthModalFactsGrid').innerHTML, /磁盘使用率[\s\S]*>-<\/div>/);

  // Close Health Modal
  closeHealthModal();
  assert.equal(isAnyModalOpen(), false);
});
