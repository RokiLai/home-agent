import { state, sanitizeHost } from './state.js';
import { showToast, addLog } from './utils.js';
import { updateInstallCommand } from './onboarding.js';
import { apiFetch } from './api.js';

export function switchSettingsSection(sectionName) {
  const validSections = ['general', 'version', 'network', 'users', 'github', 'about'];
  const isAll = !sectionName || sectionName === 'all';
  const section = isAll ? 'all' : (validSections.includes(sectionName) ? sectionName : 'general');
  state.currentSettingsSection = section;

  // Toggle section visibility
  const sections = document.querySelectorAll('.settings-section');
  sections.forEach(sec => {
    if (isAll) {
      sec.classList.add('active');
    } else {
      const secSection = (sec.dataset && sec.dataset.section) || (sec.id ? sec.id.replace('settingsSec', '').toLowerCase() : '');
      if (secSection === section) {
        sec.classList.add('active');
      } else {
        sec.classList.remove('active');
      }
    }
  });

  // Toggle tabs
  const tabs = document.querySelectorAll('.settings-tab');
  tabs.forEach(tab => {
    const tabSection = (tab.dataset && tab.dataset.section) || '';
    if (isAll) {
      if (tabSection === 'all') {
        tab.classList.add('active');
      } else {
        tab.classList.remove('active');
      }
    } else {
      if (tabSection === section) {
        tab.classList.add('active');
      } else {
        tab.classList.remove('active');
      }
    }
  });

  // On demand data loading
  if (section === 'version' || isAll) {
    loadVersionStatus();
  }
  if (section === 'network' || isAll) {
    loadServerNetworkSettings();
  }
}

export function initSettingsForm() {
  const settingsServerUrlInput = document.getElementById('settingsServerUrlInput');
  if (settingsServerUrlInput) {
    settingsServerUrlInput.value = localStorage.getItem('homeagent_server_url') || '';
  }

  initServerNetworkEvents();
  loadServerNetworkSettings();
  initVersionStatusEvents();
  loadVersionStatus();

  window.addEventListener('hashchange', () => {
    if (window.location.hash.startsWith('#/settings')) {
      const parts = window.location.hash.replace(/^#\/?/, '').split('/');
      switchSettingsSection(parts[1] || 'all');
    }
  });
}

function versionStateText(channel) {
  if (!channel || channel.status === 'unknown') return '尚无可信结果';
  if (channel.status === 'checking') return '检查中';
  if (channel.status === 'stale') return `结果已过期${channel.error_message ? `：${channel.error_message}` : ''}`;
  if (channel.status === 'error') return `检查失败${channel.error_message ? `：${channel.error_message}` : ''}`;
  if (channel.update_state === 'update_available') return '发现新版本';
  if (channel.update_state === 'current') return '已是最新';
  return 'Release 可用';
}

function checkedAtText(value) {
  return value ? new Date(value).toLocaleString() : '-';
}

export function renderVersionStatus(data) {
  const server = data?.server || {};
  const agent = data?.agent || {};
  const setText = (id, value) => {
    const el = document.getElementById(id);
    if (el) el.textContent = value;
  };
  setText('serverCurrentVersion', server.current_version || '未知');
  setText('serverLatestVersion', server.latest_version || '未知');
  setText('serverVersionState', versionStateText(server));
  setText('serverVersionCheckedAt', checkedAtText(server.checked_at));
  setText('agentLatestVersion', agent.latest_version || '未知');
  setText('agentVersionState', versionStateText(agent));
  setText('agentVersionCheckedAt', checkedAtText(agent.checked_at));
  const summary = agent.device_summary || {};
  setText('agentUpdateSummary', `可升级 ${summary.update_available || 0}，离线 ${summary.offline || 0}，制品不可用 ${summary.artifact_unavailable || 0}`);
  setText('agentBatchVersionSummary', agent.latest_version ? `Agent ${agent.latest_version} · 可升级 ${summary.update_available || 0}` : 'Agent 版本不可用');

  const upgradeBtn = document.getElementById('serverUpgradeBtn');
  if (upgradeBtn) {
    upgradeBtn.disabled = server.update_state !== 'update_available' || server.status !== 'available' || server.upgrade_supported !== true;
    upgradeBtn.title = server.upgrade_supported === true ? '' : '尚未安装独立恢复监督器，服务端自升级保持禁用';
  }
  const releaseLink = document.getElementById('serverReleaseLink');
  if (releaseLink) {
    releaseLink.hidden = !server.release_url;
    releaseLink.href = server.release_url || '#';
  }
}

export async function loadVersionStatus(force = false) {
  const refreshBtn = document.getElementById('versionRefreshBtn');
  if (!refreshBtn) return;
  refreshBtn.disabled = true;
  refreshBtn.textContent = '检查中...';
  try {
    const suffix = force ? '?refresh=true' : '';
    const res = await apiFetch(`${state.serverHost}/api/v2/system/version-status${suffix}`);
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    renderVersionStatus(await res.json());
  } catch (err) {
    renderVersionStatus({ server: { status: 'error', error_message: err.message }, agent: { status: 'error', error_message: err.message } });
  } finally {
    refreshBtn.disabled = false;
    refreshBtn.textContent = '检查更新';
  }
}

export async function monitorServerUpgradeOperation(operationId, options = {}) {
  const fetchOperation = options.fetchOperation || (async () => {
    const res = await apiFetch(`${state.serverHost}/api/v2/system/server-upgrades/${encodeURIComponent(operationId)}`);
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    return res.json();
  });
  const sleep = options.sleep || (ms => new Promise(resolve => setTimeout(resolve, ms)));
  const timeoutMs = options.timeoutMs || 90000;
  const startedAt = Date.now();
  let lastError;
  while (Date.now() - startedAt < timeoutMs) {
    try {
      const operation = await fetchOperation();
      if (['succeeded', 'failed', 'rolled_back'].includes(operation.status)) return operation;
    } catch (err) {
      lastError = err;
    }
    await sleep(1000);
  }
  throw lastError || new Error('服务端升级状态查询超时');
}

function initVersionStatusEvents() {
  const refreshBtn = document.getElementById('versionRefreshBtn');
  if (refreshBtn) refreshBtn.addEventListener('click', () => loadVersionStatus(true));
  const upgradeBtn = document.getElementById('serverUpgradeBtn');
  if (upgradeBtn) {
    upgradeBtn.addEventListener('click', async () => {
      if (!window.confirm('确认下载并替换 Server 二进制？服务将重启。')) return;
      if (!window.confirm('再次确认：重启期间控制台会暂时断开。')) return;
      upgradeBtn.disabled = true;
      try {
        const res = await apiFetch(`${state.serverHost}/api/v2/system/server-upgrades`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' });
        if (!res.ok) throw new Error(await res.text() || `HTTP ${res.status}`);
        const started = await res.json();
        showToast('服务端升级已启动，正在等待重启验证');
        const result = await monitorServerUpgradeOperation(started.operation_id);
        if (result.status !== 'succeeded') throw new Error(result.error_message || result.status);
        showToast(`服务端已升级至 ${result.target_version}`);
        await loadVersionStatus(true);
      } catch (err) {
        showToast(`服务端升级失败：${err.message}`, 'error');
        upgradeBtn.disabled = false;
      }
    });
  }
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
