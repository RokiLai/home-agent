import test from 'node:test';
import assert from 'node:assert/strict';

// Global mocks for Node environment
globalThis.localStorage = {
  getItem() { return null; },
  setItem() {},
  removeItem() {}
};
globalThis.window = {
  location: { origin: 'http://homeagent.test', hash: '#/settings' },
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
// 1. Settings Multi-section and Legacy Route Resolution Tests
// ---------------------------------------------------------------------------

test('router parseRoute parses settings sections and resolves legacy routes (#/users, #/github)', async () => {
  const { parseRoute } = await import('../static/js/router.js');

  assert.deepEqual(parseRoute('settings'), { page: 'settings', deviceId: null, section: null });
  assert.deepEqual(parseRoute('settings/general'), { page: 'settings', deviceId: null, section: 'general' });
  assert.deepEqual(parseRoute('settings/users'), { page: 'settings', deviceId: null, section: 'users' });
  assert.deepEqual(parseRoute('settings/github'), { page: 'settings', deviceId: null, section: 'github' });
  assert.deepEqual(parseRoute('settings/version'), { page: 'settings', deviceId: null, section: 'version' });
  assert.deepEqual(parseRoute('settings/network'), { page: 'settings', deviceId: null, section: 'network' });
  assert.deepEqual(parseRoute('settings/about'), { page: 'settings', deviceId: null, section: 'about' });

  // Invalid settings section falls back to general
  assert.deepEqual(parseRoute('settings/unknown_section'), { page: 'settings', deviceId: null, section: 'general' });

  // Legacy route backward compatibility: #/users and #/github
  assert.deepEqual(parseRoute('users'), { page: 'users', deviceId: null, section: null });
  assert.deepEqual(parseRoute('github'), { page: 'github', deviceId: null, section: null });
  assert.deepEqual(parseRoute('onboarding'), { page: 'onboarding', deviceId: null, section: null });
});

test('switchPage activates settings parent navigation for users and github and switches settings section views', async () => {
  const domNodes = new Map();
  const pageViews = ['Dashboard', 'Devices', 'Commands', 'Settings', 'Onboarding', 'Github', 'Users', 'DeviceDetail'];

  pageViews.forEach(name => {
    domNodes.set(`page${name}`, createElement({ id: `page${name}`, className: 'page-view' }));
  });

  const navDashboard = createElement({ id: 'navDashboard', className: 'nav-item', dataset: { page: 'dashboard' } });
  const navDevices = createElement({ id: 'navDevices', className: 'nav-item', dataset: { page: 'devices' } });
  const navCommands = createElement({ id: 'navCommands', className: 'nav-item', dataset: { page: 'commands' } });
  const navSettings = createElement({ id: 'navSettings', className: 'nav-item', dataset: { page: 'settings' } });
  const navUsers = createElement({ id: 'navUsers', className: 'nav-item', dataset: { page: 'users' } });
  const navGithub = createElement({ id: 'navGithub', className: 'nav-item', dataset: { page: 'github' } });
  const navOnboarding = createElement({ id: 'navOnboarding', className: 'nav-item', dataset: { page: 'onboarding' } });

  domNodes.set('navDashboard', navDashboard);
  domNodes.set('navDevices', navDevices);
  domNodes.set('navCommands', navCommands);
  domNodes.set('navSettings', navSettings);
  domNodes.set('navUsers', navUsers);
  domNodes.set('navGithub', navGithub);
  domNodes.set('navOnboarding', navOnboarding);

  const titleEl = createElement({ id: 'currentPageTitle' });
  const descEl = createElement({ id: 'currentPageDesc' });
  domNodes.set('currentPageTitle', titleEl);
  domNodes.set('currentPageDesc', descEl);

  globalThis.document = {
    getElementById(id) { return domNodes.get(id) || null; },
    querySelectorAll(selector) {
      if (selector === '.nav-item') return [navDashboard, navDevices, navCommands, navSettings, navUsers, navGithub, navOnboarding];
      if (selector === '.page-view') return pageViews.map(n => domNodes.get(`page${n}`));
      if (selector === '.settings-section') return [];
      return [];
    }
  };

  const { switchPage } = await import('../static/js/router.js');
  const { state } = await import('../static/js/state.js');

  // Test 1: Switch to settings directly
  switchPage('settings', { section: 'general' });
  assert.equal(state.currentPage, 'settings');
  assert.equal(navSettings.classList.contains('active'), true);
  assert.equal(domNodes.get('pageSettings').classList.contains('active'), true);

  // Test 2: Switch to users legacy page maps parent nav to settings
  switchPage('users');
  assert.equal(state.currentPage, 'users');
  assert.equal(navSettings.classList.contains('active'), true, 'Parent nav for users should highlight navSettings');
  assert.equal(domNodes.get('pageUsers').classList.contains('active'), true);

  // Test 3: Switch to github legacy page maps parent nav to settings
  switchPage('github');
  assert.equal(state.currentPage, 'github');
  assert.equal(navSettings.classList.contains('active'), true, 'Parent nav for github should highlight navSettings');
  assert.equal(domNodes.get('pageGithub').classList.contains('active'), true);
});

test('settings section switching activates corresponding section and loads data', async () => {
  const { state } = await import('../static/js/state.js');
  const { switchSettingsSection } = await import('../static/js/settings.js');

  const secGeneral = createElement({ id: 'settingsSecGeneral', className: 'settings-section' });
  const secVersion = createElement({ id: 'settingsSecVersion', className: 'settings-section' });
  const secNetwork = createElement({ id: 'settingsSecNetwork', className: 'settings-section' });
  const secAbout = createElement({ id: 'settingsSecAbout', className: 'settings-section' });

  const tabGeneral = createElement({ dataset: { section: 'general' }, className: 'filter-pill settings-tab active' });
  const tabVersion = createElement({ dataset: { section: 'version' }, className: 'filter-pill settings-tab' });
  const tabNetwork = createElement({ dataset: { section: 'network' }, className: 'filter-pill settings-tab' });
  const tabAbout = createElement({ dataset: { section: 'about' }, className: 'filter-pill settings-tab' });

  const sections = [secGeneral, secVersion, secNetwork, secAbout];
  const tabs = [tabGeneral, tabVersion, tabNetwork, tabAbout];

  globalThis.document = {
    getElementById(id) {
      if (id === 'settingsSecGeneral') return secGeneral;
      if (id === 'settingsSecVersion') return secVersion;
      if (id === 'settingsSecNetwork') return secNetwork;
      if (id === 'settingsSecAbout') return secAbout;
      return null;
    },
    querySelectorAll(selector) {
      if (selector === '.settings-section') return sections;
      if (selector === '.settings-tab') return tabs;
      return [];
    }
  };

  switchSettingsSection('version');
  assert.equal(state.currentSettingsSection, 'version');
  assert.equal(secVersion.classList.contains('active'), true);
  assert.equal(secGeneral.classList.contains('active'), false);
  assert.equal(tabVersion.classList.contains('active'), true);
  assert.equal(tabGeneral.classList.contains('active'), false);
});
