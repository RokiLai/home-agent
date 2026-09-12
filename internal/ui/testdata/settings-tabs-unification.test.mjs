import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';

globalThis.localStorage = {
  getItem() { return null; },
  setItem() {},
  removeItem() {}
};

globalThis.fetch = async () => ({
  ok: true,
  json: async () => ({})
});

// ---------------------------------------------------------------------------
// 1. Static HTML Contract Tests for Sidebar, Settings Tabs, and DOM Container
// ---------------------------------------------------------------------------

test('index.html contract: sidebar navigation has no isolated github/users and retains 4 core navs', async () => {
  const html = await readFile(new URL('../static/index.html', import.meta.url), 'utf8');

  // Extract the sidebar nav
  const sidebarMatch = html.match(/<nav class="sidebar-nav">([\s\S]*?)<\/nav>/i);
  assert.ok(sidebarMatch, 'sidebar-nav must exist in index.html');
  const sidebarContent = sidebarMatch[1];

  // Negative assertions: sidebar MUST NOT contain navGithub or navUsers
  assert.equal(/id="navGithub"/i.test(sidebarContent), false, 'sidebar-nav must not contain id="navGithub"');
  assert.equal(/id="navUsers"/i.test(sidebarContent), false, 'sidebar-nav must not contain id="navUsers"');
  assert.equal(/href="#\/github"/i.test(sidebarContent), false, 'sidebar-nav must not contain link href="#/github"');
  assert.equal(/href="#\/users"/i.test(sidebarContent), false, 'sidebar-nav must not contain link href="#/users"');

  // Positive assertions: sidebar must contain core 4 primary navigation items + onboarding
  assert.ok(/id="navDashboard"/i.test(sidebarContent), 'sidebar-nav must contain navDashboard');
  assert.ok(/id="navDevices"/i.test(sidebarContent), 'sidebar-nav must contain navDevices');
  assert.ok(/id="navCommands"/i.test(sidebarContent), 'sidebar-nav must contain navCommands');
  assert.ok(/id="navSettings"/i.test(sidebarContent), 'sidebar-nav must contain navSettings');
  assert.ok(/id="navOnboarding"/i.test(sidebarContent), 'sidebar-nav must contain navOnboarding');
});

test('index.html contract: settings tab bar contains 7 unified in-page tabs and no external redirects', async () => {
  const html = await readFile(new URL('../static/index.html', import.meta.url), 'utf8');

  const tabNavMatch = html.match(/<div class="[^"]*settings-tab-nav[^"]*"[^>]*>([\s\S]*?)<\/div>/i);
  assert.ok(tabNavMatch, 'settings-tab-nav must exist in index.html');
  const tabNavContent = tabNavMatch[1];

  // Negative assertions: settings tabs must NOT jump to isolated routes #/users or #/github
  assert.equal(/location\.hash\s*=\s*['"]#\/users['"]/i.test(tabNavContent), false, 'settings tabs must not jump to #/users');
  assert.equal(/location\.hash\s*=\s*['"]#\/github['"]/i.test(tabNavContent), false, 'settings tabs must not jump to #/github');

  // Positive assertions: verify all 7 tabs and their route targets
  const expectedTabs = [
    { section: 'all', hash: '#/settings' },
    { section: 'general', hash: '#/settings/general' },
    { section: 'version', hash: '#/settings/version' },
    { section: 'network', hash: '#/settings/network' },
    { section: 'users', hash: '#/settings/users', id: 'settingsTabUsers' },
    { section: 'github', hash: '#/settings/github', id: 'settingsTabGithub' },
    { section: 'about', hash: '#/settings/about' }
  ];

  for (const tab of expectedTabs) {
    const pattern = new RegExp(`data-section="${tab.section}"[\\s\\S]*?location\\.hash\\s*=\\s*['"]${tab.hash.replace('/', '\\/')}['"]`, 'i');
    assert.ok(pattern.test(tabNavContent), `settings tab for section "${tab.section}" must target ${tab.hash}`);
  }

  // Verify settingsTabUsers has id and initially contains hidden class
  assert.ok(/id="settingsTabUsers"/i.test(tabNavContent), 'settingsTabUsers must have id="settingsTabUsers"');
  assert.ok(/id="settingsTabUsers"[^>]*\bhidden\b|\bhidden\b[^>]*id="settingsTabUsers"/i.test(tabNavContent), 'settingsTabUsers must initially contain hidden class');
  assert.match(tabNavContent, /id="settingsTabGithub"/i, 'settingsTabGithub must have id="settingsTabGithub"');
});

test('index.html contract: settings sections container holds users and github with single card hierarchy', async () => {
  const html = await readFile(new URL('../static/index.html', import.meta.url), 'utf8');

  // Negative assertion: no top-level page-view sections for github or users
  assert.equal(/<section id="pageGithub" class="page-view"/i.test(html), false, 'pageGithub must not be a top-level page-view');
  assert.equal(/<section id="pageUsers" class="page-view"/i.test(html), false, 'pageUsers must not be a top-level page-view');

  // Positive assertion: settingsSecUsers and settingsSecGithub are inside pageSettings
  const pageSettingsMatch = html.match(/<section id="pageSettings" class="page-view">([\s\S]*?)<\/section>\s*<\/main>/i);
  assert.ok(pageSettingsMatch, 'pageSettings must exist');
  const containerContent = pageSettingsMatch[1];

  assert.ok(containerContent.includes('id="settingsSecUsers"'), 'settingsSecUsers must be inside pageSettings');
  assert.ok(containerContent.includes('id="settingsSecGithub"'), 'settingsSecGithub must be inside pageSettings');

  // Verify required IDs remain intact
  const requiredSubIds = [
    'pageUsers',
    'pageGithub',
    'usersTableBody',
    'githubCardBody',
    'githubLoading',
    'githubConnected',
    'githubDisconnected',
    'btnGithubLogin',
    'btnGithubDisconnect',
    'githubDeviceMatrix'
  ];

  for (const subId of requiredSubIds) {
    assert.ok(containerContent.includes(`id="${subId}"`), `settings-container must retain sub-id="${subId}"`);
  }
});

// ---------------------------------------------------------------------------
// 2. JavaScript Routing, Fallback, and RBAC Integration Contract Tests
// ---------------------------------------------------------------------------

test('router contract: parseRoute resolves settings sections and maps legacy #/users & #/github', async () => {
  const { parseRoute } = await import('../static/js/router.js');

  // Test root settings defaults to section: 'all'
  const routeSettings = parseRoute('#/settings');
  assert.equal(routeSettings.page, 'settings');
  assert.equal(routeSettings.section, 'all');

  // Test explicit settings sections
  assert.deepEqual(parseRoute('#/settings/all'), { page: 'settings', deviceId: null, section: 'all' });
  assert.deepEqual(parseRoute('#/settings/general'), { page: 'settings', deviceId: null, section: 'general' });
  assert.deepEqual(parseRoute('#/settings/users'), { page: 'settings', deviceId: null, section: 'users' });
  assert.deepEqual(parseRoute('#/settings/github'), { page: 'settings', deviceId: null, section: 'github' });
  assert.deepEqual(parseRoute('#/settings/version'), { page: 'settings', deviceId: null, section: 'version' });
  assert.deepEqual(parseRoute('#/settings/network'), { page: 'settings', deviceId: null, section: 'network' });
  assert.deepEqual(parseRoute('#/settings/about'), { page: 'settings', deviceId: null, section: 'about' });

  // Test invalid settings section falls back to 'general'
  assert.deepEqual(parseRoute('#/settings/unknownSection'), { page: 'settings', deviceId: null, section: 'general' });

  // Test legacy routes #/users and #/github mapped to settings sub-sections
  assert.deepEqual(parseRoute('#/users'), { page: 'settings', deviceId: null, section: 'users' });
  assert.deepEqual(parseRoute('#/github'), { page: 'settings', deviceId: null, section: 'github' });
});

test('router contract: switchPage coerces users and github to settings and activates pageSettings', async () => {
  const { switchPage } = await import('../static/js/router.js');
  const { state } = await import('../static/js/state.js');

  const classes = (init = '') => {
    const set = new Set(init.split(/\s+/).filter(Boolean));
    return {
      add(...names) { names.forEach(n => set.add(n)); },
      remove(...names) { names.forEach(n => set.delete(n)); },
      contains(n) { return set.has(n); }
    };
  };

  const navSettings = { id: 'navSettings', dataset: { page: 'settings' }, classList: classes() };
  const pageSettings = { id: 'pageSettings', classList: classes() };

  globalThis.document = {
    querySelectorAll(sel) {
      if (sel === '.nav-item') return [navSettings];
      if (sel === '.page-view') return [pageSettings];
      return [];
    },
    getElementById(id) {
      if (id === 'pageSettings') return pageSettings;
      if (id === 'navSettings') return navSettings;
      return null;
    }
  };

  // Switch to users
  switchPage('users');
  assert.equal(state.currentPage, 'settings');
  assert.equal(state.currentSettingsSection, 'users');
  assert.equal(navSettings.classList.contains('active'), true);
  assert.equal(pageSettings.classList.contains('active'), true);

  // Switch to github
  switchPage('github');
  assert.equal(state.currentPage, 'settings');
  assert.equal(state.currentSettingsSection, 'github');
  assert.equal(navSettings.classList.contains('active'), true);
  assert.equal(pageSettings.classList.contains('active'), true);
});

test('settings contract: switchSettingsSection toggles active sections and enforces RBAC non-owner fallback', async () => {
  const { state } = await import('../static/js/state.js');
  const { switchSettingsSection } = await import('../static/js/settings.js');

  const createFakeEl = (id, section) => {
    const set = new Set();
    return {
      id,
      dataset: { section },
      classList: {
        add(n) { set.add(n); },
        remove(n) { set.delete(n); },
        contains(n) { return set.has(n); }
      }
    };
  };

  const secGeneral = createFakeEl('settingsSecGeneral', 'general');
  const secUsers = createFakeEl('settingsSecUsers', 'users');
  const secGithub = createFakeEl('settingsSecGithub', 'github');
  const tabGeneral = createFakeEl('tabGeneral', 'general');
  const tabUsers = createFakeEl('tabUsers', 'users');
  const tabGithub = createFakeEl('tabGithub', 'github');
  const tabAll = createFakeEl('tabAll', 'all');

  const allSections = [secGeneral, secUsers, secGithub];
  const allTabs = [tabAll, tabGeneral, tabUsers, tabGithub];

  globalThis.document.querySelectorAll = (sel) => {
    if (sel === '.settings-section') return allSections;
    if (sel === '.settings-tab') return allTabs;
    return [];
  };

  // Scenario 1: Owner switches to users section
  state.currentUser = { role: 'owner' };
  switchSettingsSection('users');
  assert.equal(state.currentSettingsSection, 'users');
  assert.equal(secUsers.classList.contains('active'), true);
  assert.equal(secGeneral.classList.contains('active'), false);
  assert.equal(tabUsers.classList.contains('active'), true);
  assert.equal(tabGeneral.classList.contains('active'), false);

  // Scenario 2: Non-owner attempts to switch to users section -> falls back to general
  state.currentUser = { role: 'viewer' };
  switchSettingsSection('users');
  assert.equal(state.currentSettingsSection, 'general');
  assert.equal(secGeneral.classList.contains('active'), true);
  assert.equal(secUsers.classList.contains('active'), false);
  assert.equal(tabGeneral.classList.contains('active'), true);
  assert.equal(tabUsers.classList.contains('active'), false);

  // Scenario 3: Switch to github section
  switchSettingsSection('github');
  assert.equal(state.currentSettingsSection, 'github');
  assert.equal(secGithub.classList.contains('active'), true);
  assert.equal(secGeneral.classList.contains('active'), false);
  assert.equal(tabGithub.classList.contains('active'), true);

  // Scenario 4: Switch to all section
  switchSettingsSection('all');
  assert.equal(state.currentSettingsSection, 'all');
  assert.equal(secGeneral.classList.contains('active'), true);
  assert.equal(secGithub.classList.contains('active'), true);
  assert.equal(tabAll.classList.contains('active'), true);
  assert.equal(tabGeneral.classList.contains('active'), false);
});

