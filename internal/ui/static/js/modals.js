import { state } from './state.js';
import { apiFetch } from './api.js';
import { escapeHTML, copyToClipboard, showToast, addLog } from './utils.js';

let activeModalId = null;
let lastActiveElement = null;

export function isAnyModalOpen() {
  return Boolean(activeModalId);
}

export function getActiveModalId() {
  return activeModalId;
}

export function canOpenModal(modalId) {
  if (activeModalId && activeModalId !== modalId) {
    showToast('请先完成或关闭当前操作');
    return false;
  }
  return true;
}

export function requestOpenModal(modalId, triggerEl) {
  if (!canOpenModal(modalId)) {
    return false;
  }

  const modal = document.getElementById(modalId);
  if (!modal) return false;

  activeModalId = modalId;
  lastActiveElement = triggerEl || document.activeElement;
  if (document.body && document.body.classList) {
    document.body.classList.add('modal-open');
  }
  modal.classList.remove('hidden');
  return true;
}

export function requestCloseModal(modalId) {
  const targetId = modalId || activeModalId;
  if (!targetId) return;

  const modal = document.getElementById(targetId);
  if (modal) {
    modal.classList.add('hidden');
  }

  if (activeModalId === targetId) {
    activeModalId = null;
    if (document.body && document.body.classList) {
      document.body.classList.remove('modal-open');
    }

    const hasBodyContains = document.body && typeof document.body.contains === 'function';
    if (lastActiveElement && typeof lastActiveElement.focus === 'function' && (!hasBodyContains || document.body.contains(lastActiveElement))) {
      try {
        lastActiveElement.focus();
      } catch (_) {}
    } else {
      const pageTitle = document.getElementById('currentPageTitle');
      if (pageTitle && typeof pageTitle.focus === 'function') {
        try {
          pageTitle.focus();
        } catch (_) {}
      }
    }
    lastActiveElement = null;
  }
}

export function closeActiveModal() {
  if (!activeModalId) return;
  if (activeModalId === 'renameModal') {
    closeRenameModal();
  } else if (activeModalId === 'ipModal') {
    closeIpModal();
  } else {
    requestCloseModal(activeModalId);
  }
}

export function closeAllModals() {
  closeActiveModal();
  ['ipModal', 'renameModal', 'githubDeviceModal', 'deviceHealthModal'].forEach(id => {
    const el = document.getElementById(id);
    if (el) el.classList.add('hidden');
  });
  state.currentRenameDeviceId = '';
  activeModalId = null;
  if (document.body && document.body.classList) {
    document.body.classList.remove('modal-open');
  }
  lastActiveElement = null;
}

export function openRenameModal(deviceId, hostname, alias, mac, triggerEl) {
  if (!requestOpenModal('renameModal', triggerEl)) return;

  state.currentRenameDeviceId = deviceId;
  const renameDeviceInfo = document.getElementById('renameDeviceInfo');
  const renameDeviceMac = document.getElementById('renameDeviceMac');
  const deviceAliasInput = document.getElementById('deviceAliasInput');

  if (renameDeviceInfo) {
    renameDeviceInfo.innerText = `${hostname} (${deviceId})`;
  }
  if (renameDeviceMac) {
    renameDeviceMac.innerText = mac || '未上报';
  }
  if (deviceAliasInput) {
    deviceAliasInput.value = alias || '';
    setTimeout(() => {
      if (deviceAliasInput) deviceAliasInput.focus();
    }, 50);
  }
}

export function closeRenameModal() {
  state.currentRenameDeviceId = '';
  requestCloseModal('renameModal');
}

export async function saveRenameDevice(onSaved) {
  if (!state.currentRenameDeviceId) return;
  const deviceAliasInput = document.getElementById('deviceAliasInput');
  const saveRenameBtn = document.getElementById('saveRenameBtn');
  const newAlias = deviceAliasInput ? deviceAliasInput.value.trim() : '';

  if (saveRenameBtn) {
    saveRenameBtn.disabled = true;
    saveRenameBtn.innerText = '保存中...';
  }

  try {
    const res = await apiFetch(`${state.serverHost}/api/v1/devices/${encodeURIComponent(state.currentRenameDeviceId)}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ alias: newAlias })
    });

    if (!res.ok) {
      const errText = await res.text();
      throw new Error(errText || `HTTP ${res.status}`);
    }

    showToast(newAlias ? `已设置别名为 "${newAlias}"` : '已清空设备别名');
    addLog('success', `更新设备 [${state.currentRenameDeviceId}] 备注名: ${newAlias || '(无)'}`);
    closeRenameModal();
    if (onSaved) {
      await onSaved();
    }
  } catch (err) {
    console.error('Failed to update device alias:', err);
    showToast(`保存失败: ${err.message}`);
    addLog('warn', `更新设备备注失败: ${err.message}`);
  } finally {
    if (saveRenameBtn) {
      saveRenameBtn.disabled = false;
      saveRenameBtn.innerText = '保存配置';
    }
  }
}

export function openIpModal(host, ipType, ips, triggerEl) {
  if (!requestOpenModal('ipModal', triggerEl)) return;

  const ipModalTitle = document.getElementById('ipModalTitle');
  const ipModalDesc = document.getElementById('ipModalDesc');
  const ipModalList = document.getElementById('ipModalList');

  if (ipModalTitle) ipModalTitle.innerText = `${host} - ${ipType} 地址列表`;
  if (ipModalDesc) ipModalDesc.innerText = `该设备上报的全部 ${ipType} 地址，点击即可一键复制：`;
  if (ipModalList) {
    ipModalList.innerHTML = '';
    ips.forEach(ip => {
      const item = document.createElement('div');
      item.className = 'ip-modal-item';
      item.innerHTML = `
        <span class="ip-modal-text">${escapeHTML(ip)}</span>
        <button class="btn btn-copy" style="flex-shrink:0;">复制</button>
      `;
      item.querySelector('.btn-copy').addEventListener('click', () => {
        copyToClipboard(ip, `${ipType} 地址已复制`);
      });
      ipModalList.appendChild(item);
    });
  }
}

export function closeIpModal() {
  requestCloseModal('ipModal');
}

export function closeAllDropdowns() {
  state.openDropdownDevID = null;
  document.querySelectorAll('.device-dropdown-menu.is-open').forEach(menu => {
    menu.classList.remove('is-open');
  });
  document.querySelectorAll('.btn-more-actions[aria-expanded="true"]').forEach(btn => {
    btn.setAttribute('aria-expanded', 'false');
  });
}
