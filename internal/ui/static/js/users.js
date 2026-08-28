import { state } from './state.js';
import { apiFetch } from './api.js';
import { showToast, addLog, escapeHTML } from './utils.js';

export async function fetchUsersList() {
  const usersTableBody = document.getElementById('usersTableBody');
  if (!usersTableBody) return;

  try {
    const res = await apiFetch(`${state.serverHost}/api/v1/users`);
    if (!res.ok) {
      console.warn('Fetch users failed:', res.status);
      return;
    }
    const data = await res.json();
    state.usersList = data.users || [];
    renderUsersTable(state.usersList);
  } catch (err) {
    console.error('Fetch users error:', err);
  }
}

export function renderUsersTable(users) {
  const tbody = document.getElementById('usersTableBody');
  if (!tbody) return;

  if (!users || users.length === 0) {
    tbody.innerHTML = `<tr><td colspan="5" class="text-center text-muted py-6">暂无其他用户</td></tr>`;
    return;
  }

  const isCurrentOwner = state.currentUser && state.currentUser.role === 'owner';

  tbody.innerHTML = users.map(u => {
    const isSelf = state.currentUser && state.currentUser.id === u.id;
    const roleBadgeClass = u.role === 'owner' ? 'badge-owner' : (u.role === 'admin' ? 'badge-admin' : 'badge-viewer');
    const roleText = u.role === 'owner' ? '所有者' : (u.role === 'admin' ? '管理员' : '只读访客');
    const statusClass = u.status === 'active' ? 'text-emerald' : 'text-rose';
    const statusText = u.status === 'active' ? '正常活跃' : '已禁用';
    const lastLogin = u.last_login_at ? new Date(u.last_login_at).toLocaleString() : '从未使用';

    let actionButtons = '';
    if (isCurrentOwner && !isSelf) {
      const toggleActionText = u.status === 'active' ? '禁用' : '启用';
      const toggleActionClass = u.status === 'active' ? 'btn-danger-ghost' : 'btn-success-ghost';
      actionButtons = `
        <div class="table-actions">
          <button class="btn btn-xs btn-secondary" onclick="window.openResetUserPasswordModal('${u.id}', '${escapeHTML(u.username)}')">重置密码</button>
          <select class="select-role-inline" onchange="window.handleRoleChange('${u.id}', this.value)">
            <option value="owner" ${u.role === 'owner' ? 'selected' : ''}>所有者</option>
            <option value="admin" ${u.role === 'admin' ? 'selected' : ''}>管理员</option>
            <option value="viewer" ${u.role === 'viewer' ? 'selected' : ''}>只读访客</option>
          </select>
          <button class="btn btn-xs ${toggleActionClass}" onclick="window.handleToggleUserStatus('${u.id}', '${u.status}')">${toggleActionText}</button>
          <button class="btn btn-xs btn-danger-ghost" onclick="window.handleDeleteUser('${u.id}', '${escapeHTML(u.username)}')">删除</button>
        </div>
      `;
    } else if (isSelf) {
      actionButtons = `<span class="text-muted text-xs">当前登录账号</span>`;
    } else {
      actionButtons = `<span class="text-muted text-xs">只读权限</span>`;
    }

    return `
      <tr>
        <td class="font-medium">${escapeHTML(u.username)}</td>
        <td><span class="badge ${roleBadgeClass}">${roleText}</span></td>
        <td><span class="${statusClass} font-mono text-xs">${statusText}</span></td>
        <td class="text-muted text-xs">${lastLogin}</td>
        <td>${actionButtons}</td>
      </tr>
    `;
  }).join('');
}

export function openCreateUserModal() {
  const modal = document.getElementById('createUserModal');
  const form = document.getElementById('createUserForm');
  const alert = document.getElementById('createUserAlert');
  if (form) form.reset();
  if (alert) {
    alert.classList.add('hidden');
    alert.innerText = '';
  }
  if (modal) modal.classList.remove('hidden');
}

export function closeCreateUserModal() {
  const modal = document.getElementById('createUserModal');
  if (modal) modal.classList.add('hidden');
}

export async function handleCreateUserSubmit(e) {
  if (e) e.preventDefault();
  const usernameInput = document.getElementById('newUserNameInput');
  const passwordInput = document.getElementById('newUserPasswordInput');
  const roleSelect = document.getElementById('newUserRoleSelect');
  const alert = document.getElementById('createUserAlert');
  const btn = document.getElementById('btnSubmitCreateUser');

  const username = usernameInput ? usernameInput.value.trim() : '';
  const password = passwordInput ? passwordInput.value : '';
  const role = roleSelect ? roleSelect.value : 'admin';

  if (!username || !password) {
    if (alert) {
      alert.innerText = '请输入用户名和初始密码';
      alert.classList.remove('hidden');
    }
    return;
  }

  if (btn) btn.disabled = true;

  try {
    const res = await apiFetch(`${state.serverHost}/api/v1/users`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username, password, role })
    });

    if (!res.ok) {
      let msg = '创建用户失败';
      try {
        const errJson = await res.json();
        if (errJson.message) msg = errJson.message;
      } catch (_) {}
      if (alert) {
        alert.innerText = msg;
        alert.classList.remove('hidden');
      }
      return;
    }

    closeCreateUserModal();
    showToast(`用户 [${username}] 创建成功`);
    addLog('success', `成功创建用户 [${username}], 角色: ${role}`);
    await fetchUsersList();
  } catch (err) {
    if (alert) {
      alert.innerText = err.message || '网络异常';
      alert.classList.remove('hidden');
    }
  } finally {
    if (btn) btn.disabled = false;
  }
}

export async function handleRoleChange(userId, newRole) {
  try {
    const res = await apiFetch(`${state.serverHost}/api/v1/users/${userId}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ role: newRole })
    });

    if (!res.ok) {
      showToast('修改角色失败');
      await fetchUsersList();
      return;
    }

    showToast('用户角色已更新');
    addLog('info', `用户角色已更新为: ${newRole}`);
    await fetchUsersList();
  } catch (err) {
    showToast(`修改角色异常: ${err.message}`);
  }
}

export async function handleToggleUserStatus(userId, currentStatus) {
  const targetAction = currentStatus === 'active' ? 'disable' : 'enable';
  try {
    const res = await apiFetch(`${state.serverHost}/api/v1/users/${userId}/${targetAction}`, {
      method: 'POST'
    });

    if (!res.ok) {
      showToast('操作失败');
      return;
    }

    showToast(`用户已成功${targetAction === 'disable' ? '禁用' : '启用'}`);
    await fetchUsersList();
  } catch (err) {
    showToast(`操作异常: ${err.message}`);
  }
}

let currentResetUserId = '';

export function openResetUserPasswordModal(userId, username) {
  currentResetUserId = userId;
  const modal = document.getElementById('resetUserPasswordModal');
  const targetDisplay = document.getElementById('resetPasswordUsernameDisplay');
  const input = document.getElementById('resetUserNewPasswordInput');
  const alert = document.getElementById('resetUserPasswordAlert');
  if (targetDisplay) targetDisplay.innerText = username;
  if (input) input.value = '';
  if (alert) {
    alert.classList.add('hidden');
    alert.innerText = '';
  }
  if (modal) modal.classList.remove('hidden');
}

export function closeResetUserPasswordModal() {
  const modal = document.getElementById('resetUserPasswordModal');
  if (modal) modal.classList.add('hidden');
  currentResetUserId = '';
}

export async function handleResetUserPasswordSubmit(e) {
  if (e) e.preventDefault();
  if (!currentResetUserId) return;
  const input = document.getElementById('resetUserNewPasswordInput');
  const alert = document.getElementById('resetUserPasswordAlert');
  const newPassword = input ? input.value : '';

  if (!newPassword || newPassword.length < 6) {
    if (alert) {
      alert.innerText = '新密码长度不能少于 6 位';
      alert.classList.remove('hidden');
    }
    return;
  }

  try {
    const res = await apiFetch(`${state.serverHost}/api/v1/users/${currentResetUserId}/password-reset`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ new_password: newPassword })
    });

    if (!res.ok) {
      if (alert) {
        alert.innerText = '重置密码失败';
        alert.classList.remove('hidden');
      }
      return;
    }

    closeResetUserPasswordModal();
    showToast('用户密码已成功重置');
    addLog('info', '管理员已成功重置指定用户的登录密码');
  } catch (err) {
    if (alert) {
      alert.innerText = err.message || '网络异常';
      alert.classList.remove('hidden');
    }
  }
}

export async function handleDeleteUser(userId, username) {
  if (!confirm(`警告：删除用户 [${username}] 将永久级联物理删除该用户所拥有的全部设备及授权记录！\n\n此操作不可逆，是否确认删除？`)) {
    return;
  }

  try {
    const res = await apiFetch(`${state.serverHost}/api/v1/users/${userId}`, {
      method: 'DELETE'
    });

    if (!res.ok) {
      showToast('删除用户失败');
      return;
    }

    showToast(`用户 [${username}] 及其关联设备已成功删除`);
    addLog('warn', `用户 [${username}] 及其名下设备已级联物理删除`);
    await fetchUsersList();
  } catch (err) {
    showToast(`删除异常: ${err.message}`);
  }
}

// 暴露给全局 window 供 HTML inline 事件使用
if (typeof window !== 'undefined') {
  window.openCreateUserModal = openCreateUserModal;
  window.closeCreateUserModal = closeCreateUserModal;
  window.handleCreateUserSubmit = handleCreateUserSubmit;
  window.handleRoleChange = handleRoleChange;
  window.handleToggleUserStatus = handleToggleUserStatus;
  window.openResetUserPasswordModal = openResetUserPasswordModal;
  window.closeResetUserPasswordModal = closeResetUserPasswordModal;
  window.handleResetUserPasswordSubmit = handleResetUserPasswordSubmit;
  window.handleDeleteUser = handleDeleteUser;
}
