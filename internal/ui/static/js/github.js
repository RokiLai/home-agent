import { state } from './state.js';
import { apiFetch } from './api.js';
import { escapeHTML, showToast, addLog } from './utils.js';
import { canOpenModal, requestOpenModal, requestCloseModal } from './modals.js';

export async function fetchGitHubStatus() {
  try {
    const res = await apiFetch(`${state.serverHost}/api/v1/github/status`);
    if (!res.ok) return;

    const data = await res.json();
    state.latestGitHubData = data;
    renderGitHubStatus(data);
    renderDashboardGitHubBrief(data);
  } catch (err) {
    console.error('Fetch GitHub status error:', err);
  }
}

export function renderGitHubStatus(data) {
  const githubLoading = document.getElementById('githubLoading');
  const githubConnected = document.getElementById('githubConnected');
  const githubDisconnected = document.getElementById('githubDisconnected');
  const githubUsername = document.getElementById('githubUsername');
  const githubTokenHint = document.getElementById('githubTokenHint');
  const githubSyncedCount = document.getElementById('githubSyncedCount');
  const githubTotalCount = document.getElementById('githubTotalCount');
  const githubAvatar = document.getElementById('githubAvatar');
  const githubAvatarFallback = document.getElementById('githubAvatarFallback');

  if (githubLoading) githubLoading.classList.add('hidden');

  if (data && data.connected && data.user) {
    if (githubConnected) githubConnected.classList.remove('hidden');
    if (githubDisconnected) githubDisconnected.classList.add('hidden');

    if (githubUsername) githubUsername.innerText = data.user.login || 'GitHub User';
    if (githubTokenHint) githubTokenHint.innerText = `Token: ${data.token_preview || 'ghp_****'}`;

    if (githubSyncedCount) githubSyncedCount.innerText = data.synced_devices_count !== undefined ? data.synced_devices_count : '0';
    if (githubTotalCount) githubTotalCount.innerText = data.total_devices_count !== undefined ? data.total_devices_count : state.devices.length;

    if (githubAvatar && githubAvatarFallback) {
      if (data.user.avatar_url) {
        githubAvatar.src = `${state.serverHost}/api/v1/github/avatar`;
        githubAvatar.classList.remove('hidden');
        githubAvatarFallback.classList.add('hidden');
      } else {
        githubAvatar.classList.add('hidden');
        githubAvatarFallback.classList.remove('hidden');
      }
    }
  } else {
    if (githubConnected) githubConnected.classList.add('hidden');
    if (githubDisconnected) githubDisconnected.classList.remove('hidden');
  }
}

export function renderDashboardGitHubBrief(data) {
  const dashboardGithubContent = document.getElementById('dashboardGithubContent');
  if (!dashboardGithubContent) return;

  if (data && data.connected && data.user) {
    dashboardGithubContent.innerHTML = `
      <div style="display:flex; align-items:center; gap:10px; padding: 6px 0;">
        <div style="display:flex; align-items:center; gap:8px;">
          <img src="${state.serverHost}/api/v1/github/avatar" alt="avatar" style="width:28px;height:28px;border-radius:50%;object-fit:cover;" onerror="this.style.display='none'">
          <span style="font-size:0.88rem;font-weight:600;">@${escapeHTML(data.user.login || '')}</span>
        </div>
        <span class="badge badge-success" style="font-size:0.7rem;">已绑定</span>
      </div>
    `;
  } else {
    dashboardGithubContent.innerHTML = `
      <p class="sidebar-card-desc" style="margin-bottom: 8px;">未绑定 GitHub 账号，无法自动分发 Git SSH 凭据与 CLI Token。</p>
      <a href="#/github" class="btn btn-primary btn-sm">前往授权 &rarr;</a>
    `;
  }
}

export function renderGitHubDeviceMatrix(onToggle) {
  const githubDeviceMatrix = document.getElementById('githubDeviceMatrix');
  if (!githubDeviceMatrix) return;
  githubDeviceMatrix.innerHTML = '';

  if (state.devices.length === 0) {
    githubDeviceMatrix.innerHTML = '<div class="text-muted" style="font-size:0.85rem;padding:10px 0;">暂无可授权的已接入设备</div>';
    return;
  }

  state.devices.forEach(d => {
    const item = document.createElement('div');
    item.className = 'github-device-item';
    const devName = d.alias ? `${d.alias} (${d.hostname || d.id})` : (d.hostname || d.id);
    const isEnabled = !!d.github_sync_enabled;

    item.innerHTML = `
      <div class="github-device-left">
        <div>
          <div class="github-device-name">${escapeHTML(devName)}</div>
          <div class="github-device-id">${escapeHTML(d.id)}</div>
        </div>
      </div>
      <div class="github-device-right">
        <label class="github-sync-toggle">
          <input type="checkbox" class="github-sync-checkbox" data-id="${escapeHTML(d.id)}" ${isEnabled ? 'checked' : ''}>
          <span>同步凭据</span>
        </label>
      </div>
    `;

    item.querySelector('.github-sync-checkbox').addEventListener('change', (e) => {
      if (onToggle) {
        onToggle(d.id, e.target.checked);
      } else {
        handleToggleGitHubSync(d.id, e.target.checked);
      }
    });

    githubDeviceMatrix.appendChild(item);
  });
}

export async function handleToggleGitHubSync(deviceID, enabled, onRefresh) {
  try {
    const res = await apiFetch(`${state.serverHost}/api/v1/devices/${encodeURIComponent(deviceID)}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ github_sync_enabled: enabled })
    });

    if (!res.ok) {
      const errText = await res.text();
      throw new Error(errText || `HTTP ${res.status}`);
    }

    showToast(`已${enabled ? '启用' : '禁用'}设备 GitHub 凭据同步`);
    addLog('success', `设备 [${deviceID}] GitHub 权限更新成功`);
    if (onRefresh) {
      await onRefresh();
    }
  } catch (err) {
    console.error('Failed to toggle github sync:', err);
    showToast(`设置失败: ${err.message}`);
    addLog('warn', `更新 GitHub 权限失败: ${err.message}`);
  }
}

export async function startGitHubDeviceFlow(onSuccess) {
  const btnGithubLogin = document.getElementById('btnGithubLogin');
  if (!canOpenModal('githubDeviceModal')) return;

  const githubUserCodeDisplay = document.getElementById('githubUserCodeDisplay');
  const githubVerifyLink = document.getElementById('githubVerifyLink');

  if (btnGithubLogin) btnGithubLogin.disabled = true;
  addLog('info', '正在向 GitHub 发起 Device Flow 授权请求...');

  try {
    const res = await apiFetch(`${state.serverHost}/api/v1/github/auth/device-code`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' }
    });

    if (!res.ok) {
      const errText = await res.text();
      throw new Error(errText || `HTTP ${res.status}`);
    }

    const data = await res.json();
    if (githubUserCodeDisplay) githubUserCodeDisplay.innerText = data.user_code || '-';
    if (githubVerifyLink && data.verification_uri) {
      githubVerifyLink.href = data.verification_uri;
    }
    const opened = requestOpenModal('githubDeviceModal', btnGithubLogin);
    if (!opened) {
      addLog('warn', 'GitHub 授权弹窗打开被拦截（已有活动操作），已终止本次授权流程');
      return;
    }

    pollGitHubAuth(onSuccess);
  } catch (err) {
    console.error('Failed to start GitHub Device Flow:', err);
    showToast(`发起 GitHub 授权失败: ${err.message}`);
    addLog('warn', `GitHub 授权请求失败: ${err.message}`);
  } finally {
    if (btnGithubLogin) btnGithubLogin.disabled = false;
  }
}

export function closeGithubDeviceModal() {
  requestCloseModal('githubDeviceModal');
}

export async function pollGitHubAuth(onSuccess) {
  const githubDeviceModal = document.getElementById('githubDeviceModal');
  let attempts = 0;
  const maxAttempts = 60;
  const interval = setInterval(async () => {
    attempts++;
    if (attempts > maxAttempts || (githubDeviceModal && githubDeviceModal.classList.contains('hidden'))) {
      clearInterval(interval);
      return;
    }

    try {
      const res = await apiFetch(`${state.serverHost}/api/v1/github/status`);
      if (res.ok) {
        const data = await res.json();
        if (data && data.connected) {
          clearInterval(interval);
          closeGithubDeviceModal();
          showToast(`GitHub 账号 @${data.user?.login || ''} 绑定成功！`);
          addLog('success', `GitHub 授权完成，当前绑定账号: @${data.user?.login || ''}`);
          if (onSuccess) {
            await onSuccess();
          }
        }
      }
    } catch (e) {
      // Continue polling
    }
  }, 3000);
}

export async function handleDisconnectGitHub(onRefresh) {
  const btnGithubDisconnect = document.getElementById('btnGithubDisconnect');
  if (!confirm('确定要解绑当前的 GitHub 账号吗？解绑后设备将不再同步该账号的 SSH 密钥和 CLI Token。')) {
    return;
  }

  if (btnGithubDisconnect) btnGithubDisconnect.disabled = true;
  addLog('info', '正在解绑 GitHub 账号...');

  try {
    const res = await apiFetch(`${state.serverHost}/api/v1/github/disconnect`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' }
    });

    if (!res.ok) {
      throw new Error(`HTTP ${res.status}`);
    }

    showToast('GitHub 账号已成功解绑');
    addLog('success', 'GitHub 账号解绑成功');
    if (onRefresh) {
      await onRefresh();
    }
  } catch (err) {
    console.error('Failed to disconnect GitHub:', err);
    showToast(`解绑失败: ${err.message}`);
    addLog('warn', `解绑 GitHub 失败: ${err.message}`);
  } finally {
    if (btnGithubDisconnect) btnGithubDisconnect.disabled = false;
  }
}
