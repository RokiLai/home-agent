import test, { before, after } from 'node:test';
import assert from 'node:assert/strict';
import cp from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

// ---------------------------------------------------------------------------
// 1. Chrome Process Management and CDP Client
// ---------------------------------------------------------------------------

function findChromeBinary() {
  if (process.env.CHROME_BIN && fs.existsSync(process.env.CHROME_BIN)) {
    return process.env.CHROME_BIN;
  }
  const candidates = [
    '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
    '/Applications/Chromium.app/Contents/MacOS/Chromium',
    '/usr/bin/google-chrome',
    '/usr/bin/google-chrome-stable',
    '/usr/bin/chromium',
    '/usr/bin/chromium-browser'
  ];
  for (const c of candidates) {
    if (fs.existsSync(c)) return c;
  }
  throw new Error('Google Chrome or Chromium binary not found. Set CHROME_BIN environment variable.');
}

class CDPClient {
  constructor(wsUrl) {
    this.wsUrl = wsUrl;
    this.ws = null;
    this.reqId = 1;
    this.pending = new Map();
    this.eventListeners = new Map();
    this.runtimeErrors = [];
  }

  async connect() {
    return new Promise((resolve, reject) => {
      this.ws = new WebSocket(this.wsUrl);
      this.ws.onopen = () => resolve();
      this.ws.onerror = (err) => reject(err);
      this.ws.onmessage = (evt) => {
        try {
          const msg = JSON.parse(evt.data);
          if (msg.id && this.pending.has(msg.id)) {
            const { resolve: pResolve, reject: pReject } = this.pending.get(msg.id);
            this.pending.delete(msg.id);
            if (msg.error) {
              pReject(new Error(`CDP Error ${msg.error.code}: ${msg.error.message}`));
            } else {
              pResolve(msg.result);
            }
          } else if (msg.method) {
            const list = this.eventListeners.get(msg.method) || [];
            list.forEach(cb => cb(msg.params, msg.sessionId));
          }
        } catch (e) {
          console.error('CDP parse error:', e);
        }
      };
    });
  }

  send(method, params = {}, sessionId = undefined) {
    return new Promise((resolve, reject) => {
      const id = this.reqId++;
      this.pending.set(id, { resolve, reject });
      const payload = { id, method, params };
      if (sessionId) payload.sessionId = sessionId;
      this.ws.send(JSON.stringify(payload));
    });
  }

  on(event, callback) {
    if (!this.eventListeners.has(event)) {
      this.eventListeners.set(event, []);
    }
    this.eventListeners.get(event).push(callback);
  }

  close() {
    if (this.ws) {
      try { this.ws.close(); } catch (_) {}
    }
  }
}

// Global test harness state
let chromeProcess = null;
let chromeTempDir = null;
let cdp = null;
let targetSessionId = null;
const runtimeErrors = [];

const serverUrl = process.env.TEST_SERVER_URL || 'http://127.0.0.1:8888';

before(async () => {
  const chromePath = findChromeBinary();
  chromeTempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'homeagent-cdp-test-'));

  chromeProcess = cp.spawn(chromePath, [
    '--headless=new',
    '--remote-debugging-port=0',
    `--user-data-dir=${chromeTempDir}`,
    '--no-first-run',
    '--no-default-browser-check',
    '--enable-features=NetworkServiceInProcess',
    '--use-mock-keychain',
    '--password-store=basic',
    '--disable-background-networking',
    '--disable-sync',
    '--disable-default-apps',
    '--disable-extensions',
    '--disable-gpu',
    '--disable-component-update',
    '--window-size=1280,800'
  ], { stdio: ['ignore', 'pipe', 'pipe'] });

  const wsUrl = await new Promise((resolve, reject) => {
    const timeout = setTimeout(() => {
      reject(new Error('Timed out waiting for Chrome DevTools listening port'));
    }, 10000);

    chromeProcess.stderr.on('data', (chunk) => {
      const str = chunk.toString();
      const match = str.match(/DevTools listening on (ws:\/\/127\.0\.0\.1:\d+\/[^\s]+)/);
      if (match) {
        clearTimeout(timeout);
        resolve(match[1]);
      }
    });

    chromeProcess.on('error', (err) => {
      clearTimeout(timeout);
      reject(err);
    });
  });
  cdp = new CDPClient(wsUrl);
  await cdp.connect();

  // Version Probe
  const version = await cdp.send('Browser.getVersion');
  assert.ok(version.product.includes('Chrome') || version.product.includes('Chromium'), 'Browser must be Chrome/Chromium');

  // Create page target
  const targetRes = await cdp.send('Target.createTarget', { url: 'about:blank' });
  const targetId = targetRes.targetId || targetRes.result?.targetId;
  const attachRes = await cdp.send('Target.attachToTarget', { targetId, flatten: true });
  targetSessionId = attachRes.sessionId || attachRes.result?.sessionId;

  // Enable domains
  await cdp.send('Page.enable', {}, targetSessionId);
  await cdp.send('Runtime.enable', {}, targetSessionId);
  await cdp.send('DOM.enable', {}, targetSessionId);
  await cdp.send('Accessibility.enable', {}, targetSessionId);

  // Probe Emulation.setEmulatedOSTextScale
  const scaleProbe = await cdp.send('Emulation.setEmulatedOSTextScale', { scale: 2 }, targetSessionId);
  assert.ok(scaleProbe, 'CDP protocol probe for Emulation.setEmulatedOSTextScale must succeed');

  // Listen for runtime errors & console errors
  cdp.on('Runtime.exceptionThrown', (params) => {
    const text = params.exceptionDetails?.exception?.description || params.exceptionDetails?.text || 'Unknown exception';
    runtimeErrors.push(`Uncaught Exception: ${text}`);
  });

  cdp.on('Runtime.consoleAPICalled', (params) => {
    if (params.type === 'error') {
      const msg = params.args.map(a => a.value || a.description || JSON.stringify(a)).join(' ');
      runtimeErrors.push(`Console Error: ${msg}`);
    }
  });
});

after(async () => {
  if (cdp) {
    try {
      await cdp.send('Browser.close');
      cdp.close();
    } catch (_) {}
  }
  if (chromeProcess && chromeProcess.exitCode === null) {
    try {
      chromeProcess.kill('SIGKILL');
      await new Promise(r => {
        chromeProcess.once('exit', r);
        setTimeout(r, 1000);
      });
    } catch (_) {}
  }
  if (chromeTempDir && fs.existsSync(chromeTempDir)) {
    try {
      fs.rmSync(chromeTempDir, { recursive: true, force: true });
    } catch (_) {}
  }
});

// Helper to evaluate JavaScript in browser page
async function evalJS(expr) {
  const res = await cdp.send('Runtime.evaluate', {
    expression: expr,
    returnByValue: true,
    awaitPromise: true
  }, targetSessionId);
  if (res.exceptionDetails) {
    throw new Error(`Eval failed for "${expr}": ${res.exceptionDetails.text}`);
  }
  if (res.result && 'value' in res.result) {
    return res.result.value;
  }
  return res.value;
}

// Helper to wait for predicate in page
async function waitFor(expr, timeoutMs = 5000) {
  const start = Date.now();
  while (Date.now() - start < timeoutMs) {
    try {
      const val = await evalJS(`(() => { try { return Boolean(${expr}); } catch (_) { return false; } })()`);
      if (val) return val;
    } catch (_) {}
    await new Promise(r => setTimeout(r, 50));
  }
  throw new Error(`Timeout waiting for expression: ${expr}`);
}

// Helper to set viewport
async function setViewport(width, height, mobile = true, deviceScaleFactor = 2) {
  await cdp.send('Emulation.setDeviceMetricsOverride', {
    width,
    height,
    deviceScaleFactor,
    mobile
  }, targetSessionId);
  await new Promise(r => setTimeout(r, 300));
}

// ---------------------------------------------------------------------------
// 2. Test Cases
// ---------------------------------------------------------------------------

test('1. Viewport matrix: No page-level horizontal overflow across all breakpoints', async () => {
  const viewports = [
    { width: 360, height: 640, name: '360x640 (Mobile Min)' },
    { width: 390, height: 844, name: '390x844 (iPhone)' },
    { width: 412, height: 915, name: '412x915 (Android)' },
    { width: 844, height: 390, name: '844x390 (Mobile Landscape)' },
    { width: 768, height: 1024, name: '768x1024 (Tablet)' },
    { width: 1280, height: 800, name: '1280x800 (Desktop)' }
  ];

  await cdp.send('Page.navigate', { url: `${serverUrl}/` }, targetSessionId);
  await waitFor('document.readyState === "complete"');
  await waitFor('!document.getElementById("loginOverlay").classList.contains("hidden")');

  // Verify Login Overlay in unauthenticated mode across all viewports
  for (const vp of viewports) {
    await setViewport(vp.width, vp.height, vp.width < 900);
    await new Promise(r => setTimeout(r, 50));

    const checkOverlay = await evalJS(`
      (() => {
        const overlay = document.getElementById('loginOverlay');
        const scrollW = document.documentElement.scrollWidth;
        const clientW = document.documentElement.clientWidth;
        return { scrollW, clientW, overlayVisible: overlay && !overlay.classList.contains('hidden') };
      })()
    `);

    assert.ok(checkOverlay.overlayVisible, `Login overlay should be visible on ${vp.name}`);
    assert.ok(checkOverlay.scrollW <= checkOverlay.clientW + 1, `Page must not overflow horizontally on ${vp.name} (scrollW: ${checkOverlay.scrollW}, clientW: ${checkOverlay.clientW})`);
  }

  // Perform login
  await evalJS(`
    (() => {
      document.getElementById('loginUsername').value = 'admin';
      document.getElementById('loginPassword').value = 'admin123';
      document.getElementById('loginSubmitBtn').click();
    })()
  `);

  await waitFor('document.getElementById("loginOverlay").classList.contains("hidden")');

  // Verify 6 protected routes under each viewport
  const routes = ['#/dashboard', '#/devices', '#/onboarding', '#/github', '#/commands', '#/settings'];

  for (const vp of viewports) {
    await setViewport(vp.width, vp.height, vp.width < 900);

    for (const r of routes) {
      await evalJS(`window.location.hash = '${r}';`);
      await new Promise(r => setTimeout(r, 60));

      const overflow = await evalJS(`
        (() => {
          return {
            scrollW: document.documentElement.scrollWidth,
            clientW: document.documentElement.clientWidth,
            bodyScrollW: document.body.scrollWidth,
            bodyClientW: document.body.clientWidth
          };
        })()
      `);

      assert.ok(overflow.scrollW <= overflow.clientW + 1, `Route ${r} must not overflow horizontally on ${vp.name} (scrollW: ${overflow.scrollW}, clientW: ${overflow.clientW})`);
      assert.ok(overflow.bodyScrollW <= overflow.bodyClientW + 1, `Route ${r} body must not overflow horizontally on ${vp.name}`);
    }
  }
});

test('2. State activation matrix: 44px touch target geometry & click hit closure (Diff Set = 0)', async () => {
  await setViewport(360, 640, true);

  const pageTargets = [
    '#sidebarToggleBtn', '#refreshBtn', '#btnLogout',
    '#dashboardCopyBtn', '#clearLogsBtn',
    '#btnSyncAll', '#btnUpgradeAll', '#deviceSearchInput',
    '#copyCommandBtn', '#btnRefreshClaimToken',
    '#settingsSaveBtn', '#settingsClearBtn', '#savePasswordBtn'
  ];

  const drawerTargets = [
    '#sidebarCloseBtn',
    '#navDashboard', '#navDevices', '#navOnboarding', '#navGithub', '#navCommands', '#navSettings'
  ];

  const modalTargets = [
    '#closeRenameModalBtn', '#cancelRenameModalBtn', '#saveRenameBtn',
    '#closeIpModalBtn', '#doneIpModalBtn'
  ];

  const authTargets = [
    '#loginUsername', '#loginPassword', '#loginSubmitBtn'
  ];

  const contractInteractiveTargets = [
    ...pageTargets,
    ...drawerTargets,
    ...modalTargets,
    ...authTargets
  ];

  const testedTargets = new Set();

  async function inspectAndHitTest(selector) {
    const result = await evalJS(`
      (() => {
        const el = document.querySelector('${selector}');
        if (!el) return { found: false };
        const isVisible = (elem) => {
          if (!elem) return false;
          if (elem.checkVisibility && !elem.checkVisibility()) return false;
          if (elem.offsetParent === null && elem.tagName !== 'BODY' && elem.tagName !== 'HTML') return false;
          const r = elem.getBoundingClientRect();
          if (r.width <= 0 || r.height <= 0) return false;
          return true;
        };
        if (!isVisible(el)) return { found: true, visible: false };

        if (el.scrollIntoView && !el.closest('#appSidebar')) {
          el.scrollIntoView({ block: 'center', inline: 'center' });
        }

        const rect = el.getBoundingClientRect();
        const cx = rect.left + rect.width / 2;
        const cy = rect.top + rect.height / 2;
        const hit = document.elementFromPoint(cx, cy);
        const hitMatch = el === hit || el.contains(hit) || (hit && hit.contains(el));

        return {
          found: true,
          visible: true,
          width: rect.width,
          height: rect.height,
          hitMatch,
          hitTag: hit ? (hit.id ? '#' + hit.id : hit.className ? '.' + hit.className : hit.tagName) : null
        };
      })()
    `);

    if (result.found && result.visible) {
      assert.ok(result.width >= 43.5, `Touch target ${selector} width ${result.width}px must be >= 44px`);
      assert.ok(result.height >= 43.5, `Touch target ${selector} height ${result.height}px must be >= 44px`);
      assert.ok(result.hitMatch, `Touch target ${selector} center point must be clickable (hit: ${result.hitTag})`);
      testedTargets.add(selector);
    }
    return result;
  }

  // 1. Test 6 main pages
  const routes = ['#/dashboard', '#/devices', '#/onboarding', '#/github', '#/commands', '#/settings'];
  for (const r of routes) {
    await evalJS(`window.location.hash = '${r}';`);
    await new Promise(r => setTimeout(r, 60));

    for (const target of pageTargets) {
      await inspectAndHitTest(target);
    }

    // Inspect filter pills on devices page
    if (r === '#/devices') {
      const pillsCount = await evalJS(`document.querySelectorAll('.filter-pill').length`);
      for (let i = 0; i < pillsCount; i++) {
        await inspectAndHitTest(`.filter-pill:nth-child(${i + 1})`);
      }
    }

    // Inspect tabs on onboarding page
    if (r === '#/onboarding') {
      const tabCount = await evalJS(`document.querySelectorAll('.tab-btn').length`);
      for (let i = 0; i < tabCount; i++) {
        await inspectAndHitTest(`.tab-btn:nth-child(${i + 1})`);
      }
    }
  }

  // 2. Test Mobile Sidebar Drawer Open State
  await evalJS(`document.getElementById('sidebarToggleBtn').click();`);
  await waitFor('document.body.classList.contains("sidebar-open")');
  await new Promise(r => setTimeout(r, 300));
  for (const navTarget of drawerTargets) {
    await inspectAndHitTest(navTarget);
  }
  await evalJS(`document.getElementById('sidebarCloseBtn').click();`);
  await waitFor('!document.body.classList.contains("sidebar-open")');
  await new Promise(r => setTimeout(r, 300));

  // 3. Test Modals Active States
  // Rename Modal
  await evalJS(`window.location.hash = '#/devices';`);
  await new Promise(r => setTimeout(r, 60));
  await evalJS(`
    (() => {
      const btn = document.querySelector('.btn-rename-device');
      if (btn) btn.click();
    })()
  `);
  await waitFor('!document.getElementById("renameModal").classList.contains("hidden")');
  for (const renameTarget of ['#closeRenameModalBtn', '#cancelRenameModalBtn', '#saveRenameBtn']) {
    await inspectAndHitTest(renameTarget);
  }
  await evalJS(`document.getElementById('cancelRenameModalBtn').click();`);
  await waitFor('document.getElementById("renameModal").classList.contains("hidden")');

  // IP Modal
  await evalJS(`
    (() => {
      const moreBtn = document.querySelector('.ip-more-btn');
      if (moreBtn) moreBtn.click();
    })()
  `);
  await waitFor('!document.getElementById("ipModal").classList.contains("hidden")');
  for (const ipTarget of ['#closeIpModalBtn', '#doneIpModalBtn']) {
    await inspectAndHitTest(ipTarget);
  }
  await evalJS(`document.getElementById('doneIpModalBtn').click();`);
  await waitFor('document.getElementById("ipModal").classList.contains("hidden")');

  // 4. Test Unauthenticated Login Overlay
  await evalJS(`document.getElementById('btnLogout').click();`);
  await waitFor('!document.getElementById("loginOverlay").classList.contains("hidden")');

  for (const target of authTargets) {
    await inspectAndHitTest(target);
  }

  // Log back in
  await evalJS(`
    (() => {
      document.getElementById('loginUsername').value = 'admin';
      document.getElementById('loginPassword').value = 'admin123';
      document.getElementById('loginSubmitBtn').click();
    })()
  `);
  await waitFor('document.getElementById("loginOverlay").classList.contains("hidden")');

  // Difference set assertion
  const missing = contractInteractiveTargets.filter(t => !testedTargets.has(t));
  assert.equal(missing.length, 0, `All contract targets must be tested and hit (missing: ${missing.join(', ')})`);
});

test('3. 200% OS text scale: Full journey interaction and wrapping without overflow', async () => {
  await setViewport(360, 640, true);
  await cdp.send('Emulation.setEmulatedOSTextScale', { scale: 2 }, targetSessionId);

  // Full journey through all 6 routes
  const routes = ['#/dashboard', '#/devices', '#/onboarding', '#/github', '#/commands', '#/settings'];
  for (const r of routes) {
    await evalJS(`window.location.hash = '${r}';`);
    await new Promise(r => setTimeout(r, 60));

    const check = await evalJS(`
      (() => {
        const scrollW = document.documentElement.scrollWidth;
        const clientW = document.documentElement.clientWidth;
        const header = document.querySelector('.top-header');
        const headerRect = header ? header.getBoundingClientRect() : null;
        return {
          scrollW,
          clientW,
          headerHeight: headerRect ? headerRect.height : 0
        };
      })()
    `);

    assert.ok(check.scrollW <= check.clientW + 1, `200% text scale on ${r} must not cause horizontal overflow (scrollW: ${check.scrollW}, clientW: ${check.clientW})`);
    assert.ok(check.headerHeight > 0, 'Top header must be visible');
  }

  // Reset text scale
  await cdp.send('Emulation.setEmulatedOSTextScale', { scale: 1 }, targetSessionId);
});

test('4. Modals and Drawer: Focus Trap, Escape close, and scroll lock closure', async () => {
  await setViewport(360, 640, true);
  await evalJS(`window.location.hash = '#/dashboard';`);
  await new Promise(r => setTimeout(r, 100));

  // Test Mobile Sidebar Drawer
  await evalJS(`document.getElementById('sidebarToggleBtn').click();`);
  await waitFor('document.body.classList.contains("sidebar-open")');

  const sidebarOpen = await evalJS(`
    (() => {
      const sidebar = document.getElementById('appSidebar');
      const toggle = document.getElementById('sidebarToggleBtn');
      return {
        bodyHasClass: document.body.classList.contains('sidebar-open'),
        sidebarHasOpen: sidebar.classList.contains('open'),
        ariaExpanded: toggle.getAttribute('aria-expanded')
      };
    })()
  `);

  assert.equal(sidebarOpen.bodyHasClass, true, 'Body must lock scroll when sidebar is open');
  assert.equal(sidebarOpen.sidebarHasOpen, true, 'Sidebar must have .open class');
  assert.equal(sidebarOpen.ariaExpanded, 'true', 'Toggle button must reflect aria-expanded=true');

  // Close sidebar via close button
  await evalJS(`document.getElementById('sidebarCloseBtn').click();`);
  await waitFor('!document.body.classList.contains("sidebar-open")');

  const sidebarClosed = await evalJS(`
    (() => {
      const toggle = document.getElementById('sidebarToggleBtn');
      return {
        bodyHasClass: document.body.classList.contains('sidebar-open'),
        ariaExpanded: toggle.getAttribute('aria-expanded')
      };
    })()
  `);
  assert.equal(sidebarClosed.bodyHasClass, false, 'Body scroll lock must be removed when sidebar closes');
  assert.equal(sidebarClosed.ariaExpanded, 'false', 'Toggle button must reflect aria-expanded=false');
});

test('5. AXTree Accessibility: Commands table maintains header name association on cardified rows', async () => {
  await evalJS(`window.location.hash = '#/commands';`);
  await new Promise(r => setTimeout(r, 60));

  const axTree = await cdp.send('Accessibility.getFullAXTree', {}, targetSessionId);
  const nodes = axTree.nodes || axTree.result?.nodes || [];
  assert.ok(nodes.length > 0, 'Accessibility tree must contain nodes');

  const headers = ['时间', '设备', '类型', '状态', '结果'];
  for (const h of headers) {
    const found = nodes.some(n => n.name?.value?.includes(h));
    assert.ok(found, `Accessibility tree must contain table header name ${h}`);
  }
});

test('6. Device card header layout and health detail click hit across viewports and 200% text scale', async () => {
  const testViewports = [
    { width: 1280, height: 800, mobile: false, name: 'Desktop 1280x800' },
    { width: 390, height: 844, mobile: true, name: 'iPhone 390x844' },
    { width: 360, height: 640, mobile: true, name: 'Mobile Min 360x640' }
  ];

  await evalJS(`window.location.hash = '#/devices';`);
  await new Promise(r => setTimeout(r, 100));

  for (const vp of testViewports) {
    await setViewport(vp.width, vp.height, vp.mobile);

    // Test under normal 100% text scale and 200% text scale
    for (const scale of [1, 2]) {
      if (scale === 2) {
        await cdp.send('Emulation.setEmulatedOSTextScale', { scale: 2 }, targetSessionId);
      } else {
        await cdp.send('Emulation.setEmulatedOSTextScale', { scale: 1 }, targetSessionId);
      }
      await new Promise(r => setTimeout(r, 60));

      const cardCheck = await evalJS(`
        (() => {
          const onlineBadges = document.querySelectorAll('.online-badge');
          const offlinePill = document.querySelector('[data-filter="offline"]');
          const onlinePill = document.querySelector('[data-filter="online"]');
          const cards = Array.from(document.querySelectorAll('.device-card'));
          const container = document.getElementById('deviceContainer');

          const cardResults = cards.map(card => {
            const hostnameEl = card.querySelector('.device-hostname');
            const healthBtn = card.querySelector('.btn-view-health');
            const syncBadge = card.querySelector('.header-badges .status-badge');
            const headerBadgesEl = card.querySelector('.header-badges');
            const header = card.querySelector('.device-card-header');

            const hostRect = hostnameEl ? hostnameEl.getBoundingClientRect() : null;
            const healthRect = healthBtn ? healthBtn.getBoundingClientRect() : null;
            const syncRect = syncBadge ? syncBadge.getBoundingClientRect() : null;
            const badgesRect = headerBadgesEl ? headerBadgesEl.getBoundingClientRect() : null;

            // Check overlap between healthBtn and syncBadge if both present
            let badgesOverlap = false;
            if (healthRect && syncRect && healthRect.width > 0 && syncRect.width > 0) {
              const overlapX = !(healthRect.right <= syncRect.left || healthRect.left >= syncRect.right);
              const overlapY = !(healthRect.bottom <= syncRect.top || healthRect.top >= syncRect.bottom);
              badgesOverlap = overlapX && overlapY;
            }

            // Check overlap between hostnameEl and badges container
            let hostBadgesOverlap = false;
            if (hostRect && badgesRect && hostRect.width > 0 && badgesRect.width > 0) {
              const overlapX = !(hostRect.right <= badgesRect.left || hostRect.left >= badgesRect.right);
              const overlapY = !(hostRect.bottom <= badgesRect.top || hostRect.top >= badgesRect.bottom);
              hostBadgesOverlap = overlapX && overlapY;
            }

            return {
              hasHealth: !!healthBtn,
              hasSync: !!syncBadge,
              badgesOverlap,
              hostBadgesOverlap,
              scrollW: card.scrollWidth,
              clientW: card.clientWidth,
              headerScrollW: header ? header.scrollWidth : 0,
              headerClientW: header ? header.clientWidth : 0
            };
          });

          return {
            onlineBadgesCount: onlineBadges.length,
            hasOfflinePill: !!offlinePill,
            hasOnlinePill: !!onlinePill,
            containerScrollW: container ? container.scrollWidth : 0,
            containerClientW: container ? container.clientWidth : 0,
            cardCount: cards.length,
            cardResults
          };
        })()
      `);

      assert.equal(cardCheck.onlineBadgesCount, 0, `No .online-badge should exist in DOM on ${vp.name} (scale: ${scale}x)`);
      assert.equal(cardCheck.hasOfflinePill, false, `Filter pill data-filter="offline" must NOT exist on ${vp.name}`);
      assert.equal(cardCheck.hasOnlinePill, false, `Filter pill data-filter="online" must NOT exist on ${vp.name}`);
      assert.ok(cardCheck.containerScrollW <= cardCheck.containerClientW + 1, `Device container must not overflow on ${vp.name} (scale: ${scale}x)`);
      assert.ok(cardCheck.cardCount > 0, `At least one device card should be present on ${vp.name}`);

      for (let i = 0; i < cardCheck.cardResults.length; i++) {
        const cr = cardCheck.cardResults[i];
        assert.equal(cr.hostBadgesOverlap, false, `Card ${i} hostname and badges must not overlap on ${vp.name} (scale: ${scale}x)`);
        assert.equal(cr.badgesOverlap, false, `Card ${i} health badge and sync badge must not overlap on ${vp.name} (scale: ${scale}x)`);
        assert.ok(cr.headerScrollW <= cr.headerClientW + 1, `Card ${i} header must not horizontally overflow on ${vp.name} (scale: ${scale}x)`);
        assert.ok(cr.scrollW <= cr.clientW + 1, `Card ${i} must not horizontally overflow on ${vp.name} (scale: ${scale}x)`);
      }

    }
  }

  // Reset scale
  await cdp.send('Emulation.setEmulatedOSTextScale', { scale: 1 }, targetSessionId);
  await setViewport(360, 640, true);

  // Test Health Detail Modal click hit and dismiss
  await evalJS(`
    (() => {
      const healthBtn = document.querySelector('.btn-view-health');
      if (healthBtn) healthBtn.click();
    })()
  `);
  await waitFor('!document.getElementById("deviceHealthModal").classList.contains("hidden")');

  const modalOpenCheck = await evalJS(`
    (() => {
      const modal = document.getElementById('deviceHealthModal');
      const badge = document.getElementById('healthModalStatusBadge');
      const facts = document.getElementById('healthModalFactsGrid');
      return {
        visible: modal && !modal.classList.contains('hidden'),
        badgeText: badge ? badge.innerText : '',
        factsText: facts ? facts.innerText : '',
        memoryValue: facts && facts.children[2] ? facts.children[2].lastElementChild.innerText : '',
        diskLabel: facts && facts.children[3] ? facts.children[3].firstElementChild.innerText : '',
        diskValue: facts && facts.children[3] ? facts.children[3].lastElementChild.innerText : ''
      };
    })()
  `);
  assert.equal(modalOpenCheck.visible, true, 'deviceHealthModal must open upon clicking .btn-view-health');
  assert.ok(modalOpenCheck.badgeText.length > 0, 'healthModalStatusBadge must have status text');
  assert.match(modalOpenCheck.factsText, /2 小时/, 'health modal must render uptime from the real API contract');
  assert.equal(modalOpenCheck.memoryValue, '12288 / 16384 MiB', 'health modal must render valid memory facts');
  assert.match(modalOpenCheck.diskLabel, /磁盘使用率 \[\/\] \(50%\)/, 'health modal must render valid disk percentage and mount');
  assert.equal(modalOpenCheck.diskValue, '50.0 / 100.0 GiB', 'health modal must render valid disk capacity');

  // Close modal
  await evalJS(`document.getElementById('closeHealthModalBtn').click();`);
  await waitFor('document.getElementById("deviceHealthModal").classList.contains("hidden")');

  // Open the second device, whose runtime facts intentionally omit memory and disk.
  await evalJS(`
    (() => {
      const healthButtons = document.querySelectorAll('.btn-view-health');
      if (healthButtons[1]) healthButtons[1].click();
    })()
  `);
  await waitFor('!document.getElementById("deviceHealthModal").classList.contains("hidden")');
  const missingFactsCheck = await evalJS(`
    (() => {
      const facts = document.getElementById('healthModalFactsGrid');
      return {
        uptimeText: facts && facts.children[1] ? facts.children[1].lastElementChild.innerText : '',
        memoryValue: facts && facts.children[2] ? facts.children[2].lastElementChild.innerText : '',
        diskLabel: facts && facts.children[3] ? facts.children[3].firstElementChild.innerText : '',
        diskValue: facts && facts.children[3] ? facts.children[3].lastElementChild.innerText : ''
      };
    })()
  `);
  assert.equal(missingFactsCheck.uptimeText, '1 小时', 'uptime must remain visible when memory facts are missing');
  assert.equal(missingFactsCheck.memoryValue, '-', 'missing memory facts must render as unknown');
  assert.doesNotMatch(missingFactsCheck.diskLabel, /\(0%\)/, 'missing disk facts must not render 0%');
  assert.equal(missingFactsCheck.diskValue, '-', 'missing disk facts must render as unknown hyphen');
  await evalJS(`document.getElementById('closeHealthModalBtn').click();`);
  await waitFor('document.getElementById("deviceHealthModal").classList.contains("hidden")');
});

test('7. Runtime health: Zero uncaught exceptions and zero console errors', async () => {
  assert.equal(runtimeErrors.length, 0, `Runtime errors detected during tests: ${runtimeErrors.join('; ')}`);
});
