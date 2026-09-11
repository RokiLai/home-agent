import { state } from '../state.js';
import { escapeHTML, formatRelativeTime, getOSInfo, filterAndClassifyIPs } from '../utils.js';
import { attachDeviceCardEvents } from './actions.js';
import { mapCommandStatus } from '../commands.js';

export function getDeviceSyncStatus(d) {
  if (!d) return 'pending';
  const status = d.sync_status || d.status || 'pending';
  if (status === 'succeeded') return 'synced';
  return status;
}

export function isDeviceSynced(d) {
  const status = getDeviceSyncStatus(d);
  return status === 'synced';
}

export function updateStats() {
  const statTotal = document.getElementById('statTotalDevices');
  const statSynced = document.getElementById('statSyncedDevices');
  const statPending = document.getElementById('statPendingDevices');
  const statVersion = document.getElementById('statLatestVersion');
  const deviceListCount = document.getElementById('deviceListCount');
  const dashboardDeviceCount = document.getElementById('dashboardDeviceCount');
  const navDeviceBadge = document.getElementById('navDeviceBadge');

  const total = state.devices.length;
  const synced = state.devices.filter(d => isDeviceSynced(d)).length;
  const pending = total - synced;

  if (statTotal) statTotal.innerText = total;
  if (statSynced) statSynced.innerText = synced;
  if (statPending) statPending.innerText = pending;
  if (statVersion) {
    statVersion.innerText = state.serverHash ? state.serverHash.slice(0, 8) : '-';
    statVersion.title = state.serverHash || '暂无服务端配置 Hash';
  }

  if (deviceListCount) deviceListCount.innerText = `${total} 台设备`;
  if (dashboardDeviceCount) dashboardDeviceCount.innerText = `${total} 台设备`;
  if (navDeviceBadge) navDeviceBadge.innerText = total;
}

export function isDeviceOnline(d) {
  if (d.connected !== undefined) return Boolean(d.connected);
  if (!d.updated_at) return false;
  const diff = (Date.now() - new Date(d.updated_at).getTime()) / 1000;
  return diff < 45;
}

export function renderDashboardSummary() {
  const dashboardDeviceSummary = document.getElementById('dashboardDeviceSummary');
  if (dashboardDeviceSummary) {
    if (state.devices.length === 0) {
      dashboardDeviceSummary.innerHTML = `
        <div class="text-muted" style="font-size:0.85rem; padding: 10px 0;">暂无接入设备，请通过快速接入向导添加新主机。</div>
      `;
    } else {
      const previewDevices = state.devices.slice(0, 6);
      dashboardDeviceSummary.innerHTML = previewDevices.map(d => {
        const online = isDeviceOnline(d);
        const displayName = d.alias ? `${escapeHTML(d.alias)} (${escapeHTML(d.hostname)})` : escapeHTML(d.hostname);
        const isSynced = isDeviceSynced(d);

        return `
          <div class="summary-device-chip">
            <div class="summary-device-left">
              <span class="pulse-dot" style="background-color: ${online ? 'var(--emerald)' : 'var(--text-muted)'}; box-shadow: ${online ? '0 0 6px var(--emerald)' : 'none'};"></span>
              <div style="min-width: 0;">
                <div style="font-size: 0.86rem; font-weight: 600; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 170px;">
                  ${displayName}
                </div>
                <div class="font-mono text-muted" style="font-size: 0.7rem;">${escapeHTML(d.os || 'Unknown')} • ${escapeHTML(d.arch || '')}</div>
              </div>
            </div>
            <span class="status-badge ${isSynced ? 'status-synced' : 'status-pending'}">
              ${isSynced ? 'SYNCED' : 'PENDING'}
            </span>
          </div>
        `;
      }).join('');
    }
  }
  renderDashboardFocus();
}

export function renderDashboardFocus() {
  const actionableList = document.getElementById('dashboardActionableList');
  const recentCommandsList = document.getElementById('dashboardRecentCommandsList');
  const healthStatsSummary = document.getElementById('dashboardHealthStatsSummary');

  const total = state.devices.length;
  let healthyCount = 0;
  let degradedCount = 0;
  let offlineCount = 0;
  let unknownCount = 0;

  state.devices.forEach(d => {
    const s = (d.health && d.health.status) || 'unknown';
    if (s === 'healthy') healthyCount++;
    else if (s === 'degraded') degradedCount++;
    else if (s === 'offline') offlineCount++;
    else unknownCount++;
  });

  if (healthStatsSummary) {
    healthStatsSummary.innerHTML = `
      <div class="health-stat-pill stat-healthy"><span class="pulse-dot" style="background-color: var(--emerald);"></span> 正常 ${healthyCount}</div>
      <div class="health-stat-pill stat-degraded"><span class="pulse-dot" style="background-color: var(--amber);"></span> 异常 ${degradedCount}</div>
      <div class="health-stat-pill stat-offline"><span class="pulse-dot" style="background-color: var(--rose);"></span> 离线 ${offlineCount}</div>
      ${unknownCount > 0 ? `<div class="health-stat-pill stat-unknown"><span class="pulse-dot" style="background-color: var(--text-muted);"></span> 未知 ${unknownCount}</div>` : ''}
    `;
  }

  if (actionableList) {
    if (total === 0) {
      actionableList.innerHTML = `<div class="text-muted" style="padding: 16px; text-align: center;">暂无接入设备，请通过添加设备接入新主机。</div>`;
    } else {
      // Actionable: offline first, then degraded, stable sort by id
      const actionable = state.devices
        .filter(d => d.health && (d.health.status === 'offline' || d.health.status === 'degraded'))
        .sort((a, b) => {
          const rank = s => s === 'offline' ? 0 : 1;
          const diff = rank(a.health.status) - rank(b.health.status);
          if (diff !== 0) return diff;
          return a.id.localeCompare(b.id);
        })
        .slice(0, 10);

      if (actionable.length === 0) {
        actionableList.innerHTML = `<div class="dashboard-empty-ok" style="padding: 16px; text-align: center; color: var(--emerald); font-weight: 500;">✓ 全网设备运行状态良好，暂无待处理异常。</div>`;
      } else {
        actionableList.innerHTML = actionable.map(d => {
          const isOffline = d.health.status === 'offline';
          const reasons = d.health.reasons || [];
          const mainReason = reasons.length > 0 ? reasons[0].summary : (isOffline ? '设备当前处于离线状态' : '存在异常状态');
          const remaining = reasons.length > 1 ? `（还有 ${reasons.length - 1} 项）` : '';
          const devName = d.alias || d.hostname || d.id;

          return `
            <div class="actionable-device-card ${isOffline ? 'border-offline' : 'border-degraded'}">
              <div class="actionable-header">
                <span class="actionable-name">${escapeHTML(devName)}</span>
                <span class="status-badge ${isOffline ? 'health-offline' : 'health-degraded'}">
                  ${isOffline ? 'OFFLINE' : 'DEGRADED'}
                </span>
              </div>
              <div class="actionable-reason text-muted" style="font-size: 0.82rem; margin: 4px 0;">
                ${escapeHTML(mainReason)} ${escapeHTML(remaining)}
              </div>
              <div class="actionable-footer">
                <a href="#/devices/${encodeURIComponent(d.id)}/health" class="btn btn-sm btn-outline-secondary">查看健康原因</a>
              </div>
            </div>
          `;
        }).join('');
      }
    }
  }

  if (recentCommandsList) {
    const recents = (state.recentCommands || []).slice(0, 5);
    if (recents.length === 0) {
      recentCommandsList.innerHTML = `<div class="text-muted" style="padding: 16px; text-align: center;">暂无最近操作记录</div>`;
    } else {
      recentCommandsList.innerHTML = recents.map(c => `
        <div class="recent-command-row" style="display: flex; justify-content: space-between; align-items: center; padding: 8px 0; border-bottom: 1px solid var(--border-color);">
          <div>
            <div style="font-weight: 600; font-size: 0.85rem;">${escapeHTML(c.kind)} · <span class="font-mono">${escapeHTML(c.device_id)}</span></div>
            <div class="text-muted" style="font-size: 0.75rem;">${escapeHTML(new Date(c.created_at).toLocaleTimeString())}</div>
          </div>
          <div>
            <span class="status-badge" title="${escapeHTML(c.status)}">${escapeHTML(mapCommandStatus(c.status))}</span>
          </div>
        </div>
      `).join('');
    }
  }
}

export function renderDevices() {
  const deviceContainer = document.getElementById('deviceContainer');
  const searchInput = document.getElementById('deviceSearchInput');
  if (!deviceContainer) return;

  const validFilters = ['all', 'healthy', 'degraded', 'synced', 'pending'];
  if (!validFilters.includes(state.currentFilter)) {
    state.currentFilter = 'all';
    const filterPills = document.querySelectorAll('#deviceFilterPills .filter-pill, .filter-pill');
    filterPills.forEach(pill => {
      if (pill.dataset && pill.dataset.filter === 'all') {
        pill.classList.add('active');
      } else {
        pill.classList.remove('active');
      }
    });
  }

  const query = searchInput ? searchInput.value.trim().toLowerCase() : '';
  let filtered = state.devices;

  // Apply Filter Pills
  if (state.currentFilter === 'healthy') {
    filtered = filtered.filter(d => d.health && d.health.status === 'healthy');
  } else if (state.currentFilter === 'degraded') {
    filtered = filtered.filter(d => d.health && d.health.status === 'degraded');
  } else if (state.currentFilter === 'synced') {
    filtered = filtered.filter(d => isDeviceSynced(d));
  } else if (state.currentFilter === 'pending') {
    filtered = filtered.filter(d => !isDeviceSynced(d));
  }

  // Apply Search
  if (query) {
    filtered = filtered.filter(d => {
      const hostname = (d.hostname || '').toLowerCase();
      const alias = (d.alias || '').toLowerCase();
      const id = (d.id || '').toLowerCase();
      const rawIps = (d.addresses || []).map(a => typeof a === 'string' ? a : (a.ip || '')).join(' ');
      const ddnsDomain = (d.ddns_domain || '').toLowerCase();
      return hostname.includes(query) || alias.includes(query) || id.includes(query) || rawIps.includes(query) || ddnsDomain.includes(query);
    });
  }

  if (filtered.length === 0) {
    deviceContainer.innerHTML = `
      <div class="empty-state" style="grid-column: 1 / -1; text-align: center; padding: 40px 20px; color: var(--text-muted);">
        <svg width="40" height="40" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" style="margin-bottom: 10px; opacity: 0.5;">
          <circle cx="12" cy="12" r="10"/>
          <line x1="8" y1="12" x2="16" y2="12"/>
        </svg>
        <p style="font-size: 0.9rem;">没有找到匹配的设备</p>
      </div>
    `;
    return;
  }

  deviceContainer.innerHTML = filtered.map(d => createDeviceCardHTML(d)).join('');
  attachDeviceCardEvents();
}

export function createDeviceCardHTML(d) {
  const online = isDeviceOnline(d);
  const hasAlias = Boolean(d.alias && d.alias.trim());
  const displayName = hasAlias ? d.alias.trim() : (d.hostname || d.id);
  const origHostDesc = hasAlias && d.hostname ? `(${d.hostname})` : '';
  const osInfo = getOSInfo(d.os);
  const isWaking = state.wakingDevices.has(d.id);
  const isUpgrading = state.upgradingDevices.has(d.id);
  const isShuttingDown = state.shuttingDownDevices.has(d.id);

  // Sync status badge
  const syncStatus = getDeviceSyncStatus(d);
  let statusClass = 'status-pending';
  let statusText = 'PENDING';
  if (syncStatus === 'synced') {
    statusClass = 'status-synced';
    statusText = 'SYNCED';
  } else if (syncStatus === 'error') {
    statusClass = 'status-error';
    statusText = 'ERROR';
  }

  // IP classification
  const { ipv4, ipv6 } = filterAndClassifyIPs(d.addresses);
  const primaryIPv4 = ipv4.length > 0 ? ipv4[0] : '';
  const primaryIPv6 = ipv6.length > 0 ? ipv6[0] : '';
  const mainTargetIP = primaryIPv4 || primaryIPv6 || (d.addresses && d.addresses[0]) || '127.0.0.1';

  // SSH target and command
  const sshUser = d.ssh_user || 'root';
  const sshPort = d.ssh_port || 22;
  const sshCmd = sshPort === 22 ? `ssh ${sshUser}@${mainTargetIP}` : `ssh -p ${sshPort} ${sshUser}@${mainTargetIP}`;
  // Applied configuration hash
  const appliedHash = d.applied_hash ? d.applied_hash.slice(0, 8) : '-';
  const lastSync = formatRelativeTime(d.sync_updated_at);
  const ghSyncChecked = d.github_sync_enabled ? 'checked' : '';

  // Health status badge
  let healthBadge = '';
  if (d.health) {
    const hStatus = d.health.status || 'unknown';
    let hColor = 'var(--emerald)';
    let hText = 'HEALTHY';
    if (hStatus === 'degraded') {
      hColor = '#f59e0b';
      hText = d.health.reasons && d.health.reasons.length > 0 ? `DEGRADED (${d.health.reasons.length})` : 'DEGRADED';
    } else if (hStatus === 'offline') {
      hColor = '#ef4444';
      hText = 'OFFLINE';
    } else if (hStatus === 'unknown') {
      hColor = 'var(--text-muted)';
      hText = 'UNKNOWN';
    }
    const reasonTitles = d.health.reasons && d.health.reasons.length > 0 ? d.health.reasons.map(r => r.summary).join('; ') : '综合健康状态正常';
    healthBadge = `
      <button class="btn-view-health health-badge health-${hStatus}" data-id="${escapeHTML(d.id)}" style="cursor:pointer; font-size:0.68rem; font-weight:700; padding:2px 6px; border-radius:4px; border:1px solid ${hColor}; color:${hColor}; background:rgba(255,255,255,0.03);" title="${escapeHTML(reasonTitles)} (点击查看健康详情)" aria-label="${escapeHTML(hText)} (点击查看健康详情)">
        ${hText}
      </button>
    `;
  }

  return `
    <div class="device-card" data-id="${escapeHTML(d.id)}">
      <div>
        <!-- Top Header -->
        <div class="device-card-header">
          <div class="device-host-info">
            <div class="os-icon" title="${escapeHTML(osInfo.name)} (${escapeHTML(d.arch || '')})">${osInfo.icon}</div>
            <div class="device-title-area">
              <div class="device-title-row">
                <span class="device-hostname ${hasAlias ? 'has-alias' : ''}" title="${escapeHTML(displayName)}">${escapeHTML(displayName)}</span>
                <button class="btn-rename-device" data-id="${escapeHTML(d.id)}" data-hostname="${escapeHTML(d.hostname || '')}" data-alias="${escapeHTML(d.alias || '')}" data-mac="${escapeHTML(d.mac || '')}" title="修改设备备注名" aria-label="修改设备备注名">
                  <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                    <path d="M12 20h9"/>
                    <path d="M16.5 3.5a2.121 2.121 0 0 1 3 3L7 19l-4 1 1-4L16.5 3.5z"/>
                  </svg>
                </button>
              </div>
              <div class="device-id-tag font-mono" title="${escapeHTML(d.id)}">
                ${origHostDesc ? `<span class="device-orig-host">${escapeHTML(origHostDesc)}</span> ` : ''}${escapeHTML(d.id)}
              </div>
            </div>
          </div>
          <div class="header-badges">
            ${healthBadge}
            <span class="status-badge ${statusClass}" title="SSH 密钥同步状态">
              ${statusText}
            </span>
          </div>
        </div>

        <!-- Body Details -->
        <div class="device-details">
          <!-- IPv4 Row -->
          <div class="detail-row">
            <span class="ip-type-badge badge-ipv4">IPv4</span>
            <div class="ip-display-box">
              ${primaryIPv4 ? `
                <span class="ip-chip btn-copy-ip" data-ip="${escapeHTML(primaryIPv4)}" title="点击复制 IPv4: ${escapeHTML(primaryIPv4)}" role="button" aria-label="点击复制 IPv4">${escapeHTML(primaryIPv4)}</span>
                ${ipv4.length > 1 ? `<button class="ip-more-btn btn-view-ips" data-id="${escapeHTML(d.id)}" data-type="IPv4" title="查看全部 ${ipv4.length} 个 IPv4" aria-label="查看全部 ${ipv4.length} 个 IPv4">+${ipv4.length - 1}</button>` : ''}
              ` : `<span class="detail-value text-muted ip-none">无局域网 IPv4</span>`}
            </div>
          </div>

          <!-- IPv6 Row -->
          <div class="detail-row">
            <span class="ip-type-badge badge-ipv6">IPv6</span>
            <div class="ip-display-box">
              ${primaryIPv6 ? `
                <span class="ip-chip ip-v6-chip btn-copy-ip" data-ip="${escapeHTML(primaryIPv6)}" title="点击复制 IPv6: ${escapeHTML(primaryIPv6)}" role="button" aria-label="点击复制 IPv6">${escapeHTML(primaryIPv6)}</span>
                ${ipv6.length > 1 ? `<button class="ip-more-btn btn-view-ips" data-id="${escapeHTML(d.id)}" data-type="IPv6" title="查看全部 ${ipv6.length} 个 IPv6" aria-label="查看全部 ${ipv6.length} 个 IPv6">+${ipv6.length - 1}</button>` : ''}
              ` : `<span class="detail-value text-muted ip-none">无公网 IPv6</span>`}
            </div>
          </div>

          <!-- DDNS Domain (Optional) -->
          ${d.ddns_domain ? `
          <div class="detail-row">
            <span class="detail-label">DDNS 域名</span>
            <div class="ip-display-box">
              <span class="ip-chip btn-copy-ip" data-ip="${escapeHTML(d.ddns_domain)}" style="color: #38bdf8;" title="点击复制 DDNS 域名" role="button" aria-label="点击复制 DDNS 域名">${escapeHTML(d.ddns_domain)}</span>
            </div>
          </div>
          ` : ''}

          <!-- Physical MAC -->
          <div class="detail-row">
            <span class="detail-label">物理 MAC</span>
            <div class="ip-display-box">
              ${d.mac ? `
                <span class="ip-chip btn-copy-ip" data-ip="${escapeHTML(d.mac)}" title="点击复制 MAC 地址" role="button" aria-label="点击复制 MAC 地址">${escapeHTML(d.mac)}</span>
              ` : `<span class="detail-value text-muted ip-none">未上报 MAC</span>`}
            </div>
          </div>

          <!-- Agent Version -->
          <div class="detail-row">
            <span class="detail-label">Agent 版本</span>
            <span class="detail-value font-mono ${d.agent_version ? 'text-indigo' : 'text-muted'}" style="font-weight: 600;">
              ${escapeHTML(d.agent_version || '待升级')}
            </span>
          </div>

          <!-- System & Architecture -->
          <div class="detail-row">
            <span class="detail-label">系统架构</span>
            <span class="detail-value">${escapeHTML(osInfo.name)} • ${escapeHTML(d.arch || 'amd64')}</span>
          </div>

          <!-- Applied configuration hash -->
          <div class="detail-row">
            <span class="detail-label">同步 Hash</span>
            <span class="detail-value text-violet font-mono" style="font-weight: 600;" title="${escapeHTML(d.applied_hash || '暂无同步 Hash')}">${appliedHash}</span>
          </div>

          <!-- GitHub Sync Toggle & Status -->
          <div class="detail-row">
            <span class="detail-label">GitHub 凭据同步</span>
            <div style="display:flex; align-items:center; gap:6px; justify-content:flex-end;">
              <label class="github-sync-toggle" title="为该设备自动生成并注入 GitHub SSH 密钥与 gh 凭据">
                <input type="checkbox" class="gh-sync-check" data-id="${escapeHTML(d.id)}" ${ghSyncChecked}>
                <span style="font-size:0.75rem; font-weight:600; color: ${d.github_sync_enabled ? 'var(--emerald)' : 'var(--text-muted)'};">
                  ${d.github_sync_enabled ? '已启用' : '已禁用'}
                </span>
              </label>
              ${d.github_status ? `<span class="badge ${d.github_status === 'synced' ? 'badge-primary' : 'badge-secondary'}" style="font-size:0.62rem;">${escapeHTML(d.github_status)}</span>` : ''}
            </div>
          </div>

          <!-- Owner Info Row -->
          ${d.owner_user_id ? `
          <div class="detail-row">
            <span class="detail-label">归属所有者</span>
            <span class="detail-value font-mono text-xs" style="color:var(--indigo); font-weight:600;">${escapeHTML(d.owner_user_id)}</span>
          </div>
          ` : ''}

          <!-- Last Sync Time -->
          <div class="detail-row">
            <span class="detail-label">上次同步</span>
            <span class="detail-value">${lastSync}</span>
          </div>
        </div>
      </div>

      <div>
        <!-- Bottom Action Buttons -->
        <div class="device-actions">
          <!-- Fast SSH Box -->
          <button class="btn-ssh-box" data-ssh="${escapeHTML(sshCmd)}" title="点击直接复制 SSH 登录命令" aria-label="复制 SSH 登录命令">
            <span class="ssh-prompt">$</span>
            <span class="ssh-cmd-text">${escapeHTML(sshCmd)}</span>
            <span class="ssh-btn-text">
              <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <rect x="9" y="9" width="13" height="13" rx="2" ry="2"/>
                <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>
              </svg>
              复制
            </span>
          </button>

          <!-- WOL Wake Button -->
          <button class="btn-wake-device ${online ? 'is-online' : 'is-offline'} ${isWaking ? 'is-waking' : ''}"
                  data-id="${escapeHTML(d.id)}"
                  data-hostname="${escapeHTML(d.hostname || '')}"
                  data-mac="${escapeHTML(d.mac || '')}"
                  aria-label="唤醒设备"
                  ${!d.mac ? 'disabled title="客户端尚未上报 MAC 地址"' : (isWaking ? 'disabled title="正在唤醒中..."' : `title="${online ? '设备当前在线 (点击可再次发送 WOL 封包)' : '发送局域网 WOL 魔术包唤醒设备'}"`)}>
            <span class="wake-icon">${isWaking ? '⏳' : '⚡'}</span>
            <span>${isWaking ? '唤醒中' : '唤醒'}</span>
          </button>

          <!-- More Actions Dropdown Wrapper -->
          <div class="dropdown-wrapper">
            <button class="btn-more-actions" data-id="${escapeHTML(d.id)}" title="更多设备与电源操作" aria-label="更多设备与电源操作" aria-expanded="${state.openDropdownDevID === d.id ? 'true' : 'false'}">
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5">
                <circle cx="12" cy="12" r="1.5"/>
                <circle cx="19" cy="12" r="1.5"/>
                <circle cx="5" cy="12" r="1.5"/>
              </svg>
            </button>
            <div class="device-dropdown-menu ${state.openDropdownDevID === d.id ? 'is-open' : ''}" id="dropdown-${escapeHTML(d.id)}">
              <button class="dropdown-item btn-sync-device btn-menu-sync" data-id="${escapeHTML(d.id)}" data-hostname="${escapeHTML(d.hostname || '')}">
                <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                  <path d="M21.5 2v6h-6M21.34 15.57a10 10 0 1 1-.57-8.38l5.67-5.67"/>
                </svg>
                <span>立即同步密钥</span>
              </button>
              <button class="dropdown-item btn-upgrade-device btn-menu-upgrade ${isUpgrading ? 'is-upgrading' : ''}"
                      data-id="${escapeHTML(d.id)}"
                      data-hostname="${escapeHTML(d.hostname || '')}"
                      ${isUpgrading ? 'disabled' : ''}>
                <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" class="${isUpgrading ? 'spinning' : ''}">
                  <path d="M12 19V5M5 12l7-7 7 7"/>
                </svg>
                <span>${isUpgrading ? '正在自升级...' : '客户端自升级'}</span>
              </button>
              <div class="dropdown-divider"></div>
              <button class="dropdown-item btn-share-device btn-menu-share" data-id="${escapeHTML(d.id)}" data-hostname="${escapeHTML(d.hostname || '')}">
                <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                  <circle cx="18" cy="5" r="3"/>
                  <circle cx="6" cy="12" r="3"/>
                  <circle cx="18" cy="19" r="3"/>
                  <line x1="8.59" y1="13.51" x2="15.42" y2="17.49"/>
                  <line x1="15.41" y1="6.51" x2="8.59" y2="10.49"/>
                </svg>
                <span>共享与授权管理</span>
              </button>
              <button class="dropdown-item btn-transfer-device btn-menu-transfer" data-id="${escapeHTML(d.id)}" data-hostname="${escapeHTML(d.hostname || '')}">
                <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                  <polyline points="17 1 21 5 17 9"/>
                  <path d="M3 11V9a4 4 0 0 1 4-4h14"/>
                  <polyline points="7 23 3 19 7 15"/>
                  <path d="M21 13v2a4 4 0 0 1-4 4H3"/>
                </svg>
                <span>转移设备所有权</span>
              </button>
              <div class="dropdown-divider"></div>
              <button class="dropdown-item is-warning btn-shutdown-device btn-menu-shutdown ${!online ? 'is-offline' : ''} ${isShuttingDown ? 'is-shutting-down' : ''}"
                      data-id="${escapeHTML(d.id)}"
                      data-hostname="${escapeHTML(d.hostname || '')}"
                      data-alias="${escapeHTML(d.alias || '')}"
                      ${!online ? 'disabled' : (isShuttingDown ? 'disabled' : '')}>
                <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                  <path d="M18.36 6.64a9 9 0 1 1-12.73 0"/>
                  <line x1="12" y1="2" x2="12" y2="12"/>
                </svg>
                <span>${isShuttingDown ? '正在关机...' : '远程关闭设备'}</span>
              </button>
              <a href="#/devices/${encodeURIComponent(d.id)}/overview" class="dropdown-item btn-view-detail" style="text-decoration:none;">
                <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                  <circle cx="12" cy="12" r="10"/>
                  <line x1="12" y1="16" x2="12" y2="12"/>
                  <line x1="12" y1="8" x2="12.01" y2="8"/>
                </svg>
                <span>查看详情</span>
              </a>
              <button class="dropdown-item is-danger btn-del btn-menu-del" data-id="${escapeHTML(d.id)}" data-hostname="${escapeHTML(d.hostname || '')}">
                <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                  <polyline points="3 6 5 6 21 6"/>
                  <path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/>
                </svg>
                <span>移除此设备</span>
              </button>
            </div>
          </div>
        </div>
      </div>
    </div>
  `;
}

export function renderDeviceDetailView(deviceId, section = 'overview') {
  const container = document.getElementById('deviceDetailContainer');
  const titleEl = document.getElementById('deviceDetailTitle');
  const badgeEl = document.getElementById('deviceDetailBadge');
  if (!container) return;

  const device = (state.devices || []).find(d => d.id === deviceId);
  if (!device) {
    container.innerHTML = `
      <div class="empty-state" style="padding: 32px; text-align: center;">
        <p style="color: var(--rose); font-weight: 600;">未找到该设备或暂无访问权限。</p>
        <p class="text-muted" style="margin-top: 8px;">设备 ID: ${escapeHTML(deviceId)}</p>
        <a href="#/devices" class="btn btn-secondary mt-3">返回设备列表</a>
      </div>
    `;
    if (titleEl) titleEl.innerText = '设备不存在';
    if (badgeEl) badgeEl.innerHTML = '';
    return;
  }

  const displayName = device.alias ? `${device.alias} (${device.hostname})` : (device.hostname || device.id);
  if (titleEl) titleEl.innerText = displayName;

  const hStatus = (device.health && device.health.status) || 'unknown';
  if (badgeEl) {
    badgeEl.innerHTML = `<span class="health-badge health-${hStatus}">${hStatus.toUpperCase()}</span>`;
  }

  const tabLinks = document.querySelectorAll('.detail-tab');
  tabLinks.forEach(tab => {
    if (tab.dataset && tab.dataset.section === section) {
      tab.classList.add('active');
    } else if (tab.classList) {
      tab.classList.remove('active');
    }
  });

  if (section === 'health') {
    const reasons = (device.health && device.health.reasons) || [];
    const metrics = (device.health && device.health.metrics) || {};
    const cpu = metrics.cpu_usage !== undefined ? `${metrics.cpu_usage}%` : '未上报';
    const mem = metrics.mem_usage !== undefined ? `${metrics.mem_usage}%` : '未上报';
    const sampledAt = metrics.sampled_at ? new Date(metrics.sampled_at).toLocaleString() : '未上报';

    container.innerHTML = `
      <div class="detail-section-health">
        <div class="card mb-3">
          <div class="card-header"><strong>当前健康原因与诊断</strong></div>
          <div class="card-body">
            ${reasons.length === 0 ? '<p class="text-success">设备当前各项指标与健康结论正常。</p>' : reasons.map(r => `
              <div class="reason-item mb-2" style="border-left: 3px solid var(--amber); padding-left: 10px;">
                <div style="font-weight: 600;">${escapeHTML(r.summary)}</div>
                ${r.suggestion ? `<div class="text-muted font-sm">${escapeHTML(r.suggestion)}</div>` : ''}
              </div>
            `).join('')}
          </div>
        </div>
        <div class="card">
          <div class="card-header"><strong>运行指标与采样时间</strong></div>
          <div class="card-body detail-grid">
            <div><span class="text-muted">CPU 使用率:</span> <strong>${escapeHTML(cpu)}</strong></div>
            <div><span class="text-muted">内存使用率:</span> <strong>${escapeHTML(mem)}</strong></div>
            <div><span class="text-muted">采样时间:</span> <span>${escapeHTML(sampledAt)}</span></div>
          </div>
        </div>
      </div>
    `;
  } else if (section === 'ssh') {
    const isSynced = isDeviceSynced(device);
    const portArg = device.ssh_port && device.ssh_port !== 22 ? `-p ${device.ssh_port} ` : '';
    const primaryIP = (device.addresses && device.addresses[0]) || device.hostname || '127.0.0.1';
    const sshCmd = `ssh ${portArg}${device.ssh_user || 'root'}@${primaryIP}`;
    const shared = device.shared_users || [];

    container.innerHTML = `
      <div class="detail-section-ssh">
        <div class="card mb-3">
          <div class="card-header"><strong>SSH 连接与公钥同步</strong></div>
          <div class="card-body detail-grid">
            <div><span class="text-muted">SSH 用户与端口:</span> <span class="font-mono">${escapeHTML(device.ssh_user || 'root')}:${escapeHTML(device.ssh_port || 22)}</span></div>
            <div><span class="text-muted">同步状态:</span> <span class="status-badge ${isSynced ? 'status-synced' : 'status-pending'}">${isSynced ? 'SYNCED' : 'PENDING'}</span></div>
            <div style="grid-column: 1 / -1;">
              <span class="text-muted">快捷 SSH 命令:</span>
              <div class="code-box mt-1" style="display:flex; justify-content:space-between; align-items:center;">
                <code class="font-mono">${escapeHTML(sshCmd)}</code>
                <button class="btn btn-copy" onclick="copyToClipboard('${escapeHTML(sshCmd)}', 'SSH 命令已复制')">复制</button>
              </div>
            </div>
          </div>
          <div class="card-footer mt-3" style="display:flex; justify-content:flex-end;">
            <button class="btn btn-primary btn-sm btn-detail-sync" onclick="handleDetailSync('${escapeHTML(device.id)}', '${escapeHTML(device.hostname || '')}')">立即同步公钥</button>
          </div>
        </div>
        <div class="card">
          <div class="card-header"><strong>设备共享与访问授权</strong></div>
          <div class="card-body">
            ${shared.length === 0 ? '<p class="text-muted font-sm">当前设备未向其他用户共享授权。</p>' : `
              <div class="shared-users-list">
                ${shared.map(u => `
                  <div class="shared-user-row" style="display:flex; justify-content:space-between; padding:6px 0; border-bottom:1px solid var(--border-color);">
                    <span>${escapeHTML(u.username || u.user_id)}</span>
                    <span class="badge badge-secondary font-sm">${escapeHTML(u.permission || 'view')}</span>
                  </div>
                `).join('')}
              </div>
            `}
          </div>
        </div>
      </div>
    `;
  } else if (section === 'network') {
    const addresses = device.addresses || [];
    const ipv4List = addresses.filter(a => !a.includes(':'));
    const ipv6List = addresses.filter(a => a.includes(':'));

    container.innerHTML = `
      <div class="detail-section-network">
        <div class="card mb-3">
          <div class="card-header"><strong>网络地址与接口</strong></div>
          <div class="card-body detail-grid">
            <div style="grid-column: 1 / -1;">
              <span class="text-muted">IPv4 地址:</span>
              <div class="ip-display-box mt-1" style="display:flex; flex-wrap:wrap; gap:6px;">
                ${ipv4List.length ? ipv4List.map(ip => `<span class="ip-chip btn-copy-ip" data-ip="${escapeHTML(ip)}" onclick="copyToClipboard('${escapeHTML(ip)}', '已复制 IPv4')">${escapeHTML(ip)}</span>`).join('') : '<span class="text-muted">无 IPv4 地址</span>'}
              </div>
            </div>
            <div style="grid-column: 1 / -1;">
              <span class="text-muted">IPv6 地址:</span>
              <div class="ip-display-box mt-1" style="display:flex; flex-wrap:wrap; gap:6px;">
                ${ipv6List.length ? ipv6List.map(ip => `<span class="ip-chip ip-v6-chip btn-copy-ip" data-ip="${escapeHTML(ip)}" onclick="copyToClipboard('${escapeHTML(ip)}', '已复制 IPv6')">${escapeHTML(ip)}</span>`).join('') : '<span class="text-muted">无 IPv6 地址</span>'}
              </div>
            </div>
            <div><span class="text-muted">物理 MAC:</span> <span class="font-mono">${escapeHTML(device.mac || '未上报')}</span></div>
            ${device.ddns_domain ? `<div><span class="text-muted">DDNS 域名:</span> <span class="font-mono text-indigo">${escapeHTML(device.ddns_domain)}</span></div>` : ''}
          </div>
        </div>
      </div>
    `;
  } else if (section === 'commands') {
    const devCommands = (state.recentCommands || []).filter(c => c.device_id === device.id);

    container.innerHTML = `
      <div class="detail-section-commands">
        <div class="card">
          <div class="card-header"><strong>该设备的操作记录 (${devCommands.length} 条)</strong></div>
          <div class="card-body">
            ${devCommands.length === 0 ? '<p class="text-muted">暂无该设备的操作记录。</p>' : `
              <table class="data-table" style="width:100%;">
                <thead>
                  <tr>
                    <th>时间</th>
                    <th>操作类型</th>
                    <th>状态</th>
                    <th>结果</th>
                  </tr>
                </thead>
                <tbody>
                  ${devCommands.map(c => `
                    <tr>
                      <td data-label="时间">${escapeHTML(new Date(c.created_at).toLocaleString())}</td>
                      <td data-label="操作类型">${escapeHTML(c.kind)}</td>
                      <td data-label="状态"><span class="status-badge">${escapeHTML(mapCommandStatus(c.status))}</span></td>
                      <td data-label="结果">${escapeHTML(c.error_message || (c.status === 'legacy_untracked' ? '旧客户端未关联' : '-'))}</td>
                    </tr>
                  `).join('')}
                </tbody>
              </table>
            `}
          </div>
        </div>
      </div>
    `;
  } else if (section === 'settings') {
    container.innerHTML = `
      <div class="detail-section-settings">
        <div class="card mb-3">
          <div class="card-header"><strong>设备基本设置</strong></div>
          <div class="card-body detail-grid">
            <div><span class="text-muted">当前备注名:</span> <strong>${escapeHTML(device.alias || '未设置')}</strong></div>
            <div><span class="text-muted">设备所有权:</span> <span class="font-mono text-indigo">${escapeHTML(device.owner_user_id || '系统默认')}</span></div>
          </div>
        </div>
        <div class="card danger-zone" style="border: 1px solid var(--rose); background: rgba(244, 63, 94, 0.05);">
          <div class="card-header" style="color: var(--rose);"><strong>危险区域与电源管理</strong></div>
          <div class="card-body" style="display:flex; flex-direction:column; gap:12px;">
            <div style="display:flex; justify-content:space-between; align-items:center;">
              <div>
                <div style="font-weight:600;">远程关闭设备</div>
                <div class="text-muted font-sm">通过 Agent 发送系统关机信号，关机后将断开连接。</div>
              </div>
              <button class="btn btn-warning btn-detail-shutdown" onclick="handleDetailShutdown('${escapeHTML(device.id)}', '${escapeHTML(device.hostname || '')}')">远程关机</button>
            </div>
            <div style="display:flex; justify-content:space-between; align-items:center; border-top:1px solid var(--border-color); padding-top:12px;">
              <div>
                <div style="font-weight:600; color:var(--rose);">移除此设备</div>
                <div class="text-muted font-sm">从控制平面注销该受管节点，注销后设备凭据将失效。</div>
              </div>
              <button class="btn btn-danger btn-detail-remove" onclick="handleDetailRemove('${escapeHTML(device.id)}', '${escapeHTML(device.hostname || '')}')">移除此设备</button>
            </div>
          </div>
        </div>
      </div>
    `;
  } else {
    // Overview (default)
    const osInfo = getOSInfo(device.os);
    const ips = (device.addresses || []).join(', ') || '未上报';
    const reasons = (device.health && device.health.reasons) || [];
    const reasonsSummary = reasons.length > 0 ? reasons.map(r => r.summary).join('; ') : '正常';

    container.innerHTML = `
      <div class="detail-section-overview">
        <div class="card mb-3">
          <div class="card-header"><strong>设备概览</strong></div>
          <div class="card-body detail-grid">
            <div><span class="text-muted">主机名:</span> <span class="font-mono">${escapeHTML(device.hostname || '-')}</span></div>
            <div><span class="text-muted">设备 ID:</span> <span class="font-mono">${escapeHTML(device.id)}</span></div>
            <div><span class="text-muted">物理 MAC:</span> <span class="font-mono">${escapeHTML(device.mac || '未上报')}</span></div>
            <div><span class="text-muted">操作系统:</span> <span>${escapeHTML(osInfo.name)} (${escapeHTML(device.arch || '-')})</span></div>
            <div><span class="text-muted">IP 地址:</span> <span>${escapeHTML(ips)}</span></div>
            <div><span class="text-muted">SSH 用户:</span> <span class="font-mono">${escapeHTML(device.ssh_user || 'root')}:${escapeHTML(device.ssh_port || 22)}</span></div>
            <div><span class="text-muted">Agent 版本:</span> <span class="font-mono">${escapeHTML(device.agent_version || '待升级')}</span></div>
            <div><span class="text-muted">健康状态:</span> <span>${escapeHTML(reasonsSummary)}</span></div>
          </div>
        </div>
        <div class="detail-actions mt-3" style="display:flex; gap:8px; flex-wrap:wrap;">
          <a href="#/devices" class="btn btn-secondary">← 返回设备列表</a>
          <a href="#/devices/${encodeURIComponent(device.id)}/health" class="btn btn-primary">查看健康诊断</a>
          <a href="#/devices/${encodeURIComponent(device.id)}/ssh" class="btn btn-secondary">SSH 与访问</a>
          <a href="#/devices/${encodeURIComponent(device.id)}/network" class="btn btn-secondary">网络详情</a>
          <a href="#/devices/${encodeURIComponent(device.id)}/commands" class="btn btn-secondary">操作记录</a>
          <a href="#/devices/${encodeURIComponent(device.id)}/settings" class="btn btn-secondary">设备设置</a>
        </div>
      </div>
    `;
  }
}
