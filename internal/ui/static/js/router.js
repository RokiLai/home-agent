import { state } from './state.js';
import { fetchOrRefreshClaimToken } from './onboarding.js';

export const pageMeta = {
  dashboard: {
    title: '首页概览',
    desc: '全网健康统计看板、待处理异常设备与最近操作'
  },
  devices: {
    title: '设备管理',
    desc: '已接入主机的集中控制、配置同步与快捷操作'
  },
  commands: {
    title: '操作记录',
    desc: '控制命令的投递、接受与最终执行状态'
  },
  settings: {
    title: '系统设置',
    desc: '管理员会话与服务端通信参数配置'
  },
  deviceDetail: {
    title: '设备详情',
    desc: '设备运行概览、健康诊断、配置访问与操作'
  },
  onboarding: {
    title: '快速接入向导',
    desc: '单行命令零配置自动注册与守护进程自启'
  },
  users: {
    title: '用户与权限管理',
    desc: '多用户账号、角色分配、权限审计与登录安全'
  },
  github: {
    title: 'GitHub 凭据同步',
    desc: '统一 OAuth 授权、SSH Key 分发与 GitHub CLI Token 同步'
  }
};

const validDetailSections = new Set(['overview', 'health', 'ssh', 'network', 'commands', 'settings']);
const validSettingsSections = new Set(['all', 'general', 'version', 'network', 'users', 'github', 'about']);

export function parseRoute(hashString) {
  const clean = (hashString || '').replace(/^#\/?/, '').trim();
  if (!clean) return { page: 'dashboard', deviceId: null, section: null };
  const parts = clean.split('/').filter(Boolean);
  const root = parts[0];

  if (root === 'devices') {
    if (parts.length >= 2) {
      const rawSection = parts[2] || 'overview';
      const section = validDetailSections.has(rawSection) ? rawSection : 'overview';
      return {
        page: 'deviceDetail',
        deviceId: decodeURIComponent(parts[1]),
        section
      };
    }
    return { page: 'devices', deviceId: null, section: null };
  }

  if (root === 'settings') {
    const rawSection = parts[1];
    const section = rawSection && validSettingsSections.has(rawSection) ? rawSection : (rawSection ? 'general' : null);
    return { page: 'settings', deviceId: null, section };
  }

  if (pageMeta[root]) {
    return { page: root, deviceId: null, section: null };
  }

  return { page: 'dashboard', deviceId: null, section: null };
}

export function setupRouter(onRouteChanged) {
  function handleRoute() {
    const route = parseRoute(window.location.hash);
    switchPage(route.page, route);
    if (onRouteChanged) {
      onRouteChanged(route.page, route);
    }
  }

  window.addEventListener('hashchange', handleRoute);
  handleRoute();
}

export function switchPage(pageName, routeParams = {}) {
  if (!pageMeta[pageName]) pageName = 'dashboard';
  state.currentPage = pageName;
  if (routeParams.deviceId) {
    state.currentDetailDeviceId = routeParams.deviceId;
    state.currentDetailSection = routeParams.section || 'overview';
  }
  if (pageName === 'settings') {
    state.currentSettingsSection = routeParams.section || 'all';
  }

  const navItems = document.querySelectorAll('.nav-item');
  const currentPageTitle = document.getElementById('currentPageTitle');
  const currentPageDesc = document.getElementById('currentPageDesc');

  // Determine active nav item: map deviceDetail -> devices, and users/github -> settings
  let activeNavPage = pageName;
  if (pageName === 'deviceDetail') {
    activeNavPage = 'devices';
  } else if (pageName === 'users' || pageName === 'github') {
    activeNavPage = 'settings';
  }

  // Update Nav active classes
  navItems.forEach(item => {
    if (item.dataset.page === activeNavPage) {
      item.classList.add('active');
    } else {
      item.classList.remove('active');
    }
  });

  // Update Page View visibility
  document.querySelectorAll('.page-view').forEach(view => {
    view.classList.remove('active');
  });
  const activeView = document.getElementById('page' + pageName.charAt(0).toUpperCase() + pageName.slice(1));
  if (activeView) {
    activeView.classList.add('active');
  }

  // Update Header Title & Description
  if (currentPageTitle && pageMeta[pageName]) {
    currentPageTitle.innerText = pageMeta[pageName].title;
  }
  if (currentPageDesc && pageMeta[pageName]) {
    currentPageDesc.innerText = pageMeta[pageName].desc;
  }

  if (pageName === 'onboarding' && (!state.currentClaimToken || (state.claimTokenExpiresAt && new Date() > state.claimTokenExpiresAt))) {
    fetchOrRefreshClaimToken();
  }

  // Close Mobile Sidebar if opened
  closeMobileSidebar();
}

let sidebarTriggerElement = null;

export function openMobileSidebar(triggerEl) {
  const appSidebar = document.getElementById('appSidebar');
  const sidebarBackdrop = document.getElementById('sidebarBackdrop');
  const sidebarToggleBtn = document.getElementById('sidebarToggleBtn');
  const sidebarCloseBtn = document.getElementById('sidebarCloseBtn');

  sidebarTriggerElement = triggerEl || document.getElementById('sidebarToggleBtn') || (document.activeElement && document.activeElement !== document.body ? document.activeElement : null);

  if (appSidebar) appSidebar.classList.add('open');
  if (sidebarBackdrop) sidebarBackdrop.classList.remove('hidden');
  if (document.body && document.body.classList) {
    document.body.classList.add('sidebar-open');
  }
  if (sidebarToggleBtn) {
    sidebarToggleBtn.setAttribute('aria-expanded', 'true');
  }

  if (sidebarCloseBtn && typeof sidebarCloseBtn.focus === 'function') {
    try {
      sidebarCloseBtn.focus();
    } catch (_) {}
  }
}

export function closeMobileSidebar() {
  const appSidebar = document.getElementById('appSidebar');
  const sidebarBackdrop = document.getElementById('sidebarBackdrop');
  const sidebarToggleBtn = document.getElementById('sidebarToggleBtn');

  if (appSidebar) appSidebar.classList.remove('open');
  if (sidebarBackdrop) sidebarBackdrop.classList.add('hidden');
  if (document.body && document.body.classList) {
    document.body.classList.remove('sidebar-open');
  }
  if (sidebarToggleBtn) {
    sidebarToggleBtn.setAttribute('aria-expanded', 'false');
  }

  if (sidebarTriggerElement && typeof sidebarTriggerElement.focus === 'function' && (typeof document.body.contains !== 'function' || document.body.contains(sidebarTriggerElement))) {
    try {
      sidebarTriggerElement.focus();
    } catch (_) {}
  }
  sidebarTriggerElement = null;
}
