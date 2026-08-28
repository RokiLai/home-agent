import { state } from './state.js';
import { fetchOrRefreshClaimToken } from './onboarding.js';

export const pageMeta = {
  dashboard: {
    title: '仪表盘概览',
    desc: '全网设备状态监控与控制平面总览'
  },
  devices: {
    title: '设备管理',
    desc: '已接入主机的集中控制、配置同步与远程唤醒'
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
  },
  commands: {
    title: '操作历史',
    desc: '控制命令的投递、接受与最终执行状态'
  },
  settings: {
    title: '系统设置',
    desc: '管理员会话与服务端通信参数配置'
  }
};

export function setupRouter(onRouteChanged) {
  function handleRoute() {
    const hash = window.location.hash.replace('#/', '').trim();
    const targetPage = pageMeta[hash] ? hash : 'dashboard';
    switchPage(targetPage);
    if (onRouteChanged) {
      onRouteChanged(targetPage);
    }
  }

  window.addEventListener('hashchange', handleRoute);
  handleRoute();
}

export function switchPage(pageName) {
  if (!pageMeta[pageName]) pageName = 'dashboard';
  state.currentPage = pageName;

  const navItems = document.querySelectorAll('.nav-item');
  const currentPageTitle = document.getElementById('currentPageTitle');
  const currentPageDesc = document.getElementById('currentPageDesc');

  // Update Nav active classes
  navItems.forEach(item => {
    if (item.dataset.page === pageName) {
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
