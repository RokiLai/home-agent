import { state } from '../state.js';
import { apiFetch } from '../api.js';
import { showToast, copyToClipboard, addLog, filterAndClassifyIPs, escapeHTML } from '../utils.js';
import { updateStats, renderDashboardSummary, renderDevices } from './render.js';
import { renderGitHubDeviceMatrix, handleToggleGitHubSync, fetchGitHubStatus } from '../github.js';
import { openRenameModal, openIpModal, closeAllDropdowns, requestOpenModal, requestCloseModal } from '../modals.js';
import { createIdempotencyKey } from '../idempotency.mjs';

function commandHeaders(idempotencyKey = createIdempotencyKey()) {
  return { 'Content-Type': 'application/json', 'Idempotency-Key': idempotencyKey };
}

const terminalFailureStatuses = new Set(['failed', 'timed_out', 'canceled', 'interrupted']);

export function summarizeUpgradeAllResponse(data) {
  const results = Array.isArray(data.device_results) ? data.device_results : [];
  const dispatched = Number(data.dispatched_count ?? results.filter(r => r.status === 'dispatched').length);
  const skipped = Number(data.skipped_count ?? results.filter(r => r.status === 'skipped').length);
  const failed = Number(data.failed_count ?? results.filter(r => r.status === 'failed').length);
  return { dispatched, skipped, failed, results };
}

async function fetchUpgradeJSON(url) {
  const res = await apiFetch(url);
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return res.json();
}

export async function monitorUpgradeResults(results, targetVersion, options = {}) {
  const fetchJSON = options.fetchJSON || fetchUpgradeJSON;
  const refreshDevices = options.refreshDevices || fetchDevices;
  const sleep = options.sleep || (ms => new Promise(resolve => setTimeout(resolve, ms)));
  const timeoutMs = options.timeoutMs || 15 * 60 * 1000;
  const deadline = Date.now() + timeoutMs;
  const pending = new Map(results.filter(r => r.status === 'dispatched').map(r => [r.device_id, r]));
  const outcomes = [];

  while (pending.size > 0 && Date.now() < deadline) {
    await refreshDevices();
    for (const [deviceID, result] of pending) {
      let commandStatus = '';
      let commandError = '';
      if (result.command_id) {
        try {
          const command = await fetchJSON(`${state.serverHost}/api/v1/commands/${encodeURIComponent(result.command_id)}`);
          commandStatus = command.status || '';
          commandError = command.error_message || '';
        } catch (err) {
          commandError = err.message;
        }
      }

      if (terminalFailureStatuses.has(commandStatus)) {
        state.upgradingDevices.delete(deviceID);
        pending.delete(deviceID);
        outcomes.push({ device_id: deviceID, status: commandStatus, error: commandError });
        addLog('warn', `设备 [${deviceID}] 升级${commandStatus}: ${commandError || '未提供错误详情'}`);
        continue;
      }

      const device = state.devices.find(d => d.id === deviceID);
      if ((commandStatus === 'succeeded' || !result.command_id) && device && device.agent_version === targetVersion) {
        state.upgradingDevices.delete(deviceID);
        pending.delete(deviceID);
        outcomes.push({ device_id: deviceID, status: 'converged' });
        addLog('success', `设备 [${deviceID}] 已重启并收敛至 ${targetVersion}`);
      }
    }
    renderDevices();
    if (pending.size > 0) await sleep(2000);
  }

  for (const deviceID of pending.keys()) {
    state.upgradingDevices.delete(deviceID);
    outcomes.push({ device_id: deviceID, status: 'not_converged' });
    addLog('warn', `设备 [${deviceID}] 升级未在规定时间内收敛至 ${targetVersion}`);
  }
  renderDevices();
  return outcomes;
}

export async function fetchDevices() {
  if (state.isFetching) return;
  state.isFetching = true;

  const liveStatusText = document.getElementById('liveStatusText');

  try {
    const res = await apiFetch(`${state.serverHost}/api/v1/devices`);
    if (!res.ok) {
      if (liveStatusText) liveStatusText.innerText = `异常 (${res.status})`;
      return;
    }

    if (liveStatusText) liveStatusText.innerText = '实时连接中';
    const data = await res.json();
    state.devices = data.devices || [];
    state.serverHash = data.server_hash || '';
  } catch (err) {
    if (err.message !== 'Unauthorized') {
      if (liveStatusText) liveStatusText.innerText = '无法连接服务';
      console.error('Fetch devices network error:', err);
    }
    return;
  } finally {
    state.isFetching = false;
  }

  try {
    updateStats();
    renderDashboardSummary();
    renderDevices();
    renderGitHubDeviceMatrix((id, checked) => handleToggleGitHubSync(id, checked, () => {
      fetchDevices();
      fetchGitHubStatus();
    }));
  } catch (err) {
    console.error('Render devices error:', err);
  }
}

export async function handleSyncAll() {
  const btnSyncAll = document.getElementById('btnSyncAll');
  const syncAllIcon = document.getElementById('syncAllIcon');
  const syncAllBtnText = document.getElementById('syncAllBtnText');

  if (btnSyncAll) btnSyncAll.disabled = true;
  if (syncAllIcon) syncAllIcon.classList.add('spinning');
  if (syncAllBtnText) syncAllBtnText.innerText = '正在同步...';
  addLog('info', '开始触发全网设备配置下发与公钥同步...');

  try {
    const res = await apiFetch(`${state.serverHost}/api/v1/sync`, {
      method: 'POST',
	  headers: commandHeaders()
    });

    if (!res.ok) {
      throw new Error(`HTTP ${res.status}`);
    }

    const data = await res.json();
    showToast(`全网同步触发成功 (版本: ${data.version || '最新'})`);
    addLog('success', `全网同步成功下发至各设备，最新版本号: ${data.version}`);
    await fetchDevices();
  } catch (err) {
    console.error('Failed to trigger sync all:', err);
    showToast(`全网同步失败: ${err.message}`);
    addLog('warn', `全网同步失败: ${err.message}`);
  } finally {
    if (btnSyncAll) btnSyncAll.disabled = false;
    if (syncAllIcon) syncAllIcon.classList.remove('spinning');
    if (syncAllBtnText) syncAllBtnText.innerText = '全网同步';
  }
}

export async function handleUpgradeAll() {
  const btnUpgradeAll = document.getElementById('btnUpgradeAll');
  const upgradeAllIcon = document.getElementById('upgradeAllIcon');
  const upgradeAllBtnText = document.getElementById('upgradeAllBtnText');

  if (btnUpgradeAll) btnUpgradeAll.disabled = true;
  if (upgradeAllIcon) upgradeAllIcon.classList.add('spinning');
  if (upgradeAllBtnText) upgradeAllBtnText.innerText = '下发中...';
  addLog('info', '正在向全网所有已连接设备下发自升级指令...');

  try {
    const res = await apiFetch(`${state.serverHost}/api/v1/devices/upgrade-all`, {
      method: 'POST',
	  headers: commandHeaders()
    });

    if (!res.ok) {
      throw new Error(`HTTP ${res.status}`);
    }

    const data = await res.json();
    const summary = summarizeUpgradeAllResponse(data);
    showToast(`升级分发：成功 ${summary.dispatched}，跳过 ${summary.skipped}，失败 ${summary.failed}`);
    addLog(summary.failed > 0 ? 'warn' : 'success', `全网升级分发完成：成功 ${summary.dispatched}，跳过 ${summary.skipped}，失败 ${summary.failed}`);
    summary.results.filter(result => result.status !== 'dispatched').forEach(result => {
      addLog(result.status === 'failed' ? 'warn' : 'info', `设备 [${result.device_id}] ${result.status}: ${result.reason || result.message || '-'}`);
    });

    summary.results.filter(result => result.status === 'dispatched').forEach(result => state.upgradingDevices.add(result.device_id));
    renderDevices();
    void monitorUpgradeResults(summary.results, data.target_version || '');
  } catch (err) {
    console.error('Failed to trigger upgrade all:', err);
    showToast(`全网升级失败: ${err.message}`);
    addLog('warn', `全网升级失败: ${err.message}`);
  } finally {
    if (btnUpgradeAll) btnUpgradeAll.disabled = false;
    if (upgradeAllIcon) upgradeAllIcon.classList.remove('spinning');
    if (upgradeAllBtnText) upgradeAllBtnText.innerText = '全网升级';
  }
}

export async function shutdownDevice(button) {
  const devID = button.dataset.id;
  const hostname = button.dataset.hostname || devID;
  const alias = button.dataset.alias;
  const displayName = alias ? `${alias} (${hostname})` : hostname;

  if (!confirm(`确定要远程关机设备 "${displayName}" 吗？该操作将向目标主机下发关机指令。`)) {
    return;
  }

  button.disabled = true;
  state.shuttingDownDevices.add(devID);
  button.classList.add('is-shutting-down');
  addLog('warn', `正在向设备 [${displayName}] 发送关机指令...`);

  try {
    const res = await apiFetch(`${state.serverHost}/api/v1/devices/${encodeURIComponent(devID)}/shutdown`, {
      method: 'POST',
	  headers: commandHeaders(),
      body: JSON.stringify({ reason: 'web_ui_request', delay_seconds: 1 })
    });

    if (!res.ok) {
      const errText = await res.text();
      throw new Error(errText || `HTTP ${res.status}`);
    }

    showToast(`已向 ${displayName} 下发关机指令`);
    addLog('warn', `远程关机指令已成功派发至设备 [${displayName}]`);
  } catch (err) {
    console.error('Failed to shutdown device:', err);
    showToast(`关机请求失败: ${err.message}`);
    addLog('error', `关机设备 [${displayName}] 失败: ${err.message}`);
  } finally {
    setTimeout(() => {
      state.shuttingDownDevices.delete(devID);
      renderDevices();
    }, 5000);
  }
}

export async function wakeDevice(button) {
  const devID = button.dataset.id;
  const hostname = button.dataset.hostname || devID;
  const mac = button.dataset.mac;

  if (!mac) {
    showToast(`设备 ${hostname} 尚未上报物理 MAC 地址，无法发送唤醒包`);
    addLog('warn', `唤醒失败: 设备 ${hostname} 缺少 MAC 地址`);
    return;
  }

  button.disabled = true;
  state.wakingDevices.add(devID);
  button.classList.add('is-waking');
  const span = button.querySelector('span:last-child');
  if (span) span.innerText = '唤醒中';
  addLog('info', `向设备 [${hostname}] (${mac}) 发送 Wake on LAN 唤醒包...`);

  try {
    const res = await apiFetch(`${state.serverHost}/api/v1/devices/${encodeURIComponent(devID)}/wake`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' }
    });

    if (!res.ok) {
      const errText = await res.text();
      throw new Error(errText || `HTTP ${res.status}`);
    }

    showToast(`已向 ${hostname} 发送 WOL 魔术包`);
    addLog('success', `WOL 唤醒包已成功广播至 [${hostname}] (${mac})`);
  } catch (err) {
    console.error('Failed to wake device:', err);
    showToast(`唤醒请求失败: ${err.message}`);
    addLog('warn', `唤醒设备 [${hostname}] 失败: ${err.message}`);
  } finally {
    setTimeout(() => {
      state.wakingDevices.delete(devID);
      renderDevices();
    }, 4000);
  }
}

export async function upgradeDevice(button) {
  const devID = button.dataset.id;
  const hostname = button.dataset.hostname || devID;

  button.disabled = true;
  state.upgradingDevices.add(devID);
  addLog('info', `向设备 [${hostname}] 发送通道内自升级指令...`);

  try {
    const res = await apiFetch(`${state.serverHost}/api/v1/devices/${encodeURIComponent(devID)}/upgrade`, {
      method: 'POST',
	  headers: commandHeaders()
    });

    if (!res.ok) {
      const errText = await res.text();
      throw new Error(errText || `HTTP ${res.status}`);
    }

    showToast(`已向 ${hostname} 下发自升级指令`);
    addLog('success', `升级指令已成功推送到设备 [${hostname}]`);
  } catch (err) {
    console.error('Failed to upgrade device:', err);
    showToast(`升级指令下发失败: ${err.message}`);
    addLog('warn', `升级设备 [${hostname}] 失败: ${err.message}`);
  } finally {
    setTimeout(() => {
      state.upgradingDevices.delete(devID);
      renderDevices();
    }, 5000);
  }
}

export async function syncDevice(button) {
  const id = button.dataset.id;
  const hostname = button.dataset.hostname || id;

  button.disabled = true;
  addLog('info', `向设备 [${hostname}] 触发密钥同步...`);

  try {
    const res = await apiFetch(`${state.serverHost}/api/v1/devices/${encodeURIComponent(id)}/sync`, {
      method: 'POST',
	  headers: commandHeaders()
    });

    if (!res.ok) {
      throw new Error(`HTTP ${res.status}`);
    }

    showToast(`设备 ${hostname} 密钥同步已触发`);
    addLog('success', `设备 [${hostname}] 同步指令下发成功`);
    await fetchDevices();
  } catch (err) {
    console.error('Failed to sync device:', err);
    showToast(`设备同步失败: ${err.message}`);
    addLog('warn', `设备 [${hostname}] 同步失败: ${err.message}`);
  } finally {
    button.disabled = false;
  }
}

export async function deleteDevice(id, hostname) {
  const name = hostname || id;
  if (!confirm(`确定要从控制平面中移除设备 "${name}" 吗？`)) {
    return;
  }

  addLog('info', `正在移除设备 [${name}]...`);
  try {
    const res = await apiFetch(`${state.serverHost}/api/v1/devices/${encodeURIComponent(id)}`, {
      method: 'DELETE'
    });

    if (!res.ok) {
      throw new Error(`HTTP ${res.status}`);
    }

    showToast(`设备 "${name}" 已成功移除`);
    addLog('success', `设备 [${name}] 已从注册表中移除`);
    await fetchDevices();
  } catch (err) {
    console.error('Failed to delete device:', err);
    showToast(`移除设备失败: ${err.message}`);
    addLog('warn', `移除设备 [${name}] 失败: ${err.message}`);
  }
}

export function attachDeviceCardEvents() {
  // Rename buttons
  document.querySelectorAll('.btn-rename-device').forEach(btn => {
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      openRenameModal(btn.dataset.id, btn.dataset.hostname, btn.dataset.alias, btn.dataset.mac);
    });
  });

  // Copy IP chips
  document.querySelectorAll('.btn-copy-ip').forEach(chip => {
    chip.addEventListener('click', (e) => {
      e.stopPropagation();
      copyToClipboard(chip.dataset.ip, 'IP 已复制');
    });
  });

  // View all IPs
  document.querySelectorAll('.btn-view-ips').forEach(btn => {
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      const d = state.devices.find(x => x.id === btn.dataset.id);
      if (!d) return;
      const { ipv4, ipv6 } = filterAndClassifyIPs(d.addresses);
      openIpModal(d.hostname || d.id, btn.dataset.type, btn.dataset.type === 'IPv4' ? ipv4 : ipv6);
    });
  });

  // SSH Copy Box
  document.querySelectorAll('.btn-ssh-box').forEach(btn => {
    btn.addEventListener('click', () => {
      const sshCmd = btn.dataset.ssh;
      copyToClipboard(sshCmd, 'SSH 命令已复制');
      btn.classList.add('copied');
      const textSpan = btn.querySelector('.ssh-btn-text');
      if (textSpan) textSpan.innerText = '已复制!';
      setTimeout(() => {
        btn.classList.remove('copied');
        if (textSpan) textSpan.innerText = '复制';
      }, 1500);
    });
  });

  // WOL Wake
  document.querySelectorAll('.btn-wake-device').forEach(btn => {
    btn.addEventListener('click', () => {
      wakeDevice(btn);
    });
  });

  // More Actions toggle
  document.querySelectorAll('.btn-more-actions').forEach(btn => {
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      const devID = btn.dataset.id;
      const menu = document.getElementById(`dropdown-${devID}`);
      if (!menu) return;
      const isOpen = menu.classList.contains('is-open');
      closeAllDropdowns();
      if (!isOpen) {
        menu.classList.add('is-open');
        state.openDropdownDevID = devID;
        btn.setAttribute('aria-expanded', 'true');
      }
    });
  });

  // Menu Sync single device
  document.querySelectorAll('.btn-menu-sync').forEach(btn => {
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      closeAllDropdowns();
      syncDevice(btn);
    });
  });

  // Menu Upgrade single device
  document.querySelectorAll('.btn-menu-upgrade').forEach(btn => {
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      closeAllDropdowns();
      upgradeDevice(btn);
    });
  });

  // Menu Remote shutdown single device
  document.querySelectorAll('.btn-menu-shutdown').forEach(btn => {
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      closeAllDropdowns();
      shutdownDevice(btn);
    });
  });

  // Menu Delete single device
  document.querySelectorAll('.btn-menu-del').forEach(btn => {
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      closeAllDropdowns();
      deleteDevice(btn.dataset.id, btn.dataset.hostname);
    });
  });

  // View Health Modal
  document.querySelectorAll('.btn-view-health').forEach(btn => {
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      openHealthModal(btn.dataset.id, btn);
    });
  });

  // Modal close buttons
  const closeHealthBtn = document.getElementById('closeHealthModalBtn');
  const closeHealthFooterBtn = document.getElementById('closeHealthModalBtnFooter');
  if (closeHealthBtn) closeHealthBtn.onclick = () => closeHealthModal();
  if (closeHealthFooterBtn) closeHealthFooterBtn.onclick = () => closeHealthModal();
}

export function closeHealthModal() {
  requestCloseModal('deviceHealthModal');
}

export async function openHealthModal(deviceID, triggerEl) {
  const d = state.devices.find(dev => dev.id === deviceID);
  if (!d) return;

  if (!requestOpenModal('deviceHealthModal', triggerEl)) return;

  const title = document.getElementById('healthModalTitle');
  const subtitle = document.getElementById('healthModalSubtitle');
  const badge = document.getElementById('healthModalStatusBadge');
  const reasonsList = document.getElementById('healthModalReasonsList');
  const factsGrid = document.getElementById('healthModalFactsGrid');
  const timeline = document.getElementById('healthModalEventsTimeline');

  if (title) title.innerText = d.alias ? `${d.alias} (${d.hostname || d.id})` : (d.hostname || d.id);
  if (subtitle) subtitle.innerText = `设备 ID: ${d.id} • OS: ${d.os || 'unknown'} (${d.arch || ''}) • Agent: ${d.agent_version || '-'}`;

  const h = d.health;
  const status = h ? h.status : 'unknown';
  let statusBg = 'rgba(16, 185, 129, 0.1)';
  let statusColor = 'var(--emerald)';
  let statusText = 'HEALTHY (健康)';
  if (status === 'degraded') {
    statusBg = 'rgba(245, 158, 11, 0.1)';
    statusColor = '#f59e0b';
    statusText = 'DEGRADED (注意 / 异常)';
  } else if (status === 'offline') {
    statusBg = 'rgba(239, 68, 68, 0.1)';
    statusColor = '#ef4444';
    statusText = 'OFFLINE (离线)';
  } else if (status === 'unknown') {
    statusBg = 'rgba(148, 163, 184, 0.1)';
    statusColor = 'var(--text-muted)';
    statusText = 'UNKNOWN (未知)';
  }

  if (badge) {
    badge.style.background = statusBg;
    badge.style.color = statusColor;
    badge.style.border = `1px solid ${statusColor}`;
    badge.innerText = statusText;
  }

  // Reasons list
  if (reasonsList) {
    if (!h || !h.reasons || h.reasons.length === 0) {
      reasonsList.innerHTML = `<div class="text-muted" style="font-size:0.8rem;padding:8px 0;">该设备当前无活跃异常规则。</div>`;
    } else {
      reasonsList.innerHTML = h.reasons.map(r => `
        <div style="background:rgba(255,255,255,0.03);border-radius:6px;padding:8px 10px;border-left:3px solid ${r.severity === 'critical' ? '#ef4444' : '#f59e0b'};">
          <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:4px;">
            <span class="font-mono" style="font-weight:700;font-size:0.8rem;color:#f8fafc;">${escapeHTML(r.code)}</span>
            <span style="font-size:0.7rem;text-transform:uppercase;color:${r.severity === 'critical' ? '#ef4444' : '#f59e0b'};font-weight:600;">${escapeHTML(r.severity)}</span>
          </div>
          <div style="font-size:0.78rem;color:var(--text-primary);margin-bottom:4px;">${escapeHTML(r.summary)}</div>
          ${r.suggested_action ? `<div style="font-size:0.72rem;color:#38bdf8;">建议操作: ${escapeHTML(r.suggested_action)}</div>` : ''}
        </div>
      `).join('');
    }
  }

  // Facts Grid
  if (factsGrid) {
    const facts = (h && h.facts) || d.runtime;
    if (!facts) {
      factsGrid.innerHTML = `<div class="text-muted" style="grid-column:1/-1;font-size:0.8rem;">该设备尚未上报最小运行指标 (需运行 v0.6.0+ Agent)。</div>`;
    } else {
      const hasMemoryFacts = Number.isFinite(facts.memory_total_bytes)
        && Number.isFinite(facts.memory_available_bytes)
        && facts.memory_total_bytes > 0
        && facts.memory_available_bytes > 0
        && facts.memory_available_bytes <= facts.memory_total_bytes;
      const memTotalMB = hasMemoryFacts ? Math.round(facts.memory_total_bytes / (1024 * 1024)) : 0;
      const memAvailMB = hasMemoryFacts ? Math.round(facts.memory_available_bytes / (1024 * 1024)) : 0;
      const memUsedRatio = hasMemoryFacts ? Math.round(((facts.memory_total_bytes - facts.memory_available_bytes) / facts.memory_total_bytes) * 100) : 0;
      const memoryLabel = hasMemoryFacts ? `内存使用率 (${memUsedRatio}%)` : '内存使用率';
      const memoryValue = hasMemoryFacts ? `${memTotalMB - memAvailMB} / ${memTotalMB} MiB` : '-';

      const hasDiskFacts = Number.isFinite(facts.disk_total_bytes)
        && Number.isFinite(facts.disk_available_bytes)
        && facts.disk_total_bytes > 0
        && facts.disk_available_bytes >= 0
        && facts.disk_available_bytes <= facts.disk_total_bytes;
      const diskTotalGB = hasDiskFacts ? (facts.disk_total_bytes / (1024 * 1024 * 1024)).toFixed(1) : 0;
      const diskAvailGB = hasDiskFacts ? (facts.disk_available_bytes / (1024 * 1024 * 1024)).toFixed(1) : 0;
      const diskUsedRatio = hasDiskFacts && Number(diskTotalGB) > 0 ? Math.round(((facts.disk_total_bytes - facts.disk_available_bytes) / facts.disk_total_bytes) * 100) : 0;
      const diskMountLabel = facts.disk_mount || '/';
      const diskLabel = hasDiskFacts ? `磁盘使用率 [${escapeHTML(diskMountLabel)}] (${diskUsedRatio}%)` : `磁盘使用率 [${escapeHTML(diskMountLabel)}]`;
      const diskValue = hasDiskFacts ? `${(diskTotalGB - diskAvailGB).toFixed(1)} / ${diskTotalGB} GiB` : '-';

      factsGrid.innerHTML = `
        <div>
          <div style="font-size:0.7rem;color:var(--text-muted);margin-bottom:2px;">CPU / 负载</div>
          <div style="font-size:0.85rem;font-weight:600;">${facts.logical_cpu_count || '-'} 核 • Load: ${facts.load_1 !== undefined && facts.load_1 !== null ? facts.load_1.toFixed(2) : '-'}</div>
        </div>
        <div>
          <div style="font-size:0.7rem;color:var(--text-muted);margin-bottom:2px;">运行时间 (Uptime)</div>
          <div style="font-size:0.85rem;font-weight:600;">${facts.uptime_seconds ? Math.round(facts.uptime_seconds / 3600) + ' 小时' : '-'}</div>
        </div>
        <div>
          <div style="font-size:0.7rem;color:var(--text-muted);margin-bottom:2px;">${memoryLabel}</div>
          <div style="font-size:0.82rem;font-weight:600;">${memoryValue}</div>
        </div>
        <div>
          <div style="font-size:0.7rem;color:var(--text-muted);margin-bottom:2px;">${diskLabel}</div>
          <div style="font-size:0.82rem;font-weight:600;">${diskValue}</div>
        </div>
      `;
    }
  }

  // Timeline
  if (timeline) {
    timeline.innerHTML = `<div class="text-muted">正在加载历史变动记录...</div>`;
    try {
      const res = await apiFetch(`${state.serverHost}/api/v1/devices/${d.id}/health/events?limit=10`);
      if (res.ok) {
        const data = await res.json();
        const events = data.events || [];
        if (events.length === 0) {
          timeline.innerHTML = `<div class="text-muted" style="padding:6px 0;">暂无状态变动历史记录。</div>`;
        } else {
          timeline.innerHTML = events.map(ev => `
            <div style="display:flex;align-items:center;justify-content:space-between;padding:6px 8px;background:rgba(255,255,255,0.02);border-radius:4px;">
              <div>
                <span class="status-badge ${ev.type === 'resolved' ? 'status-synced' : 'status-error'}" style="font-size:0.65rem;margin-right:6px;">${escapeHTML(ev.type.toUpperCase())}</span>
                <span class="font-mono" style="font-weight:600;">${escapeHTML(ev.reason_code)}</span>
              </div>
              <span class="text-muted" style="font-size:0.72rem;">${new Date(ev.occurred_at).toLocaleTimeString()}</span>
            </div>
          `).join('');
        }
      } else {
        timeline.innerHTML = `<div class="text-muted">无法拉取历史记录</div>`;
      }
    } catch (e) {
      timeline.innerHTML = `<div class="text-muted">加载历史异常: ${escapeHTML(e.message)}</div>`;
    }
  }
}
