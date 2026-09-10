import { state, sanitizeHost } from './state.js';
import { showToast, addLog } from './utils.js';
import { updateInstallCommand } from './onboarding.js';
import { apiFetch } from './api.js';

export function initSettingsForm() {
  const settingsServerUrlInput = document.getElementById('settingsServerUrlInput');
  if (settingsServerUrlInput) {
    settingsServerUrlInput.value = localStorage.getItem('homeagent_server_url') || '';
  }

  initServerNetworkEvents();
  loadServerNetworkSettings();

  window.addEventListener('hashchange', () => {
    if (window.location.hash === '#/settings') {
      loadServerNetworkSettings();
    }
  });
}

function initServerNetworkEvents() {
  const switchEl = document.getElementById('serverNetworkEnabledSwitch');
  const saveBtn = document.getElementById('serverNetworkSaveBtn');

  if (switchEl) {
    switchEl.addEventListener('change', () => {
      updateServerNetworkFieldState(switchEl.checked);
    });
  }

  if (saveBtn) {
    saveBtn.addEventListener('click', () => {
      saveServerNetworkSettings();
    });
  }
}

function updateServerNetworkFieldState(enabled) {
  const fields = document.getElementById('serverNetworkFields');
  const ifaceInput = document.getElementById('serverNetworkInterfaceInput');
  const recordsInput = document.getElementById('serverNetworkRecordsInput');

  if (fields) {
    fields.style.opacity = enabled ? '1' : '0.5';
  }
  if (ifaceInput) {
    ifaceInput.disabled = !enabled;
  }
  if (recordsInput) {
    recordsInput.disabled = !enabled;
  }
}

function renderServerNetworkStatus(data) {
  const switchEl = document.getElementById('serverNetworkEnabledSwitch');
  const ifaceInput = document.getElementById('serverNetworkInterfaceInput');
  const recordsInput = document.getElementById('serverNetworkRecordsInput');
  const badgeEl = document.getElementById('serverNetworkStatusBadge');
  const currentIpEl = document.getElementById('serverNetworkCurrentIp');
  const lastSyncEl = document.getElementById('serverNetworkLastSyncTime');

  if (switchEl) {
    switchEl.checked = !!data.enabled;
    updateServerNetworkFieldState(!!data.enabled);
  }
  if (ifaceInput) {
    ifaceInput.value = data.interface || '';
  }
  if (recordsInput) {
    recordsInput.value = (data.records || []).join(', ');
  }
  if (currentIpEl) {
    currentIpEl.textContent = data.current_ip || '未探测';
  }
  if (lastSyncEl) {
    lastSyncEl.textContent = data.last_sync ? new Date(data.last_sync).toLocaleString() : '-';
  }

  if (badgeEl) {
    badgeEl.className = 'badge';
    if (!data.enabled) {
      badgeEl.classList.add('badge-secondary');
      badgeEl.textContent = '未启用';
      badgeEl.title = '';
    } else {
      switch (data.status) {
        case 'synced':
          badgeEl.classList.add('badge-success');
          badgeEl.textContent = '已同步';
          badgeEl.title = 'IPv6 DDNS 记录已成功同步';
          break;
        case 'probing':
          badgeEl.classList.add('badge-info');
          badgeEl.textContent = '探测中';
          badgeEl.title = '正在探测网卡 IPv6 地址';
          break;
        case 'error':
          badgeEl.classList.add('badge-danger');
          badgeEl.textContent = '同步异常';
          badgeEl.title = data.last_error || '同步发生错误';
          break;
        default:
          badgeEl.classList.add('badge-secondary');
          badgeEl.textContent = '空闲';
          badgeEl.title = '';
          break;
      }
    }
  }
}

export async function loadServerNetworkSettings() {
  const switchEl = document.getElementById('serverNetworkEnabledSwitch');
  if (!switchEl) return;

  try {
    const res = await apiFetch(`${state.serverHost}/api/v1/server/network`);
    if (res.status === 403) {
      const saveBtn = document.getElementById('serverNetworkSaveBtn');
      if (saveBtn) saveBtn.style.display = 'none';
      return;
    }
    if (!res.ok) {
      return;
    }
    const data = await res.json();
    renderServerNetworkStatus(data);
  } catch (err) {
    // 忽略加载异常
  }
}

export async function saveServerNetworkSettings() {
  const switchEl = document.getElementById('serverNetworkEnabledSwitch');
  const ifaceInput = document.getElementById('serverNetworkInterfaceInput');
  const recordsInput = document.getElementById('serverNetworkRecordsInput');
  const saveBtn = document.getElementById('serverNetworkSaveBtn');

  if (!switchEl) return;

  const enabled = switchEl.checked;
  const iface = ifaceInput ? ifaceInput.value.trim() : '';
  const rawRecords = recordsInput ? recordsInput.value.trim() : '';
  const records = rawRecords ? rawRecords.split(',').map(s => s.trim()).filter(Boolean) : [];

  if (enabled) {
    if (!iface) {
      showToast('请输入物理网络接口名称（如 en0 或 eth0）', 'error');
      if (ifaceInput) ifaceInput.focus();
      return;
    }
    if (records.length === 0) {
      showToast('请输入至少一个受管域名', 'error');
      if (recordsInput) recordsInput.focus();
      return;
    }
  }

  const payload = {
    enabled,
    interface: iface,
    records,
  };

  if (saveBtn) {
    saveBtn.disabled = true;
    saveBtn.textContent = '保存中...';
  }

  try {
    const res = await apiFetch(`${state.serverHost}/api/v1/server/network`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });

    const data = await res.json();
    if (!res.ok) {
      showToast(data.error || '保存自举网络设置失败', 'error');
      return;
    }

    renderServerNetworkStatus(data);
    showToast('自举网络设置已更新', 'success');
    addLog('success', enabled ? `已启用服务端 IPv6 自举 (${iface})` : '已停用服务端 IPv6 自举');
  } catch (err) {
    showToast(`保存失败: ${err.message}`, 'error');
  } finally {
    if (saveBtn) {
      saveBtn.disabled = false;
      saveBtn.textContent = '保存自举配置';
    }
  }
}

export function saveSettingsForm(onSettingsChanged) {
  const settingsServerUrlInput = document.getElementById('settingsServerUrlInput');
  const newServer = settingsServerUrlInput ? settingsServerUrlInput.value.trim() : '';

  if (newServer) {
    const cleanServer = sanitizeHost(newServer);
    if (cleanServer) {
      localStorage.setItem('homeagent_server_url', cleanServer);
    } else {
      localStorage.removeItem('homeagent_server_url');
    }
    state.serverHost = cleanServer;
  } else {
    localStorage.removeItem('homeagent_server_url');
    state.serverHost = '';
  }

  updateInstallCommand();
  showToast('服务端地址设置已保存');
  addLog('success', '已更新服务端通信参数配置');
  if (onSettingsChanged) {
    onSettingsChanged();
  }
}

export function clearSettingsToken(onSettingsChanged) {
  const settingsServerUrlInput = document.getElementById('settingsServerUrlInput');
  if (settingsServerUrlInput) settingsServerUrlInput.value = '';
  localStorage.removeItem('homeagent_server_url');
  state.serverHost = '';
  updateInstallCommand();
  showToast('已重置服务端地址设置');
  addLog('info', '已重置服务端通信参数');
  if (onSettingsChanged) {
    onSettingsChanged();
  }
}
