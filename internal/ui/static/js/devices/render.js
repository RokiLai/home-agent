import { state } from '../state.js';
import { escapeHTML, formatRelativeTime, getOSInfo, filterAndClassifyIPs } from '../utils.js';
import { attachDeviceCardEvents } from './actions.js';

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
  if (!dashboardDeviceSummary) return;

  if (state.devices.length === 0) {
    dashboardDeviceSummary.innerHTML = `
      <div class="text-muted" style="font-size:0.85rem; padding: 10px 0;">暂无接入设备，请通过快速接入向导添加新主机。</div>
    `;
    return;
  }

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
