import { state, sanitizeHost } from './state.js';
import { showToast, addLog } from './utils.js';
import { updateInstallCommand } from './onboarding.js';

export function initSettingsForm() {
  const settingsServerUrlInput = document.getElementById('settingsServerUrlInput');
  if (settingsServerUrlInput) {
    settingsServerUrlInput.value = localStorage.getItem('homeagent_server_url') || '';
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
