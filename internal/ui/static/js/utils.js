export function escapeHTML(str) {
  if (!str) return '';
  return String(str)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#039;');
}

export function formatRelativeTime(dateStr) {
  if (!dateStr) return '-';
  const date = new Date(dateStr);
  if (isNaN(date.getTime())) return '-';
  const diff = Math.floor((Date.now() - date.getTime()) / 1000);
  if (diff < 5) return '刚刚';
  if (diff < 60) return `${diff} 秒前`;
  if (diff < 3600) return `${Math.floor(diff / 60)} 分钟前`;
  if (diff < 86400) return `${Math.floor(diff / 3600)} 小时前`;
  return `${Math.floor(diff / 86400)} 天前`;
}

export function getOSInfo(osStr) {
  const s = (osStr || '').toLowerCase();
  if (s.includes('darwin') || s.includes('mac')) return { icon: '🍏', name: 'macOS' };
  if (s.includes('windows')) return { icon: '🪟', name: 'Windows' };
  if (s.includes('openwrt')) return { icon: '📡', name: 'OpenWrt' };
  if (s.includes('linux')) return { icon: '🐧', name: 'Linux' };
  if (s.includes('freebsd')) return { icon: '😈', name: 'FreeBSD' };
  return { icon: '💻', name: osStr || 'Unknown' };
}

export function getOSIcon(osStr) {
  return getOSInfo(osStr).icon;
}

export function filterAndClassifyIPs(rawAddresses) {
  if (!rawAddresses || !Array.isArray(rawAddresses)) {
    return { ipv4: [], ipv6: [] };
  }
  const ipv4 = [];
  const ipv6 = [];

  rawAddresses.forEach(item => {
    if (!item) return;
    let ip = '';
    if (typeof item === 'string') {
      ip = item;
    } else if (typeof item === 'object' && item.ip) {
      ip = item.ip;
    }
    ip = ip.trim();
    if (!ip) return;

    if (ip.includes(':')) {
      const lower = ip.toLowerCase();
      if (lower === '::1' || lower.startsWith('fe80:') || lower.startsWith('fd') || lower.startsWith('fc')) {
        return;
      }
      ipv6.push(ip);
    } else if (ip.includes('.')) {
      if (ip === '127.0.0.1' || ip.startsWith('169.254.')) {
        return;
      }
      ipv4.push(ip);
    }
  });

  return { ipv4, ipv6 };
}

let toastTimer = null;
export function showToast(msg) {
  const toast = document.getElementById('toast');
  const toastMsg = document.getElementById('toastMsg');
  if (!toast || !toastMsg) return;
  toastMsg.innerText = msg;
  toast.classList.remove('hidden');
  if (toastTimer) clearTimeout(toastTimer);
  toastTimer = setTimeout(() => {
    toast.classList.add('hidden');
  }, 2400);
}

export function copyToClipboard(text, msg) {
  if (!text) return;
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(text).then(() => {
      showToast(msg || '已复制到剪贴板');
    }).catch(() => {
      execFallbackCopy(text, msg);
    });
  } else {
    execFallbackCopy(text, msg);
  }
}

export function execFallbackCopy(text, msg) {
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.style.position = 'fixed';
  ta.style.opacity = '0';
  document.body.appendChild(ta);
  ta.focus();
  ta.select();
  try {
    document.execCommand('copy');
    showToast(msg || '已复制到剪贴板');
  } catch (err) {
    showToast('复制失败，请手动选择复制');
  }
  document.body.removeChild(ta);
}

export function addLog(type, message) {
  const eventLogContainer = document.getElementById('eventLogContainer');
  if (!eventLogContainer) return;
  const now = new Date();
  const timeStr = now.toTimeString().split(' ')[0];
  const item = document.createElement('div');
  item.className = 'log-item';

  let badgeClass = 'badge-info';
  let badgeText = 'INFO';
  if (type === 'success') { badgeClass = 'badge-success'; badgeText = 'OK'; }
  if (type === 'warn' || type === 'error') { badgeClass = 'badge-warn'; badgeText = 'WARN'; }

  item.innerHTML = `
    <span class="log-time">${timeStr}</span>
    <span class="log-badge ${badgeClass}">${badgeText}</span>
    <span class="log-msg">${escapeHTML(message)}</span>
  `;

  eventLogContainer.insertBefore(item, eventLogContainer.firstChild);
  while (eventLogContainer.children.length > 50) {
    eventLogContainer.removeChild(eventLogContainer.lastChild);
  }
}

export function spinRefresh() {
  const refreshIcon = document.getElementById('refreshIcon');
  if (refreshIcon) {
    refreshIcon.classList.add('spinning');
    setTimeout(() => {
      refreshIcon.classList.remove('spinning');
    }, 600);
  }
}
