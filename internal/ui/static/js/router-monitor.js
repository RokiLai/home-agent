import { apiFetch } from './api.js';
import { state } from './state.js';

const modes = {
  unconfigured: '尚未配置采集节点',
  waiting: '等待采集结果',
  monitoring: '监控运行中',
  paused: '监控已暂停',
  stale: '数据已过期',
  collection_failed: '采集失败'
};

const labels = {
  lan: '局域网',
  wifi: '无线网络',
  resources: '系统资源',
  wan: '公网连接',
  dhcp: 'DHCP',
  ipv6: 'IPv6',
  shell: '代理核心',
  dns: 'DNS',
  proxy: '代理连通性',
  learner: 'IP 学习',
  learned_sets: 'IPv4/IPv6 HTTPS 学习分流'
};

const expectedComponents = [
  'lan', 'wifi', 'resources', 'wan', 'dhcp',
  'ipv6', 'shell', 'dns', 'proxy', 'learner', 'learned_sets'
];

const statuses = {
  healthy: '正常',
  failed: '异常',
  skipped: '跳过',
  unknown: '未知'
};

let revision = 0;

function formatTimestamp(ts) {
  if (!ts || ts.startsWith('0001-')) return '无数据';
  try {
    const dateObj = new Date(ts);
    return isNaN(dateObj.getTime()) ? '格式未知' : dateObj.toLocaleString();
  } catch {
    return '格式未知';
  }
}

export async function openRouterMonitor() {
  const status = document.getElementById('routerMonitorStatus');
  if (!status) return;
  const updated = document.getElementById('routerMonitorUpdated');
  const checks = document.getElementById('routerMonitorChecks');
  const refresh = document.getElementById('routerMonitorRefresh');

  const overviewHealth = document.getElementById('routerMonitorOverviewHealth');
  const overviewMode = document.getElementById('routerMonitorOverviewMode');
  const overviewReceived = document.getElementById('routerMonitorOverviewReceived');
  const overviewSuccess = document.getElementById('routerMonitorOverviewSuccess');
  const pipeline = document.getElementById('routerMonitorPipeline');
  const cards = document.getElementById('routerMonitorCards');
  const recoveryNotice = document.getElementById('routerMonitorRecoveryNotice');

  const request = ++revision;
  refresh.onclick = openRouterMonitor;

  function clearView() {
    checks.replaceChildren();
    if (cards) cards.replaceChildren();
    if (pipeline) pipeline.replaceChildren();
    updated.textContent = '';
    if (overviewMode) overviewMode.textContent = '-';
    if (overviewReceived) overviewReceived.textContent = '-';
    if (overviewSuccess) overviewSuccess.textContent = '-';
  }

  if (!state.isAuthenticated) {
    status.textContent = '请先登录';
    if (overviewHealth) overviewHealth.textContent = '请先登录';
    clearView();
    return;
  }
  if (!['owner', 'admin'].includes(state.currentUser?.role)) {
    status.textContent = '无权查看路由器监控';
    if (overviewHealth) overviewHealth.textContent = '无权查看';
    clearView();
    return;
  }

  status.textContent = '正在读取路由器状态…';
  if (overviewHealth) overviewHealth.textContent = '读取中…';
  refresh.disabled = true;

  try {
    const response = await apiFetch('/api/v1/router-monitor');
    if (!response.ok) throw new Error('status unavailable');
    const view = await response.json();
    if (request !== revision || state.currentPage !== 'routerMonitor') return;

    const historical = view.state !== 'monitoring';
    status.textContent = modes[view.state] || '未知状态';

    // 格式化时间
    const receivedStr = formatTimestamp(view.received);
    const lastSuccessStr = formatTimestamp(view.last_success);

    if (view.last_success && !view.last_success.startsWith('0001-')) {
      updated.textContent = '最后成功采集：' + lastSuccessStr;
    } else {
      updated.textContent = '';
    }

    if (overviewMode) overviewMode.textContent = modes[view.state] || '未知状态';
    if (overviewReceived) overviewReceived.textContent = receivedStr;
    if (overviewSuccess) overviewSuccess.textContent = lastSuccessStr;

    // 分析快照组件状态
    const checkList = view.snapshot?.checks || [];
    let hasFailed = false;
    let hasSkipped = false;
    let hasUnknown = false;
    const reportedMap = new Map();

    for (const item of checkList) {
      reportedMap.set(item.component, item);
      if (item.status === 'failed') hasFailed = true;
      else if (item.status === 'skipped') hasSkipped = true;
      else if (item.status !== 'healthy') hasUnknown = true;
    }

    // 检查是否有缺失的核心组件
    let hasMissing = false;
    for (const comp of expectedComponents) {
      if (!reportedMap.has(comp)) {
        hasMissing = true;
      }
    }

    // 计算整体健康状态
    let overallHealth = '未知';
    if (view.state === 'unconfigured') {
      overallHealth = '未配置采集节点';
    } else if (view.state === 'waiting' && !view.snapshot) {
      overallHealth = '等待首次采集';
    } else if (view.state === 'waiting' && view.snapshot?.mode === 'waiting') {
      overallHealth = '等待首次完整探测';
    } else if (view.state === 'paused') {
      overallHealth = '监控已暂停';
    } else if (view.state === 'collection_failed') {
      overallHealth = '采集异常（展示历史快照）';
    } else if (view.state === 'stale') {
      overallHealth = '数据已过期（展示历史快照）';
    } else if (view.state === 'monitoring') {
      if (hasFailed) {
        overallHealth = '存在异常组件';
      } else if (hasMissing || hasSkipped || hasUnknown) {
        overallHealth = '检查不完整';
      } else {
        overallHealth = '全部正常';
      }
    }
    if (overviewHealth) overviewHealth.textContent = overallHealth;

    // 渲染旧入口检查项兼容测试
    checks.replaceChildren();
    for (const item of checkList) {
      const row = document.createElement('li');
      row.textContent = (labels[item.component] || item.component) + '：' +
        (historical ? '上次 ' : '') + (statuses[item.status] || '未知') +
        (item.detail && item.detail !== 'ok' ? '（' + item.detail + '）' : '');
      checks.append(row);
    }

    // 渲染监控流程拓扑
    if (pipeline) {
      pipeline.replaceChildren();
      const nodesData = [
        {
          name: 'Router Guard 探测',
          statusText: (view.state === 'unconfigured' || !view.snapshot) ? '不可观测' : (view.snapshot.mode === 'paused' ? '已暂停' : '已就绪'),
          timeText: (view.snapshot?.checked_uptime != null ? `最近探测（开机 ${view.snapshot.checked_uptime}s）` : '无探测数据')
        },
        {
          name: 'Agent 采集',
          statusText: (view.state === 'unconfigured') ? '不可观测' : (view.state === 'collection_failed' ? '采集失败' : '正常上报'),
          timeText: receivedStr !== '无数据' ? `接收时间：${receivedStr}` : '无上报数据'
        },
        {
          name: 'HTTPS 上报',
          statusText: (view.state === 'unconfigured') ? '不可观测' : (view.state === 'stale' ? '上报停滞或过期' : '通道可达'),
          timeText: 'TLS 专用双向通道'
        },
        {
          name: '服务端保存',
          statusText: (view.last_success && !view.last_success.startsWith('0001-')) ? '快照已保存' : '无落盘快照',
          timeText: lastSuccessStr !== '无数据' ? `成功快照：${lastSuccessStr}` : '尚未成功保存'
        },
        {
          name: '页面读取',
          statusText: '已读取最新状态',
          timeText: new Date().toLocaleTimeString()
        }
      ];

      nodesData.forEach((nodeItem, idx) => {
        const step = document.createElement('div');
        step.className = 'pipeline-step';

        const stepCard = document.createElement('div');
        stepCard.className = 'pipeline-card';

        const stepTitle = document.createElement('div');
        stepTitle.className = 'pipeline-title';
        stepTitle.textContent = nodeItem.name;

        const stepStatus = document.createElement('div');
        stepStatus.className = 'pipeline-status';
        stepStatus.textContent = nodeItem.statusText;

        const stepTime = document.createElement('div');
        stepTime.className = 'pipeline-time';
        stepTime.textContent = nodeItem.timeText;

        stepCard.append(stepTitle, stepStatus, stepTime);
        step.append(stepCard);

        if (idx < nodesData.length - 1) {
          const arrow = document.createElement('div');
          arrow.className = 'pipeline-arrow';
          arrow.textContent = '→';
          step.append(arrow);
        }

        pipeline.append(step);
      });
    }

    // 渲染组件状态卡片
    if (cards) {
      cards.replaceChildren();
      const allDisplayComponents = [...expectedComponents];
      // 检查是否有未知/自定义组件
      for (const item of checkList) {
        if (!allDisplayComponents.includes(item.component)) {
          allDisplayComponents.push(item.component);
        }
      }

      for (const comp of allDisplayComponents) {
        const item = reportedMap.get(comp);
        const card = document.createElement('div');
        card.className = 'component-card';

        const cardHeader = document.createElement('div');
        cardHeader.className = 'component-card-header';

        const cardTitle = document.createElement('span');
        cardTitle.className = 'component-card-title';
        cardTitle.textContent = labels[comp] || comp;

        const cardBadge = document.createElement('span');
        let statusClass = 'badge-secondary';
        let statusLabel = '未上报';

        if (item) {
          const mapped = statuses[item.status] || '未知';
          statusLabel = (historical ? '上次 ' : '') + mapped;
          if (item.status === 'healthy') statusClass = 'badge-success';
          else if (item.status === 'failed') statusClass = 'badge-danger';
          else if (item.status === 'skipped') statusClass = 'badge-warn';
          else statusClass = 'badge-info';
        } else {
          statusLabel = '未上报';
          statusClass = 'badge-secondary';
        }

        cardBadge.className = `badge ${statusClass}`;
        cardBadge.textContent = statusLabel;

        cardHeader.append(cardTitle, cardBadge);

        const cardDetail = document.createElement('div');
        cardDetail.className = 'component-card-detail';
        cardDetail.textContent = item?.detail || (item ? '无详情' : '该组件在本次快照中未上报');

        card.append(cardHeader, cardDetail);
        cards.append(card);
      }
    }

    // 恢复区域事件时间线渲染
    const timeline = document.getElementById('routerMonitorTimeline');
    if (timeline) {
      timeline.replaceChildren();
    }
    try {
      const eventsRes = await apiFetch('/api/v1/router-monitor/events');
      if (eventsRes.ok) {
        const eventsData = await eventsRes.json();
        if (eventsData && eventsData.items && eventsData.items.length > 0) {
          if (recoveryNotice) {
            recoveryNotice.textContent = `已接入 (${eventsData.items.length} 条记录)`;
            recoveryNotice.className = 'badge badge-success';
          }
          if (timeline) {
            eventsData.items.forEach(ev => {
              const itemEl = document.createElement('div');
              itemEl.className = 'timeline-item';

              const badge = document.createElement('span');
              badge.className = 'badge ' + (ev.display_status === '已恢复' ? 'badge-success' : (ev.display_status === '执行失败或超时' ? 'badge-danger' : 'badge-info'));
              badge.textContent = ev.display_status;

              const title = document.createElement('strong');
              title.textContent = ` [${labels[ev.component] || ev.component}] `;

              const desc = document.createElement('span');
              desc.textContent = ev.description + (ev.uptime != null ? ` (开机 ${ev.uptime}s)` : '');

              itemEl.append(badge, title, desc);
              timeline.append(itemEl);
            });
          }
        } else {
          if (recoveryNotice) {
            recoveryNotice.textContent = '暂无恢复事件';
            recoveryNotice.className = 'badge badge-secondary';
          }
        }
      } else {
        if (recoveryNotice) {
          recoveryNotice.textContent = '事件读取失败';
          recoveryNotice.className = 'badge badge-danger';
        }
      }
    } catch {
      if (recoveryNotice) {
        recoveryNotice.textContent = '事件读取失败';
        recoveryNotice.className = 'badge badge-danger';
      }
    }
  } catch {
    if (request === revision && state.currentPage === 'routerMonitor') {
      status.textContent = '暂时无法读取路由器状态';
      if (overviewHealth) overviewHealth.textContent = '页面读取失败';
    }
  } finally {
    if (request === revision) {
      refresh.disabled = false;
      if (document.activeElement === document.body || !document.activeElement) {
        refresh.focus();
      }
    }
  }
}
