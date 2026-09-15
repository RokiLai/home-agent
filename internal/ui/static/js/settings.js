import { state, sanitizeHost } from './state.js';
import { showToast, addLog } from './utils.js';
import { updateInstallCommand } from './onboarding.js';
import { apiFetch } from './api.js';
import { fetchUsersList } from './users.js';
import { fetchGitHubStatus } from './github.js';

export function switchSettingsSection(sectionName) {
  const validSections = ['general', 'version', 'network', 'users', 'github', 'about'];
  const isAll = !sectionName || sectionName === 'all';
  let section = isAll ? 'all' : (validSections.includes(sectionName) ? sectionName : 'general');

  // RBAC fallback protection: non-owner cannot switch to users section
  if (section === 'users' && state.currentUser && state.currentUser.role !== 'owner') {
    section = 'general';
  }
  state.currentSettingsSection = section;

  // Toggle section visibility
  const sections = document.querySelectorAll('.settings-section');
  sections.forEach(sec => {
    const secSection = (sec.dataset && sec.dataset.section) || (sec.id ? sec.id.replace('settingsSec', '').toLowerCase() : '');
    if (section === 'all') {
      if (secSection === 'users' && state.currentUser && state.currentUser.role !== 'owner') {
        sec.classList.remove('active');
      } else {
        sec.classList.add('active');
      }
    } else {
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
    if (section === 'all') {
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
  if (section === 'version' || section === 'about' || section === 'all') {
    loadVersionStatus();
  }
  if (section === 'network' || section === 'all') {
    loadServerNetworkSettings();
  }
  if ((section === 'users' || section === 'all') && state.currentUser && state.currentUser.role === 'owner') {
    fetchUsersList();
  }
  if (section === 'github' || section === 'all') {
    fetchGitHubStatus();
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
  setText('aboutServerVersion', server.current_version || '未知');
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

function updateServerIPv6EndpointURL() {
  const input = document.getElementById('serverIPv6EndpointInput');
  if (!input) return;
  let base = state.serverHost || (typeof window !== 'undefined' && window.location ? window.location.origin : '');
  if (!base) base = 'http://127.0.0.1:8080';
  input.value = `${base.replace(/\/+$/, '')}/api/v1/server/ipv6`;
}

function initServerNetworkEvents() {
  const switchEl = document.getElementById('serverNetworkEnabledSwitch');
  const saveBtn = document.getElementById('serverNetworkSaveBtn');
  const redetectBtn = document.getElementById('serverNetworkRedetectBtn');
  const copyBtn = document.getElementById('copyServerIPv6EndpointBtn');
  const endpointInput = document.getElementById('serverIPv6EndpointInput');

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
  if (redetectBtn) {
    redetectBtn.addEventListener('click', async () => {
      redetectBtn.disabled = true;
      redetectBtn.textContent = '探测中...';
      try {
        await detectServerNetwork();
        await detectServerNetwork(true);
      } finally {
        redetectBtn.disabled = false;
        redetectBtn.textContent = '重新探测';
      }
    });
  }

  if (copyBtn) {
    copyBtn.addEventListener('click', async () => {
      updateServerIPv6EndpointURL();
      const url = endpointInput ? endpointInput.value : '';
      if (!url) return;
      try {
        if (navigator.clipboard && navigator.clipboard.writeText) {
          await navigator.clipboard.writeText(url);
        } else if (endpointInput) {
          endpointInput.select();
          document.execCommand('copy');
        }
        showToast('已复制接口地址', 'success');
      } catch (err) {
        showToast('复制失败，请手动复制', 'error');
      }
    });
  }

  updateServerIPv6EndpointURL();
}

let serverNetworkDetectionId = '';
let serverNetworkConfigVersion = 0;

function updateServerNetworkFieldState(enabled) {
  const fields = document.getElementById('serverNetworkFields');
  const recordsInput = document.getElementById('serverNetworkRecordsInput');
  const writerConfirmed = document.getElementById('serverNetworkExternalWriterConfirmed');

  if (fields) {
    fields.style.opacity = enabled ? '1' : '0.5';
  }
  if (recordsInput) {
    recordsInput.disabled = !enabled;
  }
  if (writerConfirmed) {
    writerConfirmed.disabled = !enabled;
  }
}

function renderServerNetworkStatus(data) {
  const switchEl = document.getElementById('serverNetworkEnabledSwitch');
  const recordsInput = document.getElementById('serverNetworkRecordsInput');
  const badgeEl = document.getElementById('serverNetworkStatusBadge');
  const currentIpEl = document.getElementById('serverNetworkCurrentIp');
  const lastSyncEl = document.getElementById('serverNetworkLastSyncTime');

  if (switchEl) {
    switchEl.checked = !!data.enabled;
    updateServerNetworkFieldState(!!data.enabled);
  }
  serverNetworkConfigVersion = Number(data.config_version || serverNetworkConfigVersion || 1);
  if (recordsInput) {
    recordsInput.value = (data.records || []).join(', ');
  }
  if (currentIpEl) {
    currentIpEl.textContent = data.resolved_address || data.current_address || data.current_ip || '未探测';
  }
  if (lastSyncEl) {
    lastSyncEl.textContent = data.last_sync ? new Date(data.last_sync).toLocaleString() : '-';
  }

  if (badgeEl) {
    badgeEl.className = 'badge';
    if (data.status === 'standalone_detector' || data.status === 'synced') {
      badgeEl.classList.add('badge-success');
      badgeEl.textContent = '探测正常';
      badgeEl.title = '服务端 IPv6 探测正常';
    } else if (data.status === 'no_address') {
      badgeEl.classList.add('badge-warning');
      badgeEl.textContent = '无有效 IPv6';
      badgeEl.title = '当前物理网卡未分配有效公网 IPv6';
    } else if (data.status === 'error') {
      badgeEl.classList.add('badge-danger');
      badgeEl.textContent = '探测失败';
      badgeEl.title = data.last_error || '网络探测异常';
    } else {
      badgeEl.classList.add('badge-secondary');
      badgeEl.textContent = '就绪';
      badgeEl.title = '';
    }
  }
}

async function detectServerNetwork(interactive = false) {
  const ifaceEl = document.getElementById('serverNetworkResolvedInterface');
  const addressEl = document.getElementById('serverNetworkResolvedAddress');
  const messageEl = document.getElementById('serverNetworkDetectionMessage');
  const badgeEl = document.getElementById('serverNetworkStatusBadge');

  if (ifaceEl) ifaceEl.textContent = '探测中';
  if (addressEl) addressEl.textContent = '探测中';
  if (badgeEl) {
    badgeEl.className = 'badge badge-info';
    badgeEl.textContent = '探测中';
  }

  try {
    const res = await apiFetch(`${state.serverHost}/api/v1/server/network/candidates`);
    const data = await res.json();
    if (!res.ok || data.status !== 'ready') {
      serverNetworkDetectionId = '';
      if (ifaceEl) ifaceEl.textContent = data.resolved_interface || '-';
      if (addressEl) addressEl.textContent = '未解析';
      const errMsg = (data.errors || ['自动解析失败']).join('；');
      if (messageEl) messageEl.textContent = errMsg;
      if (badgeEl) {
        badgeEl.className = data.status === 'no_address' ? 'badge badge-warning' : 'badge badge-danger';
        badgeEl.textContent = data.status === 'no_address' ? '无有效 IPv6' : '探测失败';
      }
      if (interactive) {
        showToast(errMsg, 'error');
      }
      return false;
    }

    serverNetworkDetectionId = data.detection_id;
    if (ifaceEl) ifaceEl.textContent = data.resolved_interface || '-';
    if (addressEl) addressEl.textContent = data.resolved_address || '未解析';
    if (messageEl) messageEl.textContent = '已按当前默认 IPv6 路由和稳定地址筛选规则自动确定。';
    if (badgeEl) {
      badgeEl.className = 'badge badge-success';
      badgeEl.textContent = '探测正常';
    }
    if (interactive) {
      showToast('网络探测完成', 'success');
    }
    return true;
  } catch (err) {
    serverNetworkDetectionId = '';
    if (ifaceEl) ifaceEl.textContent = '-';
    if (addressEl) addressEl.textContent = '未解析';
    const errMsg = `探测异常: ${err.message}`;
    if (messageEl) messageEl.textContent = errMsg;
    if (badgeEl) {
      badgeEl.className = 'badge badge-danger';
      badgeEl.textContent = '探测异常';
    }
    if (interactive) {
      showToast(errMsg, 'error');
    }
    return false;
  }
}

export async function loadServerNetworkSettings() {
  updateServerIPv6EndpointURL();
  const badgeEl = document.getElementById('serverNetworkStatusBadge');
  if (!badgeEl && !document.getElementById('serverNetworkResolvedInterface')) return;

  try {
    const res = await apiFetch(`${state.serverHost}/api/v1/server/network`);
    if (res.status === 403) {
      return;
    }
    if (!res.ok) {
      return;
    }
    const data = await res.json();
    renderServerNetworkStatus(data);
    await detectServerNetwork(false);
  } catch (err) {
    // 忽略加载异常
  }
}

export async function saveServerNetworkSettings() {
  const switchEl = document.getElementById('serverNetworkEnabledSwitch');
  const recordsInput = document.getElementById('serverNetworkRecordsInput');
  const writerConfirmed = document.getElementById('serverNetworkExternalWriterConfirmed');
  const saveBtn = document.getElementById('serverNetworkSaveBtn');

  if (!switchEl) return;

  const enabled = switchEl.checked;
  const rawRecords = recordsInput ? recordsInput.value.trim() : '';
  const records = rawRecords ? rawRecords.split(',').map(s => s.trim()).filter(Boolean) : [];

  if (enabled) {
    if (records.length === 0) {
      showToast('请输入至少一个受管域名', 'error');
      if (recordsInput) recordsInput.focus();
      return;
    }
    if (!writerConfirmed || !writerConfirmed.checked) {
      showToast('请确认域名所有权并停止其他 DDNS 发布器', 'error');
      return;
    }
  }

  const payload = { enabled, records, config_version: serverNetworkConfigVersion };

  if (saveBtn) {
    saveBtn.disabled = true;
    saveBtn.textContent = '保存中...';
  }

  try {
    if (enabled) {
      if (!serverNetworkDetectionId && !(await detectServerNetwork())) {
        showToast('当前无法自动解析有效 IPv6 地址', 'error');
        return;
      }
      const validateRes = await apiFetch(`${state.serverHost}/api/v1/server/network/validate`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ detection_id: serverNetworkDetectionId, records }),
      });
      const validation = await validateRes.json();
      if (!validateRes.ok || validation.validation_status !== 'valid') {
        serverNetworkDetectionId = '';
        showToast(validation.error || validation.error_code || '配置预检失败', 'error');
        return;
      }
      payload.validation_token = validation.validation_token;
      payload.external_writer_confirmed = true;
    }
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
    addLog('success', enabled ? '已启用服务端 IPv6 自动解析与 DDNS 自举' : '已停用服务端 IPv6 自举');
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
