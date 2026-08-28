import { state } from './state.js';
import { showToast, spinRefresh, copyToClipboard, addLog } from './utils.js';
import { setupRouter, openMobileSidebar, closeMobileSidebar } from './router.js';
import { checkAuthStatus, handleLogin, handleLogout, handleChangePassword } from './auth.js';
import { fetchOrRefreshClaimToken, updateInstallCommand } from './onboarding.js';
import { initSettingsForm, saveSettingsForm, clearSettingsToken } from './settings.js';
import { openRenameModal, closeRenameModal, saveRenameDevice, closeIpModal, closeAllDropdowns, closeAllModals, closeActiveModal } from './modals.js';
import { fetchGitHubStatus, startGitHubDeviceFlow, closeGithubDeviceModal, handleDisconnectGitHub } from './github.js';
import { fetchDevices, handleSyncAll, handleUpgradeAll } from './devices/actions.js';
import { renderDevices } from './devices/render.js';
import { fetchCommands } from './commands.js';
import { fetchUsersList } from './users.js';

export function bindEventListeners() {
  const sidebarToggleBtn = document.getElementById('sidebarToggleBtn');
  const sidebarCloseBtn = document.getElementById('sidebarCloseBtn');
  const sidebarBackdrop = document.getElementById('sidebarBackdrop');

  const loginForm = document.getElementById('loginForm');
  const btnLogout = document.getElementById('btnLogout');

  const btnSyncAll = document.getElementById('btnSyncAll');
  const btnUpgradeAll = document.getElementById('btnUpgradeAll');
  const refreshBtn = document.getElementById('refreshBtn');

  const searchInput = document.getElementById('deviceSearchInput');
  const filterPills = document.querySelectorAll('.filter-pill');
  const tabBtns = document.querySelectorAll('.tab-btn');
  const btnRefreshClaimToken = document.getElementById('btnRefreshClaimToken');

  const copyCommandBtn = document.getElementById('copyCommandBtn');
  const dashboardCopyBtn = document.getElementById('dashboardCopyBtn');
  const installCode = document.getElementById('installCommandCode');
  const dashboardInstallPreviewCode = document.getElementById('dashboardInstallPreviewCode');

  const settingsSaveBtn = document.getElementById('settingsSaveBtn');
  const settingsClearBtn = document.getElementById('settingsClearBtn');
  const changePasswordForm = document.getElementById('changePasswordForm');

  const clearLogsBtn = document.getElementById('clearLogsBtn');
  const eventLogContainer = document.getElementById('eventLogContainer');

  const btnGithubLogin = document.getElementById('btnGithubLogin');
  const btnGithubDisconnect = document.getElementById('btnGithubDisconnect');
  const copyGithubUserCodeBtn = document.getElementById('copyGithubUserCodeBtn');
  const githubUserCodeDisplay = document.getElementById('githubUserCodeDisplay');
  const closeGithubDeviceModalBtn = document.getElementById('closeGithubDeviceModalBtn');
  const cancelGithubDeviceModalBtn = document.getElementById('cancelGithubDeviceModalBtn');

  const closeIpModalBtn = document.getElementById('closeIpModalBtn');
  const doneIpModalBtn = document.getElementById('doneIpModalBtn');

  const closeRenameModalBtn = document.getElementById('closeRenameModalBtn');
  const cancelRenameModalBtn = document.getElementById('cancelRenameModalBtn');
  const saveRenameBtn = document.getElementById('saveRenameBtn');
  const deviceAliasInput = document.getElementById('deviceAliasInput');

  // Mobile Sidebar
  if (sidebarToggleBtn) sidebarToggleBtn.addEventListener('click', openMobileSidebar);
  if (sidebarCloseBtn) sidebarCloseBtn.addEventListener('click', closeMobileSidebar);
  if (sidebarBackdrop) sidebarBackdrop.addEventListener('click', closeMobileSidebar);

  // Auth Actions
  if (loginForm) loginForm.addEventListener('submit', (e) => handleLogin(e, async () => {
    await fetchDevices();
    await fetchGitHubStatus();
    await fetchCommands();
    if (state.currentUser && state.currentUser.role === 'owner') {
      await fetchUsersList();
    }
  }));
  if (btnLogout) btnLogout.addEventListener('click', handleLogout);

  // Global Actions
  if (btnSyncAll) btnSyncAll.addEventListener('click', handleSyncAll);
  if (btnUpgradeAll) btnUpgradeAll.addEventListener('click', handleUpgradeAll);
  if (refreshBtn) {
    refreshBtn.addEventListener('click', () => {
      spinRefresh();
      fetchDevices();
      fetchGitHubStatus();
      fetchCommands();
      if (state.currentUser && state.currentUser.role === 'owner') {
        fetchUsersList();
      }
    });
  }

  // Search & Filter
  if (searchInput) {
    searchInput.addEventListener('input', () => {
      renderDevices();
    });
  }

  filterPills.forEach(pill => {
    pill.addEventListener('click', () => {
      filterPills.forEach(p => p.classList.remove('active'));
      pill.classList.add('active');
      state.currentFilter = pill.dataset.filter || 'all';
      renderDevices();
    });
  });

  // OS Tabs in Onboarding
  tabBtns.forEach(btn => {
    btn.addEventListener('click', () => {
      tabBtns.forEach(b => b.classList.remove('active'));
      btn.classList.add('active');
      state.activeOSTab = btn.dataset.os;
      updateInstallCommand();
    });
  });

  // Claim Token refresh button
  if (btnRefreshClaimToken) {
    btnRefreshClaimToken.addEventListener('click', () => {
      fetchOrRefreshClaimToken();
      showToast('已重新生成 Claim Token');
    });
  }

  // Copy command buttons
  if (copyCommandBtn) {
    copyCommandBtn.addEventListener('click', () => {
      copyToClipboard(installCode ? installCode.innerText : '', '安装命令已复制');
    });
  }
  if (dashboardCopyBtn) {
    dashboardCopyBtn.addEventListener('click', () => {
      copyToClipboard(dashboardInstallPreviewCode ? dashboardInstallPreviewCode.innerText : '', '安装命令已复制');
    });
  }

  // Settings Page Actions
  if (settingsSaveBtn) settingsSaveBtn.addEventListener('click', () => saveSettingsForm(() => {
    fetchDevices();
    fetchGitHubStatus();
	fetchCommands();
  }));
  if (settingsClearBtn) settingsClearBtn.addEventListener('click', () => clearSettingsToken(() => {
    fetchDevices();
  }));
  if (changePasswordForm) changePasswordForm.addEventListener('submit', handleChangePassword);

  // Logs
  if (clearLogsBtn) {
    clearLogsBtn.addEventListener('click', () => {
      if (eventLogContainer) {
        eventLogContainer.innerHTML = '';
        addLog('info', '日志已清空');
      }
    });
  }

  // Dropdown close on outside click or ESC key
  document.addEventListener('click', (e) => {
    if (!e.target.closest('.dropdown-wrapper')) {
      closeAllDropdowns();
    }
  });

  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') {
      closeAllDropdowns();
      closeActiveModal();
      closeMobileSidebar();
    }
  });

  // GitHub DOM Actions
  if (btnGithubLogin) btnGithubLogin.addEventListener('click', () => startGitHubDeviceFlow(async () => {
    await fetchGitHubStatus();
    await fetchDevices();
  }));
  if (btnGithubDisconnect) btnGithubDisconnect.addEventListener('click', () => handleDisconnectGitHub(async () => {
    await fetchGitHubStatus();
    await fetchDevices();
  }));
  if (copyGithubUserCodeBtn) {
    copyGithubUserCodeBtn.addEventListener('click', () => {
      const code = githubUserCodeDisplay ? githubUserCodeDisplay.innerText : '';
      copyToClipboard(code, '验证码已复制');
    });
  }
  if (closeGithubDeviceModalBtn) closeGithubDeviceModalBtn.addEventListener('click', closeGithubDeviceModal);
  if (cancelGithubDeviceModalBtn) cancelGithubDeviceModalBtn.addEventListener('click', closeGithubDeviceModal);

  // IP Modal
  if (closeIpModalBtn) closeIpModalBtn.addEventListener('click', closeIpModal);
  if (doneIpModalBtn) doneIpModalBtn.addEventListener('click', closeIpModal);

  // Rename Modal
  if (closeRenameModalBtn) closeRenameModalBtn.addEventListener('click', closeRenameModal);
  if (cancelRenameModalBtn) cancelRenameModalBtn.addEventListener('click', closeRenameModal);
  if (saveRenameBtn) saveRenameBtn.addEventListener('click', () => saveRenameDevice(fetchDevices));
  if (deviceAliasInput) {
    deviceAliasInput.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') {
        e.preventDefault();
        saveRenameDevice(fetchDevices);
      }
    });
  }
}

export async function init() {
  setupRouter((page) => {
    if (page === 'users' && state.currentUser && state.currentUser.role === 'owner') {
      fetchUsersList();
    }
  });
  bindEventListeners();
  initSettingsForm();

  const ok = await checkAuthStatus();
  if (ok) {
    fetchDevices();
    fetchGitHubStatus();
    fetchCommands();
    if (state.currentUser && state.currentUser.role === 'owner') {
      fetchUsersList();
    }
  }

  // Auto refresh poll every 4 seconds
  setInterval(() => {
    if (state.isAuthenticated) {
      fetchDevices();
      fetchGitHubStatus();
      fetchCommands();
      if (state.currentPage === 'users' && state.currentUser && state.currentUser.role === 'owner') {
        fetchUsersList();
      }
    }
  }, 4000);
}

if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', init);
} else {
  init();
}
