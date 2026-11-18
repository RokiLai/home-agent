import { apiFetch } from './api.js';
import { state } from './state.js';

const modes = {
  unconfigured: '尚未配置采集节点', waiting: '等待采集结果', monitoring: '监控运行中',
  paused: '监控已暂停', stale: '数据已过期', collection_failed: '采集失败'
};
const labels = {
  lan: '局域网', wifi: '无线网络', resources: '系统资源', wan: '公网连接', dhcp: 'DHCP',
  ipv6: 'IPv6', shell: '代理核心', dns: 'DNS', proxy: '代理连通性',
  learner: 'IP 学习', learned_sets: 'IPv4/IPv6 HTTPS 学习分流'
};
const statuses = { healthy: '正常', failed: '异常', skipped: '跳过', unknown: '未知' };
let revision = 0;

export async function openRouterMonitor() {
  const status = document.getElementById('routerMonitorStatus');
  if (!status) return;
  const updated = document.getElementById('routerMonitorUpdated');
  const checks = document.getElementById('routerMonitorChecks');
  const refresh = document.getElementById('routerMonitorRefresh');
  const request = ++revision;
  refresh.onclick = openRouterMonitor;
  checks.replaceChildren();
  updated.textContent = '';
  if (!state.isAuthenticated) {
    status.textContent = '请先登录';
    return;
  }
  if (!['owner', 'admin'].includes(state.currentUser?.role)) {
    status.textContent = '无权查看路由器监控';
    return;
  }
  status.textContent = '正在读取路由器状态…';
  try {
    const response = await apiFetch('/api/v1/router-monitor');
    if (!response.ok) throw new Error('status unavailable');
    const view = await response.json();
    if (request !== revision || state.currentPage !== 'routerMonitor') return;
    const historical = view.state !== 'monitoring';
    status.textContent = modes[view.state] || '未知状态';
    if (view.last_success && !view.last_success.startsWith('0001-')) {
      updated.textContent = '最后成功采集：' + new Date(view.last_success).toLocaleString();
    }
    for (const item of view.snapshot?.checks || []) {
      const row = document.createElement('li');
      row.textContent = (labels[item.component] || item.component) + '：' +
        (historical ? '上次 ' : '') + (statuses[item.status] || '未知') +
        (item.detail && item.detail !== 'ok' ? '（' + item.detail + '）' : '');
      checks.append(row);
    }
  } catch {
    if (request === revision && state.currentPage === 'routerMonitor') status.textContent = '暂时无法读取路由器状态';
  }
}
