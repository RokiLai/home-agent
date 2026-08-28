import { state } from './state.js';
import { apiFetch, setAuthFailureHandler } from './api.js';
import { showToast, addLog } from './utils.js';

export function showLoginOverlay() {
  const loginOverlay = document.getElementById('loginOverlay');
  const sidebarTokenStatus = document.getElementById('sidebarTokenStatus');
  if (loginOverlay) loginOverlay.classList.remove('hidden');
  if (sidebarTokenStatus) {
    sidebarTokenStatus.innerText = '未登录';
    sidebarTokenStatus.className = 'font-mono text-amber';
  }
}

export function hideLoginOverlay() {
  const loginOverlay = document.getElementById('loginOverlay');
  const loginError = document.getElementById('loginError');
  if (loginOverlay) loginOverlay.classList.add('hidden');
  if (loginError) {
    loginError.classList.add('hidden');
    loginError.innerText = '';
  }
}

export function showLoginError(msg) {
  const loginError = document.getElementById('loginError');
  if (loginError) {
    loginError.innerText = msg;
    loginError.classList.remove('hidden');
  }
}

export async function checkAuthStatus() {
  const adminUsernameDisplay = document.getElementById('adminUsernameDisplay');
  const sidebarTokenStatus = document.getElementById('sidebarTokenStatus');
  try {
    const res = await fetch(`${state.serverHost}/api/v1/auth/me`, { credentials: 'same-origin' });
    if (res.ok) {
      const data = await res.json();
      state.isAuthenticated = true;
      if (data.public_url) {
        state.publicUrl = data.public_url;
      }
      hideLoginOverlay();
      if (adminUsernameDisplay && data.username) {
        adminUsernameDisplay.innerText = data.username;
      }
      if (sidebarTokenStatus) {
        sidebarTokenStatus.innerText = '已登录';
        sidebarTokenStatus.className = 'font-mono text-emerald';
      }
      return true;
    }
  } catch (e) {
    console.warn('Check auth status failed:', e);
  }
  state.isAuthenticated = false;
  showLoginOverlay();
  return false;
}

export async function handleLogin(e, onLoginSuccess) {
  if (e) e.preventDefault();
  const loginUsername = document.getElementById('loginUsername');
  const loginPassword = document.getElementById('loginPassword');
  const loginRememberMe = document.getElementById('loginRememberMe');
  const loginSubmitBtn = document.getElementById('loginSubmitBtn');
  const adminUsernameDisplay = document.getElementById('adminUsernameDisplay');
  const sidebarTokenStatus = document.getElementById('sidebarTokenStatus');

  const username = loginUsername ? loginUsername.value.trim() : '';
  const password = loginPassword ? loginPassword.value : '';
  const rememberMe = loginRememberMe ? loginRememberMe.checked : true;

  if (!username || !password) {
    showLoginError('请输入账号和密码');
    return;
  }

  if (loginSubmitBtn) {
    loginSubmitBtn.disabled = true;
    loginSubmitBtn.innerText = '正在登入...';
  }

  try {
    const res = await fetch(`${state.serverHost}/api/v1/auth/login`, {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username, password, remember_me: rememberMe })
    });

    if (!res.ok) {
      let errText = '登录失败，请检查账号密码';
      try {
        const errJson = await res.json();
        if (errJson.error) errText = errJson.error;
      } catch (_) {}
      showLoginError(errText);
      return;
    }

    const data = await res.json();
    state.isAuthenticated = true;
    hideLoginOverlay();
    if (adminUsernameDisplay) {
      adminUsernameDisplay.innerText = data.username || username;
    }
    if (sidebarTokenStatus) {
      sidebarTokenStatus.innerText = '已登录';
      sidebarTokenStatus.className = 'font-mono text-emerald';
    }
    showToast('登录成功');
    addLog('success', `管理员 [${username}] 成功登入控制台`);
    if (onLoginSuccess) {
      await onLoginSuccess();
    }
  } catch (err) {
    showLoginError(`登录异常: ${err.message}`);
  } finally {
    if (loginSubmitBtn) {
      loginSubmitBtn.disabled = false;
      loginSubmitBtn.innerText = '登入控制台';
    }
  }
}

export async function handleLogout() {
  try {
    await fetch(`${state.serverHost}/api/v1/auth/logout`, {
      method: 'POST',
      credentials: 'same-origin'
    });
  } catch (_) {}
  state.isAuthenticated = false;
  showLoginOverlay();
  showToast('已安全登出');
  addLog('info', '管理员已退出登录');
}

export async function handleChangePassword(e) {
  if (e) e.preventDefault();
  const oldPasswordInput = document.getElementById('oldPasswordInput');
  const newPasswordInput = document.getElementById('newPasswordInput');
  const confirmPasswordInput = document.getElementById('confirmPasswordInput');
  const changePasswordAlert = document.getElementById('changePasswordAlert');
  const savePasswordBtn = document.getElementById('savePasswordBtn');
  const changePasswordForm = document.getElementById('changePasswordForm');

  if (!oldPasswordInput || !newPasswordInput || !confirmPasswordInput) return;

  const oldPass = oldPasswordInput.value;
  const newPass = newPasswordInput.value;
  const confirmPass = confirmPasswordInput.value;

  if (changePasswordAlert) {
    changePasswordAlert.classList.add('hidden');
    changePasswordAlert.innerText = '';
  }

  if (!oldPass || !newPass) {
    showChangePasswordError('请输入当前旧密码和新密码');
    return;
  }

  if (newPass.length < 6) {
    showChangePasswordError('新密码长度不能少于 6 位');
    return;
  }

  if (newPass !== confirmPass) {
    showChangePasswordError('两次输入的新密码不一致，请检查');
    return;
  }

  if (savePasswordBtn) savePasswordBtn.disabled = true;

  try {
    const resp = await apiFetch(`${state.serverHost}/api/v1/auth/password`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        old_password: oldPass,
        new_password: newPass
      })
    });

    const data = await resp.json();
    if (!resp.ok || !data.success) {
      showChangePasswordError(data.message || '修改密码失败，请检查旧密码是否正确');
      return;
    }

    showToast('管理员密码修改成功');
    addLog('success', '管理员登录密码已成功更新');
    if (changePasswordForm) changePasswordForm.reset();
  } catch (err) {
    showChangePasswordError(err.message || '网络通信异常，请重试');
  } finally {
    if (savePasswordBtn) savePasswordBtn.disabled = false;
  }
}

export function showChangePasswordError(msg) {
  const changePasswordAlert = document.getElementById('changePasswordAlert');
  if (changePasswordAlert) {
    changePasswordAlert.innerText = msg;
    changePasswordAlert.classList.remove('hidden');
  }
}

setAuthFailureHandler(showLoginOverlay);
