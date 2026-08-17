(function() {
  'use strict';

  // State
  let devices = [];
  const urlParams = new URLSearchParams(window.location.search);
  const tokenFromUrl = urlParams.get('token');
  let joinToken = tokenFromUrl || localStorage.getItem('homeagent_token') || '';
  if (tokenFromUrl) {
    localStorage.setItem('homeagent_token', tokenFromUrl);
  }
  let serverHost = localStorage.getItem('homeagent_server_url') || window.location.origin;
  let activeOSTab = 'darwin';
  let isFetching = false;

  // DOM Elements
  const deviceContainer = document.getElementById('deviceContainer');
  const deviceListCount = document.getElementById('deviceListCount');
  const searchInput = document.getElementById('deviceSearchInput');
  const btnSyncAll = document.getElementById('btnSyncAll');
  const syncAllIcon = document.getElementById('syncAllIcon');
  const syncAllBtnText = document.getElementById('syncAllBtnText');
  const refreshBtn = document.getElementById('refreshBtn');
  const refreshIcon = document.getElementById('refreshIcon');
  const liveStatusText = document.getElementById('liveStatusText');
  
  const statTotal = document.getElementById('statTotalDevices');
  const statSynced = document.getElementById('statSyncedDevices');
  const statPending = document.getElementById('statPendingDevices');
  const statVersion = document.getElementById('statLatestVersion');

  const installCode = document.getElementById('installCommandCode');
  const copyCommandBtn = document.getElementById('copyCommandBtn');
  const tabBtns = document.querySelectorAll('.tab-btn');
  const eventLogContainer = document.getElementById('eventLogContainer');
  const clearLogsBtn = document.getElementById('clearLogsBtn');

  // Modal elements
  const tokenConfigBtn = document.getElementById('tokenConfigBtn');
  const tokenBtnLabel = document.getElementById('tokenBtnLabel');
  const tokenModal = document.getElementById('tokenModal');
  const tokenInput = document.getElementById('tokenInput');
  const serverUrlInput = document.getElementById('serverUrlInput');
  const saveTokenBtn = document.getElementById('saveTokenBtn');
  const cancelModalBtn = document.getElementById('cancelModalBtn');
  const closeModalBtn = document.getElementById('closeModalBtn');
  
  const ipModal = document.getElementById('ipModal');
  const ipModalTitle = document.getElementById('ipModalTitle');
  const ipModalDesc = document.getElementById('ipModalDesc');
  const ipModalList = document.getElementById('ipModalList');
  const closeIpModalBtn = document.getElementById('closeIpModalBtn');
  const doneIpModalBtn = document.getElementById('doneIpModalBtn');

  // Rename modal elements
  const renameModal = document.getElementById('renameModal');
  const renameDeviceInfo = document.getElementById('renameDeviceInfo');
  const deviceAliasInput = document.getElementById('deviceAliasInput');
  const saveRenameBtn = document.getElementById('saveRenameBtn');
  const cancelRenameModalBtn = document.getElementById('cancelRenameModalBtn');
  const closeRenameModalBtn = document.getElementById('closeRenameModalBtn');
  let currentRenameDeviceId = '';

  const toast = document.getElementById('toast');
  const toastMsg = document.getElementById('toastMsg');

  // Initialize
  function init() {
    updateTokenBadge();
    updateInstallCommand();
    fetchDevices();

    // Auto refresh every 4 seconds
    setInterval(fetchDevices, 4000);

    // Event listeners
    if (btnSyncAll) {
      btnSyncAll.addEventListener('click', handleSyncAll);
    }

    refreshBtn.addEventListener('click', () => {
      spinRefresh();
      fetchDevices();
    });

    searchInput.addEventListener('input', () => {
      renderDevices();
    });

    tabBtns.forEach(btn => {
      btn.addEventListener('click', () => {
        tabBtns.forEach(b => b.classList.remove('active'));
        btn.classList.add('active');
        activeOSTab = btn.dataset.os;
        updateInstallCommand();
      });
    });

    copyCommandBtn.addEventListener('click', () => {
      copyToClipboard(installCode.innerText, '安装命令已复制');
    });

    tokenConfigBtn.addEventListener('click', openTokenModal);
    closeModalBtn.addEventListener('click', closeTokenModal);
    cancelModalBtn.addEventListener('click', closeTokenModal);
    saveTokenBtn.addEventListener('click', saveTokenConfig);

    if (closeIpModalBtn) closeIpModalBtn.addEventListener('click', closeIpModal);
    if (doneIpModalBtn) doneIpModalBtn.addEventListener('click', closeIpModal);

    if (closeRenameModalBtn) closeRenameModalBtn.addEventListener('click', closeRenameModal);
    if (cancelRenameModalBtn) cancelRenameModalBtn.addEventListener('click', closeRenameModal);
    if (saveRenameBtn) saveRenameBtn.addEventListener('click', saveRenameDevice);
    if (deviceAliasInput) {
      deviceAliasInput.addEventListener('keydown', (e) => {
        if (e.key === 'Enter') {
          e.preventDefault();
          saveRenameDevice();
        }
      });
    }

    clearLogsBtn.addEventListener('click', () => {
      eventLogContainer.innerHTML = '';
    });

    // Global event delegation for dynamic elements
    document.addEventListener('click', (e) => {
      // 0. Click on Rename button
      const renameBtn = e.target.closest('.btn-rename-device');
      if (renameBtn && renameBtn.dataset.id) {
        e.preventDefault();
        e.stopPropagation();
        openRenameModal(renameBtn.dataset.id, renameBtn.dataset.host, renameBtn.dataset.alias);
        return;
      }

      // 1. Click on +N more IPs button
      const moreBtn = e.target.closest('.ip-more-btn');
      if (moreBtn && moreBtn.dataset.ips) {
        e.preventDefault();
        e.stopPropagation();
        try {
          const ips = JSON.parse(decodeURIComponent(moreBtn.dataset.ips));
          const ipType = moreBtn.dataset.type || 'IP';
          const host = moreBtn.dataset.host || '设备';
          openIpModal(host, ipType, ips);
        } catch (err) {
          console.error('Parse IPs error:', err);
        }
        return;
      }

      // 2. Click on IP chip
      const ipChip = e.target.closest('.btn-copy-ip');
      if (ipChip && ipChip.dataset.ip) {
        e.preventDefault();
        e.stopPropagation();
        copyToClipboard(ipChip.dataset.ip, `已复制 IP: ${ipChip.dataset.ip}`);
        return;
      }

      // 3. Click on SSH Box
      const sshBox = e.target.closest('.btn-ssh-box');
      if (sshBox && sshBox.dataset.ssh) {
        e.preventDefault();
        e.stopPropagation();
        const cmd = sshBox.dataset.ssh;
        copyToClipboard(cmd, `已复制 SSH 命令: ${cmd}`);

        const textSpan = sshBox.querySelector('.ssh-btn-text');
        if (textSpan) {
          const originalHTML = textSpan.innerHTML;
          sshBox.classList.add('copied');
          textSpan.innerHTML = '已复制 ✓';
          setTimeout(() => {
            sshBox.classList.remove('copied');
            textSpan.innerHTML = originalHTML;
          }, 1800);
        }
        return;
      }

      // 4. Click on per-device Sync button
      const syncBtn = e.target.closest('.btn-sync-device');
      if (syncBtn && syncBtn.dataset.id) {
        e.preventDefault();
        e.stopPropagation();
        syncDevice(syncBtn);
        return;
      }

      // 5. Click on Delete button
      const delBtn = e.target.closest('.btn-del');
      if (delBtn && delBtn.dataset.id) {
        e.preventDefault();
        e.stopPropagation();
        deleteDevice(delBtn.dataset.id, delBtn.dataset.host);
        return;
      }
    });
  }

  function openRenameModal(deviceId, hostname, alias) {
    if (!renameModal) return;
    currentRenameDeviceId = deviceId;
    if (renameDeviceInfo) {
      renameDeviceInfo.innerText = `${hostname} (${deviceId})`;
    }
    if (deviceAliasInput) {
      deviceAliasInput.value = alias || '';
    }
    renameModal.classList.remove('hidden');
    setTimeout(() => {
      if (deviceAliasInput) {
        deviceAliasInput.focus();
        deviceAliasInput.select();
      }
    }, 50);
  }

  function closeRenameModal() {
    if (renameModal) renameModal.classList.add('hidden');
    currentRenameDeviceId = '';
  }

  async function saveRenameDevice() {
    if (!currentRenameDeviceId) return;
    const newAlias = (deviceAliasInput ? deviceAliasInput.value : '').trim();
    const id = currentRenameDeviceId;

    try {
      const headers = {
        'Content-Type': 'application/json'
      };
      if (joinToken) {
        headers['Authorization'] = `Bearer ${joinToken}`;
      }

      const res = await fetch(`/api/v1/devices/${encodeURIComponent(id)}`, {
        method: 'PATCH',
        headers,
        body: JSON.stringify({ alias: newAlias })
      });

      if (res.status === 401) {
        showToast('认证失败，请先配置有效的 Join Token');
        openTokenModal();
        return;
      }
      if (!res.ok) {
        const text = await res.text();
        throw new Error(text || `HTTP ${res.status}`);
      }

      const updated = await res.json();
      const idx = devices.findIndex(d => d.id === id);
      if (idx !== -1) {
        devices[idx] = updated;
      }
      renderDevices();
      closeRenameModal();
      if (newAlias) {
        addLog('SUCCESS', `已设置设备备注名: ${id} -> "${newAlias}"`);
        showToast(`已成功修改名称: ${newAlias}`);
      } else {
        addLog('SUCCESS', `已清除设备备注名: ${id}`);
        showToast(`已恢复默认主机名`);
      }
      fetchDevices();
    } catch (err) {
      alert(`修改设备名称失败: ${err.message}`);
    }
  }

  function openIpModal(host, ipType, ips) {
    if (!ipModal) return;
    ipModalTitle.innerText = `${host} - ${ipType} 地址列表 (共 ${ips.length} 个)`;
    ipModalDesc.innerText = '点击任意地址即可一键复制到剪贴板：';
    ipModalList.innerHTML = ips.map(ip => `
      <div class="ip-modal-item btn-copy-ip" data-ip="${escapeHTML(ip)}" style="cursor: pointer;" title="点击复制">
        <span class="ip-modal-text">${escapeHTML(ip)}</span>
        <button class="btn btn-copy" style="pointer-events: none;">
          <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
            <rect x="9" y="9" width="13" height="13" rx="2" ry="2"/>
            <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>
          </svg>
          复制
        </button>
      </div>
    `).join('');
    ipModal.classList.remove('hidden');
  }

  function closeIpModal() {
    if (ipModal) ipModal.classList.add('hidden');
  }

  function updateTokenBadge() {
    if (joinToken) {
      tokenBtnLabel.innerText = 'Token: ••••••••';
    } else {
      tokenBtnLabel.innerText = '未配 Token';
    }
  }

  function openTokenModal() {
    tokenInput.value = joinToken;
    serverUrlInput.value = serverHost;
    tokenModal.classList.remove('hidden');
    tokenInput.focus();
  }

  function closeTokenModal() {
    tokenModal.classList.add('hidden');
  }

  function saveTokenConfig() {
    joinToken = tokenInput.value.trim();
    if (serverUrlInput.value.trim()) {
      serverHost = serverUrlInput.value.trim().replace(/\/+$/, '');
    }
    localStorage.setItem('homeagent_token', joinToken);
    localStorage.setItem('homeagent_server_url', serverHost);
    updateTokenBadge();
    updateInstallCommand();
    closeTokenModal();
    showToast('Token 与服务端地址已保存');
    fetchDevices();
  }

  async function handleSyncAll() {
    if (!btnSyncAll || btnSyncAll.disabled) return;
    btnSyncAll.disabled = true;
    if (syncAllIcon) syncAllIcon.classList.add('spinning');
    if (syncAllBtnText) syncAllBtnText.innerText = '同步中...';

    try {
      const headers = {};
      if (joinToken) {
        headers['Authorization'] = `Bearer ${joinToken}`;
      }
      const resp = await fetch('/api/v1/sync', {
        method: 'POST',
        headers: headers
      });

      if (resp.status === 401) {
        showToast('认证失败，请先配置有效的 Join Token');
        openTokenModal();
        return;
      }

      if (!resp.ok) {
        const text = await resp.text();
        showToast(`全网同步失败: ${text || resp.statusText}`);
        return;
      }

      const data = await resp.json();
      const count = (data.results && data.results.length) || 0;
      showToast(`全网同步已触发${count > 0 ? ` (${count} 台设备已对齐)` : ''}`);
      await fetchDevices();
    } catch (err) {
      showToast(`同步请求异常: ${err.message}`);
    } finally {
      btnSyncAll.disabled = false;
      if (syncAllIcon) syncAllIcon.classList.remove('spinning');
      if (syncAllBtnText) syncAllBtnText.innerText = '全网同步';
    }
  }

  function spinRefresh() {
    refreshIcon.style.transition = 'transform 0.5s ease';
    refreshIcon.style.transform = 'rotate(360deg)';
    setTimeout(() => {
      refreshIcon.style.transition = 'none';
      refreshIcon.style.transform = 'rotate(0deg)';
    }, 500);
  }

  // IP Filtering for LAN Valid Addresses
  function filterAndClassifyIPs(rawAddresses) {
    const v4 = [];
    const v6 = [];

    (rawAddresses || []).forEach(raw => {
      const ip = (raw || '').trim().replace(/^\[|\]$/g, '');
      if (!ip || ip.startsWith('127.') || ip === '::1' || ip.startsWith('169.254.')) {
        return;
      }

      if (ip.includes('.')) {
        // IPv4 validation
        const parts = ip.split('.').map(Number);
        if (parts.length !== 4 || parts.some(isNaN)) return;
        
        // Exclude network/broadcast (.0 or .255)
        if (parts[3] === 0 || parts[3] === 255) return;
        
        // Exclude Tailscale CGNAT 100.64.0.0/10
        if (parts[0] === 100 && (parts[1] >= 64 && parts[1] <= 127)) return;

        // Exclude WSL/Docker 172.17-31.x.x
        if (parts[0] === 172 && (parts[1] >= 17 && parts[1] <= 31)) return;

        // Exclude macOS bridge / VM adapters
        if (parts[0] === 192 && parts[1] === 168) {
          const third = parts[2];
          if ([56, 65, 97, 107, 117, 139, 147, 148, 156, 158, 215].includes(third)) {
            return;
          }
        }

        if (!v4.includes(ip)) v4.push(ip);
      } else if (ip.includes(':')) {
        // IPv6 validation
        const lower = ip.toLowerCase();
        // Exclude link-local fe80: and ULA fd... / fc... (Tailscale / virtual bridges)
        if (lower.startsWith('fe80:') || lower.startsWith('fd') || lower.startsWith('fc')) {
          return;
        }
        if (!v6.includes(ip)) v6.push(ip);
      }
    });

    return { ipv4: v4, ipv6: v6 };
  }

  let lastRenderedData = '';

  // Fetch Devices API
  async function fetchDevices() {
    if (isFetching) return;
    isFetching = true;

    try {
      const headers = {};
      if (joinToken) {
        headers['Authorization'] = `Bearer ${joinToken}`;
      }

      const res = await fetch('/api/v1/devices', { headers });
      if (res.status === 401) {
        liveStatusText.innerText = 'Token 未认证';
        addLog('WARN', 'API 返回 401 未授权，请点击右上角配置 Join Token');
        return;
      }
      if (!res.ok) {
        throw new Error(`HTTP ${res.status}`);
      }

      const data = await res.json();
      const newDevices = data.devices || [];
      const dataStr = JSON.stringify(newDevices);

      if (dataStr !== lastRenderedData) {
        if (devices.length > 0 && newDevices.length !== devices.length) {
          addLog('SUCCESS', `设备状态刷新：当前共 ${newDevices.length} 台设备`);
        }
        lastRenderedData = dataStr;
        devices = newDevices;
        updateStats();
        renderDevices();
      }

      liveStatusText.innerText = '实时在线';
    } catch (err) {
      liveStatusText.innerText = '连接中断';
      console.error('Fetch devices failed:', err);
    } finally {
      isFetching = false;
    }
  }

  function updateStats() {
    statTotal.innerText = devices.length;
    const syncedCount = devices.filter(d => d.sync_status === 'synced').length;
    const pendingCount = devices.filter(d => d.sync_status !== 'synced').length;
    
    statSynced.innerText = syncedCount;
    statPending.innerText = pendingCount;

    let maxVersion = 0;
    devices.forEach(d => {
      if (d.applied_version && d.applied_version > maxVersion) {
        maxVersion = d.applied_version;
      }
    });
    statVersion.innerText = maxVersion > 0 ? `v${maxVersion}` : '-';
  }

  function renderDevices() {
    const query = (searchInput.value || '').toLowerCase().trim();
    const filtered = devices.filter(d => {
      const matchAlias = (d.alias || '').toLowerCase().includes(query);
      const matchHost = (d.hostname || '').toLowerCase().includes(query);
      const matchId = (d.id || '').toLowerCase().includes(query);
      const matchIp = (d.addresses || []).some(ip => ip.toLowerCase().includes(query));
      return matchAlias || matchHost || matchId || matchIp;
    });

    deviceListCount.innerText = `${filtered.length} 台设备`;

    if (filtered.length === 0) {
      deviceContainer.innerHTML = `
        <div class="empty-state">
          <svg width="40" height="40" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" style="color: var(--text-muted);">
            <circle cx="11" cy="11" r="8"/>
            <line x1="21" y1="21" x2="16.65" y2="16.65"/>
          </svg>
          <p>${query ? '没有找到匹配的设备' : '当前暂无已接入的设备，请在终端执行右侧安装命令接入。'}</p>
        </div>
      `;
      return;
    }

    deviceContainer.innerHTML = filtered.map(d => createDeviceCardHTML(d)).join('');
  }

  function createDeviceCardHTML(d) {
    const os = (d.os || '').toLowerCase();
    let osIcon = '💻';
    let osName = d.os || 'Unknown';
    if (os.includes('darwin') || os.includes('mac')) {
      osIcon = '🍏';
      osName = 'macOS';
    } else if (os.includes('windows')) {
      osIcon = '🪟';
      osName = 'Windows';
    } else if (os.includes('openwrt')) {
      osIcon = '📡';
      osName = 'OpenWrt';
    } else if (os.includes('linux')) {
      osIcon = '🐧';
      osName = 'Linux';
    }

    const status = d.sync_status || 'pending';
    let statusClass = 'status-pending';
    let statusText = 'PENDING';
    if (status === 'synced') {
      statusClass = 'status-synced';
      statusText = 'SYNCED';
    } else if (status === 'error') {
      statusClass = 'status-error';
      statusText = 'ERROR';
    }

    const displayName = d.alias || d.hostname || d.id;

    // Filter LAN IPv4 & IPv6
    const { ipv4, ipv6 } = filterAndClassifyIPs(d.addresses);
    const mainIPv4 = ipv4[0] || '';
    const mainIPv6 = ipv6[0] || '';
    const mainTargetIP = mainIPv4 || mainIPv6 || (d.addresses && d.addresses[0]) || '127.0.0.1';

    const user = d.ssh_user || 'root';
    const port = d.ssh_port || 22;
    const sshCmd = port === 22 ? `ssh ${user}@${mainTargetIP}` : `ssh -p ${port} ${user}@${mainTargetIP}`;

    const version = d.applied_version ? `v${d.applied_version}` : '-';
    const hash = d.applied_hash ? d.applied_hash.slice(0, 8) : '-';
    const lastSeen = formatRelativeTime(d.last_seen_at || d.updated_at);

    return `
      <div class="device-card">
        <!-- Top Header -->
        <div class="device-card-header">
          <div class="device-host-info">
            <div class="os-icon" title="${osName} (${escapeHTML(d.arch || '')})">${osIcon}</div>
            <div class="device-title-area">
              <div class="device-title-row">
                <div class="device-hostname ${d.alias ? 'has-alias' : ''}" title="${escapeHTML(displayName)}">${escapeHTML(displayName)}</div>
                <button class="btn-rename-device" data-id="${escapeHTML(d.id)}" data-host="${escapeHTML(d.hostname || d.id)}" data-alias="${escapeHTML(d.alias || '')}" title="修改设备名称 / 别名">
                  <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                    <path d="M12 20h9"/>
                    <path d="M16.5 3.5a2.121 2.121 0 0 1 3 3L7 19l-4 1 1-4L16.5 3.5z"/>
                  </svg>
                </button>
              </div>
              <div class="device-id-tag" title="${escapeHTML(d.id)}">
                ${d.alias && d.hostname ? `<span class="device-orig-host" title="原始主机名: ${escapeHTML(d.hostname)}">${escapeHTML(d.hostname)}</span> • ` : ''}${escapeHTML(d.id)}
              </div>
            </div>
          </div>
          <span class="status-badge ${statusClass}">
            <span class="pulse-dot" style="width:6px;height:6px;background:currentColor;"></span>
            ${statusText}
          </span>
        </div>

        <!-- Body Details (Fixed Uniform Height) -->
        <div class="device-details">
          <!-- IPv4 Row -->
          <div class="detail-row">
            <span class="ip-type-badge badge-ipv4">IPv4</span>
            <div class="ip-display-box">
              ${mainIPv4 ? `
                <span class="ip-chip btn-copy-ip" data-ip="${escapeHTML(mainIPv4)}" title="点击复制 IPv4: ${escapeHTML(mainIPv4)}">${escapeHTML(mainIPv4)}</span>
                ${ipv4.length > 1 ? `<button class="ip-more-btn" data-type="IPv4" data-host="${escapeHTML(displayName)}" data-ips="${encodeURIComponent(JSON.stringify(ipv4))}" title="查看所有 ${ipv4.length} 个 IPv4">+${ipv4.length - 1}</button>` : ''}
              ` : `<span class="text-muted ip-none">无局域网 IPv4</span>`}
            </div>
          </div>

          <!-- IPv6 Row (Full IPv6 Address Displayed) -->
          <div class="detail-row">
            <span class="ip-type-badge badge-ipv6">IPv6</span>
            <div class="ip-display-box">
              ${mainIPv6 ? `
                <span class="ip-chip ip-v6-chip btn-copy-ip" data-ip="${escapeHTML(mainIPv6)}" title="点击复制 IPv6: ${escapeHTML(mainIPv6)}">${escapeHTML(mainIPv6)}</span>
                ${ipv6.length > 1 ? `<button class="ip-more-btn" data-type="IPv6" data-host="${escapeHTML(displayName)}" data-ips="${encodeURIComponent(JSON.stringify(ipv6))}" title="查看所有 ${ipv6.length} 个 IPv6">+${ipv6.length - 1}</button>` : ''}
              ` : `<span class="text-muted ip-none">无公网 IPv6</span>`}
            </div>
          </div>

          <!-- Architecture & OS -->
          <div class="detail-row">
            <span class="detail-label">系统架构</span>
            <span class="detail-value">${osName} • ${escapeHTML(d.arch || 'amd64')}</span>
          </div>

          <!-- Sync Version & Hash -->
          <div class="detail-row">
            <span class="detail-label">同步版本 / Hash</span>
            <span class="detail-value text-violet font-bold">${version} • ${hash}</span>
          </div>

          <!-- Last Heartbeat -->
          <div class="detail-row">
            <span class="detail-label">最近心跳</span>
            <span class="detail-value">${lastSeen}</span>
          </div>
        </div>

        <!-- Bottom Actions (Click-to-copy SSH Box & Delete) -->
        <div class="device-actions">
          <button class="btn-ssh-box" data-ssh="${escapeHTML(sshCmd)}" title="点击直接复制 SSH 命令">
            <span class="ssh-prompt">$</span>
            <span class="ssh-cmd-text">${escapeHTML(sshCmd)}</span>
            <span class="ssh-btn-text">
              <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <rect x="9" y="9" width="13" height="13" rx="2" ry="2"/>
                <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>
              </svg>
              复制
            </span>
          </button>
          <button class="btn-sync-device" data-id="${escapeHTML(d.id)}" data-host="${escapeHTML(displayName)}" title="同步此设备" aria-label="同步 ${escapeHTML(displayName)}">
            <svg class="sync-device-icon" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <path d="M21.5 2v6h-6M21.34 15.57a10 10 0 1 1-.57-8.38l5.67-5.67"/>
            </svg>
          </button>
          <button class="btn-del" data-id="${escapeHTML(d.id)}" data-host="${escapeHTML(displayName)}" title="移除此设备">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <polyline points="3 6 5 6 21 6"/>
              <path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/>
            </svg>
          </button>
        </div>
      </div>
    `;
  }

  async function syncDevice(button) {
    if (button.disabled) return;
    const id = button.dataset.id;
    const hostname = button.dataset.host || id;
    button.disabled = true;
    const icon = button.querySelector('.sync-device-icon');
    if (icon) icon.classList.add('spinning');

    try {
      const headers = {};
      if (joinToken) {
        headers['Authorization'] = `Bearer ${joinToken}`;
      }
      const res = await fetch(`/api/v1/devices/${encodeURIComponent(id)}/sync`, {
        method: 'POST',
        headers
      });
      if (res.status === 401) {
        showToast('认证失败，请先配置有效的 Join Token');
        openTokenModal();
        return;
      }
      if (!res.ok) {
        const text = await res.text();
        throw new Error(text || `HTTP ${res.status}`);
      }

      addLog('SUCCESS', `已触发单设备同步: ${hostname} (${id})`);
      showToast(`已触发同步: ${hostname}`);
      await fetchDevices();
    } catch (err) {
      showToast(`设备同步失败: ${err.message}`);
    } finally {
      button.disabled = false;
      if (icon) icon.classList.remove('spinning');
    }
  }

  async function deleteDevice(id, hostname) {
    if (!confirm(`确认要从 HomeAgent 控制平面移除设备 "${hostname}" (${id}) 吗？`)) {
      return;
    }

    try {
      const headers = {};
      if (joinToken) {
        headers['Authorization'] = `Bearer ${joinToken}`;
      }

      const res = await fetch(`/api/v1/devices/${encodeURIComponent(id)}`, {
        method: 'DELETE',
        headers
      });

      if (!res.ok) {
        throw new Error(`HTTP ${res.status}`);
      }

      addLog('WARN', `已删除设备: ${hostname} (${id})`);
      showToast(`已成功移除设备: ${hostname}`);
      fetchDevices();
    } catch (err) {
      alert(`删除设备失败: ${err.message}`);
    }
  }

  function updateInstallCommand() {
    const srv = serverHost || window.location.origin;
    const tok = joinToken || '<YOUR_JOIN_TOKEN>';

    let cmd = '';
    switch (activeOSTab) {
      case 'darwin':
      case 'linux':
      case 'openwrt':
        cmd = `curl -fsSL ${srv}/install.sh | HOMEAGENT_SERVER="${srv}" HOMEAGENT_JOIN_TOKEN="${tok}" sh`;
        break;
      case 'windows':
        cmd = `$env:HOMEAGENT_SERVER="${srv}"; $env:HOMEAGENT_JOIN_TOKEN="${tok}"; irm ${srv}/install.ps1 | iex`;
        break;
    }
    installCode.innerText = cmd;
  }

  function addLog(type, message) {
    const now = new Date();
    const timeStr = now.toTimeString().split(' ')[0];
    const item = document.createElement('div');
    item.className = 'log-item';

    let badgeClass = 'badge-info';
    if (type === 'SUCCESS') badgeClass = 'badge-success';
    if (type === 'WARN') badgeClass = 'badge-warn';

    item.innerHTML = `
      <span class="log-time">${timeStr}</span>
      <span class="log-badge ${badgeClass}">${type}</span>
      <span class="log-msg">${escapeHTML(message)}</span>
    `;

    eventLogContainer.prepend(item);
    // Keep max 30 items
    while (eventLogContainer.children.length > 30) {
      eventLogContainer.removeChild(eventLogContainer.lastChild);
    }
  }

  function copyToClipboard(text, msg) {
    if (!text) return;
    text = text.trim();

    if (navigator.clipboard && window.isSecureContext) {
      navigator.clipboard.writeText(text).then(() => {
        showToast(msg || '已复制到剪贴板');
      }).catch(() => {
        execFallbackCopy(text, msg);
      });
      return;
    }

    execFallbackCopy(text, msg);
  }

  function execFallbackCopy(text, msg) {
    const activeEl = document.activeElement;
    const scrollY = window.scrollY || window.pageYOffset;
    const scrollX = window.scrollX || window.pageXOffset;

    try {
      const textarea = document.createElement('textarea');
      textarea.value = text;
      textarea.setAttribute('readonly', '');
      textarea.style.position = 'fixed';
      textarea.style.top = '0';
      textarea.style.left = '0';
      textarea.style.width = '1px';
      textarea.style.height = '1px';
      textarea.style.padding = '0';
      textarea.style.border = 'none';
      textarea.style.outline = 'none';
      textarea.style.boxShadow = 'none';
      textarea.style.background = 'transparent';
      textarea.style.opacity = '0';
      textarea.style.pointerEvents = 'none';
      textarea.style.zIndex = '-9999';

      document.body.appendChild(textarea);
      textarea.select();
      textarea.setSelectionRange(0, textarea.value.length);
      document.execCommand('copy');
      document.body.removeChild(textarea);
      showToast(msg || '已复制到剪贴板');
    } catch (err) {
      console.warn('execCommand failed:', err);
      prompt('请按 Ctrl+C / Cmd+C 复制以下内容:', text);
    } finally {
      window.scrollTo(scrollX, scrollY);
      if (activeEl && typeof activeEl.focus === 'function') {
        activeEl.focus();
      }
    }
  }

  function showToast(msg) {
    toastMsg.innerText = msg;
    toast.classList.remove('hidden');
    clearTimeout(toast._timer);
    toast._timer = setTimeout(() => {
      toast.classList.add('hidden');
    }, 2400);
  }

  function formatRelativeTime(dateStr) {
    if (!dateStr || dateStr.startsWith('0001')) return '未同步';
    const date = new Date(dateStr);
    const now = new Date();
    const sec = Math.floor((now - date) / 1000);
    if (sec < 5) return '刚刚';
    if (sec < 60) return `${sec} 秒前`;
    const min = Math.floor(sec / 60);
    if (min < 60) return `${min} 分钟前`;
    const hr = Math.floor(min / 60);
    if (hr < 24) return `${hr} 小时前`;
    return `${Math.floor(hr / 24)} 天前`;
  }

  function escapeHTML(str) {
    return String(str)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#039;');
  }

  // Start app
  document.addEventListener('DOMContentLoaded', init);
})();
