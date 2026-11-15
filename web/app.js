'use strict';

/* ============================================================================
   adks builder · 前端只有三件事：把 manifest 画成表单、把表单写回 manifest、
   把试跑的事件流画出来。其余都是这两条链路的呈现。
   ========================================================================== */

const state = {
  projects: [],
  slug: null,
  manifest: null,
  yaml: '',
  issues: [],
  versions: [],
  estimate: null,
  filter: '',
  tab: 'form',
  dirty: false,
  sessionId: newSessionId(),
  pending: null,
  busy: false,
  toolCalls: new Map(), // callId -> <details> 元素
};

function newSessionId() {
  const rnd = crypto.getRandomValues(new Uint32Array(2));
  return 'sess_' + rnd[0].toString(16) + rnd[1].toString(16);
}

const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => Array.from(document.querySelectorAll(sel));

function esc(s) {
  return String(s == null ? '' : s)
    .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

const ICON = {
  chev: '<svg class="chev" viewBox="0 0 16 16" aria-hidden="true"><path d="M4 6.5L8 10.5l4-4" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/></svg>',
  plus: '<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M8 3.5v9M3.5 8h9" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg>',
};

/* ------------------------------------------------------------------- api */

async function call(method, path, body) {
  const opt = { method, headers: {} };
  if (body !== undefined) {
    opt.headers['Content-Type'] = 'application/json';
    opt.body = JSON.stringify(body);
  }
  const resp = await fetch(path, opt);
  const text = await resp.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch (e) { data = { raw: text }; }
  if (!resp.ok) {
    const msg = (data && data.error) || text || resp.statusText;
    throw new Error(msg);
  }
  return data;
}

/* ------------------------------------------------------- path get/set util */

function getPath(obj, path) {
  return path.split('.').reduce((o, k) => (o == null ? undefined : o[k]), obj);
}

function setPath(obj, path, value) {
  const keys = path.split('.');
  let cur = obj;
  for (let i = 0; i < keys.length - 1; i++) {
    const k = keys[i];
    if (cur[k] == null || typeof cur[k] !== 'object') cur[k] = {};
    cur = cur[k];
  }
  const last = keys[keys.length - 1];
  if (value === undefined || value === '') {
    delete cur[last];
  } else {
    cur[last] = value;
  }
}

function pruneEmpty(obj) {
  if (Array.isArray(obj)) {
    obj.forEach(pruneEmpty);
    return obj;
  }
  if (obj && typeof obj === 'object') {
    Object.keys(obj).forEach((k) => {
      const v = obj[k];
      if (v === null || v === undefined) { delete obj[k]; return; }
      if (typeof v === 'object') {
        pruneEmpty(v);
        if (!Array.isArray(v) && Object.keys(v).length === 0) delete obj[k];
      }
    });
  }
  return obj;
}

/* --------------------------------------------------------------- projects */

async function loadProjects() {
  const data = await call('GET', '/api/projects');
  state.projects = data.projects || [];
  $('#home-path').textContent = data.home || '';
  $('#home-path').title = data.home || '';
  renderProjects();
}

function projectFiltered() {
  const q = state.filter.trim().toLowerCase();
  if (!q) return state.projects;
  return state.projects.filter((p) =>
    (p.name || '').toLowerCase().includes(q) || (p.slug || '').toLowerCase().includes(q));
}

function renderProjects() {
  const ul = $('#project-list');
  $('#project-count').textContent = String(state.projects.length);
  const list = projectFiltered();

  if (!state.projects.length) {
    ul.innerHTML = '<li class="empty-row">还没有项目。<br>在下面输入 slug 建第一个。</li>';
    return;
  }
  if (!list.length) {
    ul.innerHTML = `<li class="empty-row">没有匹配「${esc(state.filter)}」的项目。</li>`;
    return;
  }

  ul.innerHTML = list.map((p) => {
    const level = p.errorCount ? 'err' : p.warnCount ? 'warn' : 'ok';
    return `<li data-slug="${esc(p.slug)}" class="${p.slug === state.slug ? 'active' : ''}">
      <div class="p-name">
        <span class="dot ${level}"></span>
        <span class="name" title="${esc(p.slug)}">${esc(p.name || p.slug)}</span>
      </div>
      <div class="p-meta">
        <span class="model" title="${esc(p.model || '')}">${esc(p.model || '—')}</span>
        <span class="tools">${p.toolCount || 0} 工具</span>
      </div>
    </li>`;
  }).join('');

  $$('#project-list li[data-slug]').forEach((li) => {
    li.onclick = () => openProject(li.dataset.slug);
  });
}

function renderProjectHead() {
  const head = $('#project-head');
  if (!state.manifest) { head.hidden = true; return; }
  head.hidden = false;

  const m = state.manifest;
  const name = (m.metadata && m.metadata.name) || (m.metadata && m.metadata.slug) || '未命名';
  $('#proj-name').textContent = name;
  $('#proj-slug').textContent = (m.metadata && m.metadata.slug) || state.slug || '—';

  const est = state.estimate || {};
  const errs = state.issues.filter((i) => i.level === 'error').length;
  const warns = state.issues.length - errs;

  const model = m.spec?.agent?.model;
  const modelLabel = model ? `${model.provider || '?'}/${model.name || '?'}` : '未配置';

  const parts = [
    `<span class="stat" title="模型">${esc(modelLabel)}</span>`,
    `<span class="stat" title="工具总数">工具 <b>${est.toolCount ?? 0}</b></span>`,
    `<span class="stat" title="指令 + 工具声明的粗略 token 估算，不是账单">≈ <b>${est.totalTokens ?? 0}</b> tokens</span>`,
  ];
  if (errs) parts.push(`<span class="stat err" title="校验错误">${errs} 错误</span>`);
  else if (warns) parts.push(`<span class="stat warn" title="校验警告">${warns} 警告</span>`);
  else parts.push('<span class="stat ok">校验通过</span>');

  $('#proj-stats').innerHTML = parts.join('');
}

async function createProject() {
  const slug = ($('#new-slug').value || '').trim();
  if (!slug) { $('#new-slug').focus(); toast('先填一个 slug'); return; }
  try {
    await call('POST', '/api/projects', { slug, name: slug });
    $('#new-slug').value = '';
    await loadProjects();
    await openProject(slug);
  } catch (e) { toast(e.message, true); }
}

async function deleteProject() {
  if (!state.slug) return;
  const slug = state.slug;
  if (!confirm(`删除项目 ${slug}？\n项目目录、快照、导出的源码都会一并删除，无法恢复。`)) return;
  try {
    await call('DELETE', `/api/projects/${encodeURIComponent(slug)}`);
    state.slug = null;
    state.manifest = null;
    state.yaml = '';
    state.issues = [];
    state.versions = [];
    state.estimate = null;
    state.dirty = false;
    $('#editor-empty').hidden = false;
    $('#project-head').hidden = true;
    $('#form-root').innerHTML = '';
    $('#yaml-input').value = '';
    $('#issues').innerHTML = '';
    setSaveState('');
    clearChat();
    await loadProjects();
    if (state.projects.length) await openProject(state.projects[0].slug);
    toast('已删除 ' + slug);
  } catch (e) { toast(e.message, true); }
}

async function openProject(slug) {
  if (state.dirty && !confirm('有未保存的修改，仍要切换项目？')) return;
  try {
    const data = await call('GET', `/api/projects/${encodeURIComponent(slug)}`);
    state.slug = slug;
    state.yaml = data.manifestYaml || '';
    state.manifest = data.manifest || null;
    state.issues = data.issues || [];
    state.versions = data.versions || [];
    state.estimate = data.estimate || null;
    state.dirty = false;
    state.sessionId = newSessionId();
    state.toolCalls.clear();
    clearChat();
    $('#editor-empty').hidden = true;
    showTab(state.tab);
    $('#yaml-input').value = state.yaml;
    renderForm();
    renderIssues();
    renderVersions();
    renderProjectHead();
    renderProjects();
    refreshTally();
    setSaveState(data.loadError ? '解析失败' : '已加载', data.loadError ? 'error' : '');
    if (data.loadError) {
      toast('这个项目的 YAML 无法解析，已切到 YAML 标签页', true);
      showTab('yaml');
    }
  } catch (e) { toast(e.message, true); }
}

/* ------------------------------------------------------------------- form */

const SECTIONS = [
  {
    key: 'agent',
    title: 'Agent 身份',
    open: true,
    match: ['metadata', 'spec.agent.name', 'spec.agent.description', 'spec.agent.instruction'],
    fields: [
      { path: 'metadata.name', label: '项目名', type: 'text' },
      { path: 'spec.agent.name', label: 'Agent 名', type: 'text', hint: 'ADK 内部标识，小写字母开头，不能是 user' },
      { path: 'spec.agent.description', label: '描述', type: 'text', full: true, hint: '最多 200 字；是多 Agent 时上级据此决定是否委派' },
      { path: 'spec.agent.instruction', label: '指令', type: 'textarea', full: true, rows: 9, hint: '禁止写时间戳或随机 ID：会让 prompt 前缀缓存每天失效' },
    ],
  },
  {
    key: 'model',
    title: '模型',
    open: true,
    match: ['spec.agent.model', 'spec.agent.generation'],
    fields: [
      { path: 'spec.agent.model.provider', label: '供应商', type: 'select', options: ['gemini', 'openai', 'anthropic'] },
      { path: 'spec.agent.model.name', label: '模型名', type: 'text' },
      { path: 'spec.agent.model.apiKeyEnv', label: '密钥环境变量', type: 'text', hint: '只存变量名，值从进程环境读取' },
      { path: 'spec.agent.model.baseURL', label: 'Base URL', type: 'text', hint: '仅 openai 兼容端点使用' },
      { path: 'spec.agent.generation.temperature', label: 'temperature', type: 'number', step: '0.05', hint: '留空表示不设置' },
      { path: 'spec.agent.generation.topP', label: 'topP', type: 'number', step: '0.05' },
      { path: 'spec.agent.generation.maxOutputTokens', label: 'maxOutputTokens', type: 'number', step: '1' },
    ],
  },
  {
    key: 'runtime',
    title: '运行时与存储',
    open: false,
    match: ['spec.runtime'],
    fields: [
      { path: 'spec.runtime.server.mode', label: '启动模式', type: 'select', options: ['full', 'web', 'api', 'console'] },
      { path: 'spec.runtime.server.host', label: '绑定地址', type: 'text' },
      { path: 'spec.runtime.server.port', label: '端口', type: 'number', step: '1' },
      { path: 'spec.runtime.server.auth', label: '鉴权', type: 'select', options: ['none', 'bearer'] },
      { path: 'spec.runtime.session.backend', label: '会话后端', type: 'select', options: ['sqlite', 'memory', 'postgres', 'mysql'] },
      { path: 'spec.runtime.session.maxOpenConns', label: '连接数', type: 'number', step: '1' },
      { path: 'spec.runtime.session.dsn', label: 'DSN', type: 'text', full: true, hint: 'sqlite 相对路径会重定位到项目运行目录，多个项目互不干扰' },
      { path: 'spec.runtime.artifact.backend', label: '制品后端', type: 'select', options: ['local', 'memory'] },
      { path: 'spec.runtime.artifact.dir', label: '制品目录', type: 'text' },
      { path: 'spec.runtime.memory.backend', label: '长期记忆', type: 'select', options: ['memory'] },
      { path: 'spec.runtime.compaction.enabled', label: '开启压缩', type: 'checkbox', hint: '会重写前缀并多一次摘要调用' },
    ],
  },
  {
    key: 'policy',
    title: '策略与限额',
    open: false,
    match: ['spec.policy'],
    fields: [
      { path: 'spec.policy.confirmation', label: '确认策略', type: 'select', options: ['destructive', 'none', 'all'] },
      { path: 'spec.policy.destructiveTools', label: '需确认的工具', type: 'tags', hint: '逗号分隔；也可在工具上勾选 destructive' },
      { path: 'spec.policy.limits.maxLlmCalls', label: '最大模型调用', type: 'number', step: '1' },
      { path: 'spec.policy.limits.maxWallClockSec', label: '最大墙钟秒', type: 'number', step: '1' },
      { path: 'spec.policy.limits.maxToolConcurrency', label: '工具并发', type: 'number', step: '1' },
      { path: 'spec.policy.limits.maxInstructionBytes', label: '指令字节上限', type: 'number', step: '1024' },
    ],
  },
];

const BUILTINS = [
  ['google_search', '模型侧联网搜索'],
  ['url_context', '抓取并理解 URL'],
  ['exit_loop', '跳出循环工具'],
  ['load_artifacts', '读取会话制品'],
  ['preload_memory', '预加载长期记忆'],
];

// normalizeManifest guarantees the containers the form writes into exist, so a
// manifest that omitted an optional section does not throw on the first edit.
function normalizeManifest() {
  if (!state.manifest) return;
  const spec = state.manifest.spec || (state.manifest.spec = {});
  const tools = spec.tools || (spec.tools = {});
  if (!Array.isArray(tools.builtin)) tools.builtin = [];
  if (!Array.isArray(tools.http)) tools.http = [];
  if (!Array.isArray(tools.mcp)) tools.mcp = [];
  const policy = spec.policy || (spec.policy = {});
  if (!policy.limits) policy.limits = {};
  if (!Array.isArray(policy.destructiveTools)) policy.destructiveTools = [];
  const runtime = spec.runtime || (spec.runtime = {});
  if (!runtime.server) runtime.server = {};
  if (!runtime.session) runtime.session = {};
  if (!runtime.artifact) runtime.artifact = {};
  if (!runtime.memory) runtime.memory = {};
  if (!Array.isArray(spec.plugins)) spec.plugins = [];
  if (!spec.mode) spec.mode = 'llm';
  const graph = spec.graph || (spec.graph = {});
  if (!Array.isArray(graph.nodes)) graph.nodes = [];
  if (!Array.isArray(graph.edges)) graph.edges = [];
}

/* issues -> per-section badge, so a warning deep in runtime is visible without
   opening every card. */
function issuesFor(prefixes) {
  if (!prefixes || !prefixes.length) return [];
  return state.issues.filter((i) =>
    prefixes.some((p) => i.path === p || i.path.startsWith(p + '.')));
}

function renderForm() {
  if (!state.manifest) { $('#form-root').innerHTML = ''; return; }
  normalizeManifest();

  // Every add/delete re-renders the whole form. Without carrying the open state
  // and scroll offset across, the page collapses and jumps under the cursor.
  const pane = $('.tab-pane[data-pane="form"]');
  const scroll = pane ? pane.scrollTop : 0;
  const openState = $$('#form-root details.section').map((d) => d.open);

  const html = [];
  let n = 0;
  // The graph section sits right after the identity card because it decides what
  // the rest of the form means: in graph mode the instruction belongs to nodes,
  // not to the root agent.
  html.push(renderSection(SECTIONS[0], ++n));
  html.push(renderGraphSection(++n));
  for (const sec of SECTIONS.slice(1)) html.push(renderSection(sec, ++n));
  html.push(renderToolsSection(++n));
  html.push(renderPluginsSection(++n));
  $('#form-root').innerHTML = html.join('');

  $$('#form-root details.section').forEach((d, i) => {
    if (openState[i] !== undefined) d.open = openState[i];
  });
  if (pane) pane.scrollTop = scroll;
  bindForm();
}

function sectionHead(n, title, count, issues, extra) {
  const errs = issues.filter((i) => i.level === 'error').length;
  const badge = errs
    ? `<span class="pill err">${errs}</span>`
    : issues.length ? `<span class="pill warn">${issues.length}</span>` : '';
  return `<summary>
    <span class="sec-num">${String(n).padStart(2, '0')}</span>
    <span class="sec-title">${esc(title)}</span>
    <span class="sec-meta">${badge}${extra || ''}<span class="count">${count}</span>${ICON.chev}</span>
  </summary>`;
}

function renderSection(sec, n) {
  const issues = issuesFor(sec.match);
  const body = sec.fields.map((f) => renderField(f)).join('') + extraFor(sec.key);
  return `<details class="section" ${sec.open ? 'open' : ''}>
    ${sectionHead(n, sec.title, `${sec.fields.length} 项`, issues)}
    <div class="section-body">${body}</div>
  </details>`;
}

function renderField(f) {
  const v = getPath(state.manifest, f.path);
  const id = 'f_' + f.path.replace(/\./g, '_');
  const bad = state.issues.some((i) => i.level === 'error' && (i.path === f.path || i.path.startsWith(f.path + '.')));
  const cls = 'field' + (f.full ? ' full' : '') + (bad ? ' invalid' : '');
  const hint = f.hint ? `<span class="hint">${esc(f.hint)}</span>` : '';
  let input = '';

  if (f.type === 'textarea') {
    input = `<textarea id="${id}" data-path="${f.path}" rows="${f.rows || 4}" spellcheck="false">${esc(v || '')}</textarea>`;
  } else if (f.type === 'select') {
    const opts = f.options.map((o) => `<option value="${esc(o)}" ${String(v) === o ? 'selected' : ''}>${esc(o)}</option>`);
    input = `<select id="${id}" data-path="${f.path}">${opts.join('')}</select>`;
  } else if (f.type === 'checkbox') {
    return `<div class="${cls}"><label class="check"><input type="checkbox" id="${id}" data-path="${f.path}" ${v ? 'checked' : ''}> ${esc(f.label)}</label>${hint}</div>`;
  } else if (f.type === 'tags') {
    const list = Array.isArray(v) ? v.join(', ') : '';
    input = `<input id="${id}" data-path="${f.path}" data-tags="1" value="${esc(list)}" placeholder="a, b, c" spellcheck="false">`;
  } else if (f.type === 'number') {
    input = `<input id="${id}" data-path="${f.path}" data-number="1" type="number" step="${f.step || 'any'}" value="${v == null ? '' : esc(v)}">`;
  } else {
    input = `<input id="${id}" data-path="${f.path}" value="${esc(v || '')}" spellcheck="false">`;
  }
  return `<div class="${cls}"><label for="${id}">${esc(f.label)}<span class="path">${esc(f.path)}</span></label>${input}${hint}</div>`;
}

// extraFor adds the connectivity probes that sit next to the thing they test.
function extraFor(key) {
  if (key === 'model') {
    return `<div class="field full">
      <div class="field-actions">
        <button class="btn small" data-probe-model type="button">测试模型连通</button>
        <span class="hint">真实发一次最小请求——密钥能解析不等于能用。</span>
      </div>
      <div class="result" data-probe-model-result></div>
    </div>`;
  }
  if (key === 'runtime') {
    return `<div class="field full">
      <div class="field-actions">
        <button class="btn small" data-probe-sql type="button">测试会话存储</button>
        <span class="hint">建一个会话再删掉，顺带验证方言、DSN 和建表。</span>
      </div>
      <div class="result" data-probe-sql-result></div>
    </div>`;
  }
  return '';
}

function renderToolsSection(n) {
  const t = state.manifest.spec.tools || {};
  const builtin = t.builtin || [];
  const http = t.http || [];
  const mcp = t.mcp || [];
  const issues = issuesFor(['spec.tools']);

  const chips = BUILTINS.map(([name, desc]) =>
    `<label class="chip-check" title="${esc(desc)}">
      <input type="checkbox" data-builtin="${name}" ${builtin.includes(name) ? 'checked' : ''}>
      <span class="box"></span>${esc(name)}
    </label>`).join('');

  const httpHtml = http.map((h, i) => renderHTTPItem(h, i)).join('') ||
    '<div class="muted" style="font-size:12px;padding:6px 0">还没有 HTTP 工具。ADK Go 没有 OpenAPI toolset，这里声明的工具由脚手架自己实现。</div>';
  const mcpHtml = mcp.map((m, i) => renderMCPItem(m, i)).join('') ||
    '<div class="muted" style="font-size:12px;padding:6px 0">还没有 MCP 服务器。</div>';

  return `<details class="section" open>
    ${sectionHead(n, '工具', `${builtin.length + http.length + mcp.length} 个来源`, issues)}
    <div class="section-body" style="grid-template-columns:1fr">
      <div class="field full">
        <label>内置工具</label>
        <div class="chips">${chips}</div>
      </div>

      <div class="field full">
        <label class="check"><input type="checkbox" id="toolsearch" ${t.toolSearch ? 'checked' : ''}> 延迟披露工具（toolSearch）</label>
        <span class="hint">工具多时改用 search_tools 折叠；与 MCP 的 toolAllow 互斥。少于 5 个工具时没有收益。</span>
      </div>

      <div class="field full">
        <div class="group-label">声明式 HTTP 工具 <span class="mono">spec.tools.http</span></div>
        <div id="http-list">${httpHtml}</div>
        <button class="btn small" id="add-http" type="button">${ICON.plus} 添加 HTTP 工具</button>
      </div>

      <div class="field full">
        <div class="group-label">MCP 服务器 <span class="mono">spec.tools.mcp</span></div>
        <div id="mcp-list">${mcpHtml}</div>
        <div class="field-actions" style="margin-top:8px">
          <button class="btn small" id="add-mcp" type="button">${ICON.plus} 添加 MCP 服务器</button>
          <span class="hint">MCP 工具不支持执行前确认，只会走「执行后告知 + 审计」。</span>
        </div>
      </div>
    </div>
  </details>`;
}

function renderHTTPItem(h, i) {
  const base = `spec.tools.http.${i}`;
  const method = (h.method || 'GET').toUpperCase();
  let host = '';
  try { host = h.url ? new URL(h.url).host : ''; } catch (e) { host = ''; }

  const params = (h.params || []).map((p, j) => `
    <div class="param-row">
      <input data-path="${base}.params.${j}.name" value="${esc(p.name || '')}" placeholder="参数名" spellcheck="false">
      <select data-path="${base}.params.${j}.type">
        ${['string', 'integer', 'number', 'boolean', 'array_of_string'].map((t) => `<option ${p.type === t ? 'selected' : ''}>${t}</option>`).join('')}
      </select>
      <select data-path="${base}.params.${j}.in">
        ${['path', 'query', 'body'].map((t) => `<option ${p.in === t ? 'selected' : ''}>${t}</option>`).join('')}
      </select>
      <input data-path="${base}.params.${j}.description" value="${esc(p.description || '')}" placeholder="参数说明" spellcheck="false">
      <label class="check"><input type="checkbox" data-path="${base}.params.${j}.required" data-bool="1" ${p.required ? 'checked' : ''}> 必填</label>
      <button class="btn small danger" data-del-param="${i}:${j}" type="button" title="删除参数">×</button>
    </div>`).join('');

  const auth = h.auth || {};
  return `<div class="tool-card">
    <div class="tool-head">
      <span class="method ${esc(method)}">${esc(method)}</span>
      <span class="tool-name">${esc(h.name || '未命名工具')}</span>
      <span class="tool-host">${esc(host)}</span>
      <span class="spacer"></span>
      <button class="btn small danger" data-del-http="${i}" type="button">删除</button>
    </div>
    <div class="tool-body">
      <div class="field">
        <label>工具名</label>
        <input data-path="${base}.name" value="${esc(h.name || '')}" placeholder="get_order" spellcheck="false">
      </div>
      <div class="field">
        <label>方法</label>
        <select data-path="${base}.method">${['GET', 'POST', 'PUT', 'PATCH', 'DELETE'].map((m) => `<option ${method === m ? 'selected' : ''}>${m}</option>`).join('')}</select>
      </div>
      <div class="field full">
        <label>描述 <span class="path">至少 15 字</span></label>
        <input data-path="${base}.description" value="${esc(h.description || '')}" placeholder="模型据此决定什么时候调用它" spellcheck="false">
      </div>
      <div class="field full">
        <label>URL</label>
        <input data-path="${base}.url" value="${esc(h.url || '')}" placeholder="https://api.example.com/v1/orders/{order_id}" spellcheck="false">
      </div>
      <div class="field">
        <label>允许的主机 <span class="path">allowedHosts</span></label>
        <input data-path="${base}.allowedHosts" data-tags="1" value="${esc((h.allowedHosts || []).join(', '))}" placeholder="api.example.com" spellcheck="false">
      </div>
      <div class="field">
        <label>超时（秒）</label>
        <input data-path="${base}.timeoutSec" data-number="1" type="number" value="${h.timeoutSec || ''}" placeholder="20">
      </div>
      <div class="field">
        <label>鉴权方式</label>
        <select data-path="${base}.auth.scheme">
          <option value="">无鉴权</option>
          ${['bearer', 'apikey-header', 'basic'].map((s) => `<option ${auth.scheme === s ? 'selected' : ''}>${s}</option>`).join('')}
        </select>
      </div>
      <div class="field">
        <label>密钥环境变量</label>
        <input data-path="${base}.auth.secretEnv" value="${esc(auth.secretEnv || '')}" placeholder="ORDERS_API_TOKEN" spellcheck="false">
      </div>
      <div class="field">
        <label>响应上限（字节）</label>
        <input data-path="${base}.maxRespBytes" data-number="1" type="number" value="${h.maxRespBytes || ''}" placeholder="65536">
      </div>
      <div class="field">
        <label>破坏性</label>
        <label class="check"><input type="checkbox" data-path="${base}.destructive" data-bool="1" ${h.destructive ? 'checked' : ''}> 执行前需要人工确认</label>
      </div>
      <div class="param-table">
        <div class="param-head"><span>参数</span><span class="spacer" style="flex:1"></span>
          <button class="btn small" data-add-param="${i}" type="button">${ICON.plus} 参数</button>
        </div>
        ${params || '<div class="muted" style="font-size:11.5px">没有参数。URL 里用 {name} 占位即可声明路径参数。</div>'}
      </div>
    </div>
  </div>`;
}

function renderMCPItem(m, i) {
  const base = `spec.tools.mcp.${i}`;
  const headers = m.headers ? Object.entries(m.headers).map(([k, v]) => `${k}: ${v}`).join('\n') : '';
  return `<div class="tool-card">
    <div class="tool-head">
      <span class="method GET">MCP</span>
      <span class="tool-name">${esc(m.id || '未命名 MCP')}</span>
      <span class="tool-host">${esc(m.transport || 'stdio')}</span>
      <span class="spacer"></span>
      <button class="btn small" data-probe="${i}" type="button">探测工具</button>
      <button class="btn small danger" data-del-mcp="${i}" type="button">删除</button>
    </div>
    <div class="tool-body">
      <div class="field">
        <label>id</label>
        <input data-path="${base}.id" value="${esc(m.id || '')}" placeholder="files" spellcheck="false">
      </div>
      <div class="field">
        <label>传输方式</label>
        <select data-path="${base}.transport">${['stdio', 'http', 'sse'].map((t) => `<option ${m.transport === t ? 'selected' : ''}>${t}</option>`).join('')}</select>
      </div>
      <div class="field">
        <label>命令 <span class="path">stdio</span></label>
        <input data-path="${base}.command" value="${esc(m.command || '')}" placeholder="npx" spellcheck="false">
      </div>
      <div class="field">
        <label>参数</label>
        <input data-path="${base}.args" data-tags="1" value="${esc((m.args || []).join(', '))}" placeholder="-y, @modelcontextprotocol/server-filesystem, ." spellcheck="false">
      </div>
      <div class="field full">
        <label>端点 URL <span class="path">http / sse</span></label>
        <input data-path="${base}.url" value="${esc(m.url || '')}" placeholder="https://mcp.example.com/mcp" spellcheck="false">
      </div>
      <div class="field">
        <label>超时（秒）</label>
        <input data-path="${base}.timeoutSec" data-number="1" type="number" value="${m.timeoutSec || ''}" placeholder="20">
      </div>
      <div class="field">
        <label>透传环境变量白名单</label>
        <input data-path="${base}.envAllow" data-tags="1" value="${esc((m.envAllow || []).join(', '))}" placeholder="PATH, HOME" spellcheck="false">
      </div>
      <div class="field full">
        <label>只披露这些工具 <span class="path">toolAllow</span></label>
        <input data-path="${base}.toolAllow" data-tags="1" value="${esc((m.toolAllow || []).join(', '))}" placeholder="read_file, list_directory" spellcheck="false">
      </div>
      <div class="field full">
        <label>请求头 <span class="path">每行一个</span></label>
        <textarea data-path="${base}.headers" data-headers="1" rows="2" placeholder="Authorization: Bearer ${'${MCP_TOKEN}'}" spellcheck="false">${esc(headers)}</textarea>
      </div>
    </div>
    <div class="result" data-probe-result="${i}" style="padding:0 11px 11px"></div>
  </div>`;
}

/* ---------- graph editor ---------- */

const NODE_KINDS = [
  ['agent', 'Agent 节点', 'LLM 子代理，唯一会调模型的节点'],
  ['tool', '工具节点', '直接执行一个工具，把结果交给下一站'],
  ['route', '路由节点', '按规则打标签选分支，不调模型'],
  ['join', '汇聚点', '等所有前驱完成后才继续'],
  ['subgraph', '子图', '嵌套的另一张图'],
];

const NODE_DEFAULTS = {
  agent: () => ({ instruction: '', tools: [] }),
  tool: () => ({ tool: '' }),
  route: () => ({ rules: [{ op: 'contains', match: '', route: 'go' }], input: '' }),
  join: () => ({}),
  subgraph: () => ({ sub: { nodes: [], edges: [] } }),
};

function renderGraphSection(n) {
  const spec = state.manifest.spec;
  const g = spec.graph || (spec.graph = { nodes: [], edges: [] });
  const isGraph = spec.mode === 'graph';
  const issues = issuesFor(['spec.mode', 'spec.graph']);

  const modePicker = `<div class="field full">
    <label>编排模式 <span class="path">spec.mode</span></label>
    <div class="mode-cards">
      <label class="mode-card ${!isGraph ? 'on' : ''}">
        <input type="radio" name="graph-mode" value="llm" ${!isGraph ? 'checked' : ''}>
        <b>单 Agent</b><span>一个 LLM 循环，工具平铺。默认。</span>
      </label>
      <label class="mode-card ${isGraph ? 'on' : ''}">
        <input type="radio" name="graph-mode" value="graph" ${isGraph ? 'checked' : ''}>
        <b>Graph 工作流</b><span>节点 + 边：条件分支、并行、汇聚、嵌套子图。</span>
      </label>
    </div>
    ${isGraph ? '' : '<span class="hint">切到 Graph 才会编译下面的节点和边。单 Agent 模式下这些内容会保留但不生效。</span>'}
  </div>`;

  if (!isGraph) {
    return `<details class="section" open>
      ${sectionHead(n, '编排', '未启用', issues)}
      <div class="section-body" style="grid-template-columns:1fr">${modePicker}</div>
    </details>`;
  }

  const nodeHtml = g.nodes.map((nd, i) => renderGraphNode(nd, i)).join('') ||
    '<div class="muted" style="font-size:12px;padding:6px 0">还没有节点。先加一个路由节点或 Agent 节点。</div>';
  const ids = g.nodes.map((x) => x.id).filter(Boolean);
  const edgeHtml = g.edges.map((e, i) => renderGraphEdge(e, i, ids)).join('') ||
    '<div class="muted" style="font-size:12px;padding:6px 0">还没有边。边决定数据流向；route 留空表示无条件。</div>';

  return `<details class="section" open>
    ${sectionHead(n, '编排', `${g.nodes.length} 节点 · ${g.edges.length} 边`, issues)}
    <div class="section-body" style="grid-template-columns:1fr">
      ${modePicker}

      <div class="field full">
        <div class="group-label">拓扑预览 <span class="mono">只读 · 实时反映编辑</span></div>
        <div class="topo-holder">${renderTopology(g)}</div>
      </div>

      <div class="field full">
        <div class="group-label">节点 <span class="mono">spec.graph.nodes</span></div>
        <div id="graph-nodes">${nodeHtml}</div>
        <div class="field-actions" style="margin-top:8px">
          ${NODE_KINDS.map(([k, label]) => `<button class="btn small" data-add-node="${k}" type="button">${ICON.plus} ${label}</button>`).join('')}
        </div>
      </div>

      <div class="field full">
        <div class="group-label">边 <span class="mono">spec.graph.edges</span></div>
        <div id="graph-edges">${edgeHtml}</div>
        <div class="field-actions" style="margin-top:8px">
          <button class="btn small" id="add-edge" type="button">${ICON.plus} 添加边</button>
          <span class="hint">from 可填 START。route 填路由节点打出的标签；留空即无条件边。</span>
        </div>
      </div>
    </div>
  </details>`;
}

function renderGraphNode(nd, i) {
  const base = `spec.graph.nodes.${i}`;
  const kind = nd.kind || 'agent';

  let body = `<div class="field">
    <label>描述 <span class="path">description</span></label>
    <input data-path="${base}.description" value="${esc(nd.description || '')}" placeholder="这一步做什么">
  </div>`;

  if (kind === 'agent') {
    body += `
      <div class="field full">
        <label>指令 <span class="path">instruction</span></label>
        <textarea data-path="${base}.instruction" rows="4" spellcheck="false">${esc(nd.instruction || '')}</textarea>
      </div>
      <div class="field">
        <label>模型覆盖 <span class="path">model.name</span></label>
        <input data-path="${base}.model.name" value="${esc(nd.model && nd.model.name || '')}" placeholder="留空继承默认模型">
      </div>
      <div class="field">
        <label>工具子集 <span class="path">tools</span></label>
        <input data-path="${base}.tools" data-tags="1" value="${esc((nd.tools || []).join(', '))}" placeholder="留空 = 全部">
      </div>`;
  } else if (kind === 'tool') {
    body += `
      <div class="field full">
        <label>工具名 <span class="path">tool</span></label>
        <input data-path="${base}.tool" value="${esc(nd.tool || '')}" placeholder="spec.tools.http 里的名字，或 MCP 工具名">
        <span class="hint">模型侧内置工具（google_search 等）做不了节点：它们由 provider 在生成时执行，没有可转发的结果。</span>
      </div>`;
  } else if (kind === 'route') {
    const rules = (nd.rules || []).map((r, j) => `
      <div class="param-row">
        <select data-path="${base}.rules.${j}.op">
          ${['contains', 'equals', 'prefix', 'suffix', 'regex', 'always'].map((o) => `<option ${r.op === o ? 'selected' : ''}>${o}</option>`).join('')}
        </select>
        <input data-path="${base}.rules.${j}.match" value="${esc(r.match || '')}" placeholder="匹配内容">
        <input data-path="${base}.rules.${j}.route" value="${esc(r.route || '')}" placeholder="路由标签">
        <button class="btn small danger" data-del-rule="${i}:${j}" type="button" title="删除规则">×</button>
      </div>`).join('');
    body += `
      <div class="field full">
        <label>规则 <span class="path">按顺序匹配，第一条命中生效</span></label>
        <div>${rules || '<div class="muted" style="font-size:12px">没有规则。至少加一条，最后一条建议用 always 兜底。</div>'}</div>
        <button class="btn small" data-add-rule="${i}" type="button">${ICON.plus} 添加规则</button>
      </div>
      <div class="field">
        <label>输入来源 <span class="path">input</span></label>
        <input data-path="${base}.input" value="${esc(nd.input || '')}" placeholder="留空 = 上游输出；填 state key">
      </div>`;
  } else if (kind === 'subgraph') {
    body += `<div class="field full">
      <span class="hint">子图请切到 YAML 标签页编辑（spec.graph.nodes[${i}].sub）。图形化编辑留待后续版本。</span>
    </div>`;
  }

  return `<div class="tool-card graph-node">
    <div class="tool-head">
      <span class="method ${kind === 'route' ? 'PUT' : kind === 'tool' ? 'GET' : 'POST'}">${esc(kind)}</span>
      <input class="tool-name" data-path="${base}.id" value="${esc(nd.id || '')}" placeholder="节点 id" spellcheck="false">
      <select data-path="${base}.kind" data-kind-change="${i}">
        ${NODE_KINDS.map(([k, label]) => `<option value="${k}" ${kind === k ? 'selected' : ''}>${label}</option>`).join('')}
      </select>
      <button class="btn small danger" data-del-node="${i}" type="button" title="删除节点">×</button>
    </div>
    <div class="tool-body">${body}</div>
  </div>`;
}

function renderGraphEdge(e, i, ids) {
  const base = `spec.graph.edges.${i}`;
  const opts = (sel) => ['', 'START', ...ids]
    .map((o) => `<option value="${esc(o)}" ${sel === o ? 'selected' : ''}>${o === '' ? '— 选择 —' : esc(o)}</option>`).join('');
  return `<div class="param-row edge-row">
    <select data-path="${base}.from">${opts(e.from)}</select>
    <input data-path="${base}.route" value="${esc(e.route || '')}" placeholder="route（可空）" spellcheck="false">
    <span class="mono muted">→</span>
    <select data-path="${base}.to">${opts(e.to)}</select>
    <button class="btn small danger" data-del-edge="${i}" type="button" title="删除边">×</button>
  </div>`;
}

// renderTopology draws the graph from the manifest in the editor, not from the
// server. Editing a node reflows the diagram immediately, which is the point:
// a stale preview reads as a defect in the editor rather than in the graph.
function renderTopology(g) {
  const nodes = g.nodes || [];
  const edges = g.edges || [];
  if (!nodes.length) return '<div class="muted" style="font-size:12px;padding:8px 0">还没有节点。</div>';

  const adj = {};
  edges.forEach((e) => { (adj[e.from] = adj[e.from] || []).push(e.to); });

  const depth = { START: -1 };
  const queue = ['START'];
  while (queue.length) {
    const cur = queue.shift();
    (adj[cur] || []).forEach((to) => {
      if (depth[to] === undefined) { depth[to] = depth[cur] + 1; queue.push(to); }
    });
  }

  const layers = {};
  nodes.forEach((nd) => {
    const d = depth[nd.id] === undefined ? -2 : depth[nd.id];
    (layers[d] = layers[d] || []).push(nd);
  });

  const NW = 138, NH = 40, GX = 22, GY = 92, PADX = 24, TOP = 46;
  const keys = Object.keys(layers).map(Number).sort((a, b) => a - b);
  const maxRow = keys.reduce((m, k) => Math.max(m, layers[k].length), 1);
  const W = Math.max(600, PADX * 2 + maxRow * NW + (maxRow - 1) * GX);
  const H = TOP + keys.length * NH + Math.max(0, keys.length - 1) * GY + 24;

  const pos = {};
  keys.forEach((k, row) => {
    const rowNodes = layers[k];
    const rowW = rowNodes.length * NW + (rowNodes.length - 1) * GX;
    const startX = (W - rowW) / 2;
    rowNodes.forEach((nd, col) => {
      pos[nd.id] = { x: startX + col * (NW + GX), y: TOP + row * (NH + GY), nd };
    });
  });

  const SX = W / 2, SY = 10;
  // Fills come from the theme so the diagram reads the same in light and dark;
  // the accent is carried by the border, which is what distinguishes the kinds.
  const KIND_STROKE = {
    agent: 'var(--accent)',
    tool: 'var(--ok)',
    route: 'var(--warn)',
    join: 'var(--fg-3)',
    subgraph: '#a78bfa',
  };

  let svg = `<svg class="topo" viewBox="0 0 ${W} ${H}" width="100%" role="img" aria-label="graph topology">`;
  svg += `<defs><marker id="topo-arrow" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="5" markerHeight="5" orient="auto-start-reverse">
    <path d="M2 1L8 5L2 9" fill="none" stroke="var(--fg-3)" stroke-width="1.4" stroke-linecap="round"/></marker></defs>`;
  svg += `<circle cx="${SX}" cy="${SY}" r="6" fill="var(--fg-2)"/>`;
  svg += `<text x="${SX + 12}" y="${SY + 4}" class="topo-label">START</text>`;

  edges.forEach((e) => {
    const a = e.from === 'START' ? { x: SX, y: SY, start: true } : pos[e.from];
    const b = pos[e.to];
    if (!a || !b) return;
    const x1 = a.start ? a.x : a.x + NW / 2;
    const y1 = a.start ? a.y + 6 : a.y + NH;
    const x2 = b.x + NW / 2;
    const y2 = b.y;
    svg += `<path d="M ${x1} ${y1} C ${x1} ${y1 + 30}, ${x2} ${y2 - 30}, ${x2} ${y2}" fill="none" stroke="var(--fg-3)" stroke-width="1" marker-end="url(#topo-arrow)"/>`;
    if (e.route) {
      const mx = (x1 + x2) / 2, my = (y1 + y2) / 2;
      svg += `<text x="${mx}" y="${my - 4}" text-anchor="middle" class="topo-route">${esc(e.route)}</text>`;
    }
  });

  nodes.forEach((nd) => {
    const p = pos[nd.id];
    if (!p) return;
    const stroke = KIND_STROKE[nd.kind] || 'var(--fg-3)';
    const unreachable = depth[nd.id] === undefined;
    svg += `<g class="topo-node${unreachable ? ' unreachable' : ''}">
      <rect x="${p.x}" y="${p.y}" width="${NW}" height="${NH}" rx="8" fill="var(--bg-3)" stroke="${stroke}" stroke-width="1"/>`;
    if (unreachable) {
      svg += `<text x="${p.x + NW - 8}" y="${p.y + 13}" text-anchor="end" class="topo-bad">不可达</text>`;
    }
    svg += `<text x="${p.x + NW / 2}" y="${p.y + 17}" text-anchor="middle" class="topo-title">${esc(nd.id || '?')}</text>
      <text x="${p.x + NW / 2}" y="${p.y + 31}" text-anchor="middle" class="topo-kind" fill="${stroke}">${esc(nd.kind || '')}</text>
    </g>`;
  });

  svg += '</svg>';
  return svg;
}

// refreshTopology redraws only the diagram. A node or edge edit must be visible
// immediately, but re-rendering the whole form would drop the focus out of the
// field the user is typing in — which is the one thing an editor must never do.
function refreshTopology() {
  const holder = $('.topo-holder');
  if (!holder) return;
  holder.innerHTML = renderTopology(state.manifest.spec.graph || { nodes: [], edges: [] });
}

function renderPluginsSection(n) {
  const plugins = state.manifest.spec.plugins || [];
  const issues = issuesFor(['spec.plugins']);

  const items = plugins.map((p, i) => {
    const base = `spec.plugins.${i}`;
    const cfg = p.config ? Object.entries(p.config).map(([k, v]) => `${k}: ${v}`).join('\n') : '';
    return `<div class="tool-card">
      <div class="tool-head">
        <span class="method PUT">PLUGIN</span>
        <span class="tool-name">${esc(p.name || '未命名插件')}</span>
        <span class="tool-host">${esc(p.kind || '')}</span>
        <span class="spacer"></span>
        <button class="btn small danger" data-del-plugin="${i}" type="button">删除</button>
      </div>
      <div class="tool-body">
        <div class="field">
          <label>插件名</label>
          <input data-path="${base}.name" value="${esc(p.name || '')}" placeholder="logging" spellcheck="false">
        </div>
        <div class="field">
          <label>类型</label>
          <select data-path="${base}.kind">
            ${['builtin.logging', 'builtin.retryreflect', 'audit.webhook'].map((k) => `<option ${p.kind === k ? 'selected' : ''}>${k}</option>`).join('')}
          </select>
        </div>
        <div class="field full">
          <label>config <span class="path">每行 key: value</span></label>
          <textarea data-path="${base}.config" data-headers="1" rows="2" placeholder="url: https://hooks.example.com/audit" spellcheck="false">${esc(cfg)}</textarea>
        </div>
      </div>
    </div>`;
  }).join('') || '<div class="muted" style="font-size:12px;padding:6px 0">未启用插件。审计日志始终写入项目运行目录。</div>';

  return `<details class="section">
    ${sectionHead(n, '插件', `${plugins.length} 个`, issues)}
    <div class="section-body" style="grid-template-columns:1fr">
      <div id="plugin-list">${items}</div>
      <div><button class="btn small" id="add-plugin" type="button">${ICON.plus} 添加插件</button></div>
    </div>
  </details>`;
}

/* ------------------------------------------------------------- form bind */

function bindForm() {
  $$('#form-root [data-path]').forEach((el) => {
    const path = el.dataset.path;
    const handler = () => {
      let v;
      if (el.type === 'checkbox') v = el.checked;
      else if (el.dataset.number) v = el.value === '' ? undefined : Number(el.value);
      else if (el.dataset.tags) v = el.value.split(',').map((s) => s.trim()).filter(Boolean);
      else if (el.dataset.headers) v = parseKV(el.value);
      else v = el.value;
      setPath(state.manifest, path, v);
      markDirty();
      // Keep the topology in step with node and edge edits without re-rendering
      // the form, which would steal focus out of the field being typed in.
      if (path.indexOf('spec.graph.') === 0) refreshTopology();
    };
    el.addEventListener('change', handler);
    if (el.tagName === 'INPUT' && el.type !== 'checkbox') el.addEventListener('input', handler);
  });

  $$('#form-root [data-bool]').forEach((el) => {
    // data-bool checkboxes write a boolean even when false, so the manifest keeps
    // the field instead of losing it to pruneEmpty.
    el.addEventListener('change', () => {
      const keys = el.dataset.path.split('.');
      let cur = state.manifest;
      for (let i = 0; i < keys.length - 1; i++) {
        if (cur[keys[i]] == null) cur[keys[i]] = {};
        cur = cur[keys[i]];
      }
      cur[keys[keys.length - 1]] = el.checked;
      markDirty();
    });
  });

  const ts = $('#toolsearch');
  if (ts) ts.addEventListener('change', () => {
    if (ts.checked) {
      const conflicted = (state.manifest.spec.tools.mcp || []).some((m) => (m.toolAllow || []).length);
      if (conflicted) {
        toast('toolSearch 与 toolAllow 互斥，已清空所有 MCP 的 toolAllow', true);
        (state.manifest.spec.tools.mcp || []).forEach((m) => delete m.toolAllow);
      }
    }
    state.manifest.spec.tools.toolSearch = ts.checked;
    markDirty();
    renderForm();
  });

  $$('#form-root [data-builtin]').forEach((el) => {
    el.addEventListener('change', () => {
      const t = state.manifest.spec.tools;
      const set = new Set(t.builtin || []);
      if (el.checked) set.add(el.dataset.builtin); else set.delete(el.dataset.builtin);
      t.builtin = Array.from(set);
      markDirty();
      renderProjectHead();
    });
  });

  // Graph editor wiring. Every mutation re-renders the whole form because node
  // count, edge options and the topology all shift together.
  $$('#form-root input[name="graph-mode"]').forEach((el) => {
    el.addEventListener('change', () => {
      state.manifest.spec.mode = el.value;
      markDirty();
      renderForm();
      renderProjectHead();
    });
  });

  $$('#form-root [data-add-node]').forEach((el) => {
    el.onclick = () => {
      const kind = el.dataset.addNode;
      const g = state.manifest.spec.graph;
      const node = { id: `node_${g.nodes.length + 1}`, kind };
      Object.assign(node, NODE_DEFAULTS[kind] ? NODE_DEFAULTS[kind]() : {});
      g.nodes.push(node);
      markDirty();
      renderForm();
    };
  });

  $$('#form-root [data-del-node]').forEach((el) => {
    el.onclick = () => {
      state.manifest.spec.graph.nodes.splice(Number(el.dataset.delNode), 1);
      markDirty();
      renderForm();
    };
  });

  $$('#form-root [data-kind-change]').forEach((el) => {
    el.addEventListener('change', () => {
      const nd = state.manifest.spec.graph.nodes[Number(el.dataset.kindChange)];
      const keep = { id: nd.id, description: nd.description, kind: el.value };
      Object.keys(nd).forEach((k) => { if (!(k in keep)) delete nd[k]; });
      Object.assign(nd, NODE_DEFAULTS[el.value] ? NODE_DEFAULTS[el.value]() : {});
      markDirty();
      renderForm();
    });
  });

  $$('#form-root [data-add-rule]').forEach((el) => {
    el.onclick = () => {
      const nd = state.manifest.spec.graph.nodes[Number(el.dataset.addRule)];
      nd.rules = nd.rules || [];
      nd.rules.push({ op: 'contains', match: '', route: 'go' });
      markDirty();
      renderForm();
    };
  });

  $$('#form-root [data-del-rule]').forEach((el) => {
    el.onclick = () => {
      const [i, j] = el.dataset.delRule.split(':').map(Number);
      state.manifest.spec.graph.nodes[i].rules.splice(j, 1);
      markDirty();
      renderForm();
    };
  });

  const addEdge = $('#add-edge');
  if (addEdge) addEdge.onclick = () => {
    state.manifest.spec.graph.edges.push({ from: 'START', to: '', route: '' });
    markDirty();
    renderForm();
  };

  $$('#form-root [data-del-edge]').forEach((el) => {
    el.onclick = () => {
      state.manifest.spec.graph.edges.splice(Number(el.dataset.delEdge), 1);
      markDirty();
      renderForm();
    };
  });

  const addHTTP = $('#add-http');
  if (addHTTP) addHTTP.onclick = () => {
    const t = state.manifest.spec.tools;
    t.http = t.http || [];
    t.http.push({
      name: 'new_tool', description: 'Describe what this tool does for the model, in at least 15 characters',
      method: 'GET', url: 'https://api.example.com/v1/items', allowedHosts: ['api.example.com'],
      timeoutSec: 20, maxRespBytes: 65536, params: [],
    });
    markDirty();
    renderForm();
  };

  $$('#form-root [data-del-http]').forEach((el) => {
    el.onclick = () => {
      state.manifest.spec.tools.http.splice(Number(el.dataset.delHttp), 1);
      markDirty();
      renderForm();
    };
  });

  $$('#form-root [data-add-param]').forEach((el) => {
    el.onclick = () => {
      const t = state.manifest.spec.tools.http[Number(el.dataset.addParam)];
      t.params = t.params || [];
      t.params.push({ name: 'param', type: 'string', in: 'query', required: false, description: '' });
      markDirty();
      renderForm();
    };
  });

  $$('#form-root [data-del-param]').forEach((el) => {
    el.onclick = () => {
      const [i, j] = el.dataset.delParam.split(':').map(Number);
      state.manifest.spec.tools.http[i].params.splice(j, 1);
      markDirty();
      renderForm();
    };
  });

  const addMCP = $('#add-mcp');
  if (addMCP) addMCP.onclick = () => {
    const t = state.manifest.spec.tools;
    t.mcp = t.mcp || [];
    t.mcp.push({ id: 'mcp' + (t.mcp.length + 1), transport: 'stdio', command: 'npx', args: ['-y', '@modelcontextprotocol/server-filesystem', '.'], timeoutSec: 20 });
    markDirty();
    renderForm();
  };

  $$('#form-root [data-del-mcp]').forEach((el) => {
    el.onclick = () => {
      state.manifest.spec.tools.mcp.splice(Number(el.dataset.delMcp), 1);
      markDirty();
      renderForm();
    };
  });

  $$('#form-root [data-probe]').forEach((el) => {
    el.onclick = async () => {
      const i = Number(el.dataset.probe);
      const box = $(`[data-probe-result="${i}"]`);
      const ref = state.manifest.spec.tools.mcp[i];
      box.innerHTML = '<span class="spinner"></span> 连接中…';
      try {
        await saveProject(true);
        const data = await call('POST', `/api/projects/${state.slug}/probe/mcp`, { mcpId: ref.id });
        if (!data.ok) {
          box.innerHTML = `<div class="check-row err"><span class="st">✗</span><div>${esc(data.detail)}</div></div>`;
          return;
        }
        box.innerHTML = `<div class="muted mono">${data.count} 个工具 · ${data.durMs}ms</div>` +
          (data.tools || []).map((t) => `<div class="file-row"><span class="nm">${esc(t.name)}</span><span class="sz">${esc((t.description || '').slice(0, 48))}</span></div>`).join('');
      } catch (e) {
        box.innerHTML = `<div class="check-row err"><span class="st">✗</span><div>${esc(e.message)}</div></div>`;
      }
    };
  });

  const pm = $('[data-probe-model]');
  if (pm) pm.onclick = () => runProbe(pm, 'model', '[data-probe-model-result]', {});
  const ps = $('[data-probe-sql]');
  if (ps) ps.onclick = () => runProbe(ps, 'sql', '[data-probe-sql-result]', {});

  const addPlugin = $('#add-plugin');
  if (addPlugin) addPlugin.onclick = () => {
    state.manifest.spec.plugins = state.manifest.spec.plugins || [];
    state.manifest.spec.plugins.push({ name: 'logging', kind: 'builtin.logging' });
    markDirty();
    renderForm();
  };

  $$('#form-root [data-del-plugin]').forEach((el) => {
    el.onclick = () => {
      state.manifest.spec.plugins.splice(Number(el.dataset.delPlugin), 1);
      markDirty();
      renderForm();
    };
  });
}

async function runProbe(btn, kind, boxSel, body) {
  const box = $(boxSel);
  btn.disabled = true;
  box.innerHTML = '<div class="check-row"><span class="spinner"></span> <span>探测中…</span></div>';
  try {
    await saveProject(true);
    const data = await call('POST', `/api/projects/${state.slug}/probe/${kind}`, body);
    if (!data.ok) {
      box.innerHTML = `<div class="check-row err"><span class="st">✗</span><div>${esc(data.detail || '探测失败')}<div class="muted" style="margin-top:2px">${data.durMs || 0}ms</div></div></div>`;
      return;
    }
    box.innerHTML = `<div class="check-row ok"><span class="st">✓</span><div>${esc(data.detail || '通过')}<div class="muted" style="margin-top:2px">${data.durMs || 0}ms</div></div></div>`;
  } catch (e) {
    box.innerHTML = `<div class="check-row err"><span class="st">✗</span><div>${esc(e.message)}</div></div>`;
  } finally {
    btn.disabled = false;
  }
}

// parseKV turns "Key: value" lines into an object; a line without a colon is
// kept with an empty value rather than dropped, so typos stay visible.
function parseKV(text) {
  const out = {};
  String(text || '').split('\n').forEach((line) => {
    const s = line.trim();
    if (!s) return;
    const i = s.indexOf(':');
    if (i < 0) { out[s] = ''; return; }
    out[s.slice(0, i).trim()] = s.slice(i + 1).trim();
  });
  return out;
}

/* ---------------------------------------------------------------- saving */

function markDirty() {
  state.dirty = true;
  setSaveState('未保存', 'dirty');
}

function setSaveState(text, tone) {
  const el = $('#save-state');
  el.textContent = text;
  el.className = 'save-state' + (tone ? ' ' + tone : '');
}

async function saveProject(silent) {
  if (!state.slug || !state.manifest) return null;
  const payload = JSON.parse(JSON.stringify(state.manifest));
  pruneEmpty(payload);
  if (!payload.spec.tools) payload.spec.tools = {};

  try {
    const data = await call('PUT', `/api/projects/${state.slug}`, { manifest: payload });
    state.issues = data.issues || [];
    state.estimate = data.estimate || null;
    state.dirty = false;
    setSaveState('已保存', 'ok');
    renderIssues();
    renderProjectHead();
    await loadProjects();
    // Re-read so the YAML tab shows exactly what the server wrote.
    const fresh = await call('GET', `/api/projects/${state.slug}`);
    state.yaml = fresh.manifestYaml || '';
    state.manifest = fresh.manifest || state.manifest;
    state.versions = fresh.versions || [];
    state.estimate = fresh.estimate || state.estimate;
    $('#yaml-input').value = state.yaml;
    renderVersions();
    renderProjectHead();
    if (!silent) renderForm();
    return data;
  } catch (e) {
    setSaveState('保存失败', 'error');
    toast(e.message, true);
    return null;
  }
}

async function saveYAML() {
  if (!state.slug) return;
  const raw = $('#yaml-input').value;
  try {
    const data = await call('PUT', `/api/projects/${state.slug}`, { manifestYaml: raw });
    if (data.saved === false) {
      state.issues = data.issues || [];
      renderIssues();
      const first = state.issues.find((i) => i.level === 'error');
      setSaveState('YAML 解析失败', 'error');
      toast(first ? first.message : 'YAML 解析失败', true);
      return;
    }
    state.issues = data.issues || [];
    state.estimate = data.estimate || null;
    const fresh = await call('GET', `/api/projects/${state.slug}`);
    state.manifest = fresh.manifest || null;
    state.yaml = fresh.manifestYaml || raw;
    state.versions = fresh.versions || [];
    state.dirty = false;
    const bad = state.issues.some((i) => i.level === 'error');
    setSaveState(bad ? '有错误' : '已保存', bad ? 'error' : 'ok');
    renderForm();
    renderIssues();
    renderProjectHead();
    renderVersions();
    await loadProjects();
  } catch (e) {
    setSaveState('保存失败', 'error');
    toast(e.message, true);
  }
}

/* ---------------------------------------------------------------- issues */

function renderIssues() {
  const box = $('#issues');
  if (!state.manifest) { box.innerHTML = ''; return; }
  if (!state.issues.length) {
    box.innerHTML = `<div class="issue-ok">✓ 校验通过，没有发现问题</div>
      <div class="issue-sum">保存后会自动校验；导出前建议再用「导出」页跑一次确定性校验。</div>`;
    return;
  }
  const errs = state.issues.filter((i) => i.level === 'error').length;
  const warns = state.issues.length - errs;
  box.innerHTML = state.issues.map((i) => `<div class="issue ${esc(i.level)}">
    <span class="lvl">${i.level === 'error' ? 'ERROR' : 'WARN'}</span>
    <span class="p">${esc(i.path)}</span>
    <span class="m">${esc(i.message)}</span>
  </div>`).join('') + `<div class="issue-sum">${errs} 错误 · ${warns} 警告</div>`;
}

/* ------------------------------------------------------------------ chat */

function clearChat() {
  $('#chat').innerHTML = `<div class="msg sys" style="--err:var(--fg-3)">
    <div class="who">会话 ${esc(state.sessionId.slice(-8))}</div>
    <div class="bubble" style="background:transparent;border:1px dashed var(--line-2);color:var(--fg-3)">
      新会话。发一条消息试试——文本、工具调用、确认请求都会出现在这里。
    </div>
  </div>`;
  state.pending = null;
  state.toolCalls.clear();
}

function addMsg(cls, role) {
  const wrap = document.createElement('div');
  wrap.className = 'msg ' + cls;
  wrap.innerHTML = `<div class="who">${esc(role)}</div><div class="bubble"></div>`;
  $('#chat').appendChild(wrap);
  scrollChat();
  return wrap.querySelector('.bubble');
}

function scrollChat() {
  const c = $('#chat');
  c.scrollTop = c.scrollHeight;
}

// fmtTokens keeps a token count readable without hiding its magnitude.
function fmtTokens(n) {
  if (!n) return '0';
  return n >= 1000 ? `${(n / 1000).toFixed(1)}k` : String(n);
}

// refreshTally renders the project's running account above the chat.
//
// It reads the same file each turn appends to, so this line and the one under a
// finished turn can never disagree. A project that has never run simply shows
// nothing.
async function refreshTally() {
  const box = $('#play-tally');
  if (!box) return;
  if (!state.slug) {
    box.hidden = true;
    return;
  }
  try {
    const s = await call('GET', `/api/projects/${encodeURIComponent(state.slug)}/stats`);
    if (!s || !s.turns) {
      box.hidden = true;
      return;
    }
    const bits = [
      `累计 ${s.turns} 轮`,
      `↑${fmtTokens(s.promptTokens)} ↓${fmtTokens(s.outputTokens)} tok`,
    ];
    if (s.cacheHitRate) bits.push(`缓存 ${Math.round(s.cacheHitRate * 100)}%`);
    if (s.wallMs && s.wallMs.p50) bits.push(`p50 ${(s.wallMs.p50 / 1000).toFixed(1)}s`);
    if (s.wallMs && s.wallMs.p95) bits.push(`p95 ${(s.wallMs.p95 / 1000).toFixed(1)}s`);
    if (s.errTurns) bits.push(`${s.errTurns} 轮失败`);
    box.textContent = bits.join(' · ');
    box.title = JSON.stringify(s);
    box.hidden = false;
  } catch (e) {
    box.hidden = true;
  }
}

// ---- 回归用例 ----
// 只断言事实：工具是否被调用、回答里有没有某段文字。运行一次就是一次真实的
// 模型调用，所以按钮会在运行期间锁住，避免连点连烧。

function setEvalsState(text, isErr) {
  const el = $('#evals-state');
  if (!el) return;
  el.textContent = text || '';
  el.classList.toggle('err', !!isErr);
}

async function loadEvals() {
  const box = $('#evals-input');
  if (!box || !state.slug) return;
  try {
    const data = await call('GET', `/api/projects/${encodeURIComponent(state.slug)}/evals`);
    // Do not overwrite what the user is typing when the tab is re-entered.
    if (document.activeElement !== box) box.value = data.yaml || '';
    if (data.error) setEvalsState(`解析失败：${data.error}`, true);
    else if (data.cases && data.cases.length) setEvalsState(`${data.cases.length} 条用例`);
    else setEvalsState('还没有用例');
  } catch (e) {
    setEvalsState('读取失败', true);
  }
}

async function saveEvals() {
  if (!state.slug) return;
  try {
    const res = await call('PUT', `/api/projects/${encodeURIComponent(state.slug)}/evals`, {
      yaml: $('#evals-input').value,
    });
    setEvalsState(`${(res.cases || []).length} 条用例 · 已保存`);
    toast('用例已保存');
  } catch (e) {
    setEvalsState(`保存失败：${e.message}`, true);
  }
}

async function runEvals() {
  if (!state.slug) return;
  const btn = $('#btn-evals-run');
  btn.disabled = true;
  setEvalsState('运行中…');
  try {
    const rep = await call('POST', `/api/projects/${encodeURIComponent(state.slug)}/evals/run`, {
      yaml: $('#evals-input').value,
    });
    renderEvalReport(rep);
    setEvalsState(
      `${rep.passed}/${rep.total} 通过 · ${(rep.durMs / 1000).toFixed(1)}s`,
      rep.passed !== rep.total,
    );
  } catch (e) {
    setEvalsState(`运行失败：${e.message}`, true);
  } finally {
    btn.disabled = false;
  }
}

function renderEvalReport(rep) {
  const box = $('#evals-report');
  if (!box) return;
  const rows = (rep.cases || []).map((c) => {
    const mark = c.ok ? '✓' : '✗';
    const cls = c.ok ? 'ok' : 'err';
    const tools = (c.tools || []).length ? `工具：${c.tools.join(', ')}` : '工具：无';
    const cost = c.stats ? ` · ${c.stats.llmCalls} 次调用 · ↑${fmtTokens(c.stats.promptTokens)} ↓${fmtTokens(c.stats.outputTokens)}` : '';
    const fails = (c.failures || []).map((f) => `<li>${esc(f)}</li>`).join('');
    return `<div class="eval-case ${cls}">
      <div class="eval-line"><span class="mark">${mark}</span><span>${esc(c.name)}</span>
        <span class="muted">${tools}${cost}</span></div>
      ${fails ? `<ul class="eval-fails">${fails}</ul>` : ''}
    </div>`;
  });
  box.innerHTML = rows.join('');
}

// renderTurnStats is the turn's account: what it cost and what it felt like.
// Every number is measured by the server, never estimated.
function renderTurnStats(wrap, st) {
  const bits = [];
  bits.push(`${st.llmCalls || 0} 次调用`);
  bits.push(`↑${fmtTokens(st.promptTokens)} ↓${fmtTokens(st.outputTokens)} tok`);
  if (st.cachedTokens) bits.push(`缓存 ${fmtTokens(st.cachedTokens)}`);
  if (st.firstByteMs) bits.push(`首字 ${st.firstByteMs}ms`);
  if (st.errCode) bits.push(st.errCode);
  const tools = (st.tools || []).map((t) => {
    const errs = t.errors ? ` /${t.errors}✗` : '';
    return `${t.name} ${t.calls}×${errs} ${t.durMs}ms`;
  });
  if (tools.length) bits.push(tools.join(' · '));

  const el = document.createElement('div');
  el.className = 'turn-stats';
  el.textContent = bits.join(' · ');
  // The full object is one hover away, for copying into an issue.
  el.title = JSON.stringify(st);
  wrap.appendChild(el);
  scrollChat();
}

function setPlayState(text) { $('#play-state').textContent = text; }

async function send() {
  if (state.busy || !state.slug) return;
  const box = $('#msg');
  const message = box.value.trim();
  if (!message) return;
  box.value = '';
  addMsg('user', '你').textContent = message;

  state.busy = true;
  state.turnStart = performance.now();
  setPlayState('运行中…');
  const bubble = addMsg('agent', state.manifest?.spec?.agent?.name || 'agent');
  bubble.textContent = '';

  try {
    const resp = await fetch(`/api/projects/${encodeURIComponent(state.slug)}/play`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ sessionId: state.sessionId, message }),
    });
    if (!resp.ok) {
      const text = await resp.text();
      let msg = text;
      try {
        const j = JSON.parse(text);
        msg = j.error || j.message || text;
      } catch (e) { /* 非 JSON 就用原文 */ }
      bubble.parentElement.remove();
      addMsg('sys', 'error').textContent = msg;
      return;
    }
    await readSSE(resp, (ev) => handleEvent(ev, bubble));
  } catch (e) {
    bubble.textContent += (bubble.textContent ? '\n' : '') + '连接失败：' + e.message;
  } finally {
    state.busy = false;
    setPlayState('');
  }
}

async function readSSE(resp, onEvent) {
  const reader = resp.body.getReader();
  const decoder = new TextDecoder();
  let buf = '';
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });
    let idx;
    while ((idx = buf.indexOf('\n\n')) >= 0) {
      const chunk = buf.slice(0, idx);
      buf = buf.slice(idx + 2);
      const line = chunk.split('\n').find((l) => l.startsWith('data: '));
      if (!line) continue;
      try { onEvent(JSON.parse(line.slice(6))); } catch (e) { /* ignore malformed frame */ }
    }
  }
}

function addToolCall(ev) {
  const el = document.createElement('details');
  el.className = 'tool-call';
  el.innerHTML = `<summary>
      <span class="arrow">→</span>
      <span>${esc(ev.tool)}</span>
      <span class="muted" style="font-size:10.5px">${esc(summarizeArgs(ev.args))}</span>
      <span class="dur muted" style="font-size:10.5px;margin-left:auto"></span>
    </summary>
    <pre>${esc(JSON.stringify(ev.args || {}, null, 2))}</pre>`;
  $('#chat').appendChild(el);
  if (ev.callId) state.toolCalls.set(ev.callId, el);
  scrollChat();
  return el;
}

function summarizeArgs(args) {
  if (!args) return '';
  const keys = Object.keys(args);
  if (!keys.length) return '';
  const first = keys.slice(0, 2).map((k) => `${k}=${JSON.stringify(args[k])}`).join(' ');
  return first.length > 64 ? first.slice(0, 62) + '…' : first;
}

function handleEvent(ev, bubble) {
  switch (ev.type) {
    case 'text':
      bubble.textContent += ev.text;
      break;
    case 'tool_call':
      addToolCall(ev);
      break;
    case 'tool_result': {
      const ok = ev.ok !== false;
      const card = ev.callId ? state.toolCalls.get(ev.callId) : null;
      if (card) {
        card.classList.add(ok ? 'ok' : 'err');
        const icon = card.querySelector('.arrow');
        if (icon) icon.textContent = ok ? '✓' : '✗';
        const dur = card.querySelector('.dur');
        if (dur && ev.durMs) dur.textContent = `${ev.durMs}ms`;
        if (ev.code) {
          const pre = card.querySelector('pre');
          if (pre) pre.textContent += `\n\n// ${ev.code}`;
        }
        // The tool's own reply, capped server-side. Without it the only way to see
        // what a tool returned was to read the audit log, which stores a hash.
        if (ev.result) {
          const out = document.createElement('pre');
          out.className = 'result';
          out.textContent = ev.result;
          card.appendChild(out);
        }
      } else {
        const el = document.createElement('div');
        el.className = 'tool-call ' + (ok ? 'ok' : 'err');
        el.innerHTML = `<summary><span class="arrow">${ok ? '✓' : '✗'}</span><span>${esc(ev.tool)}</span>${ev.code ? `<span class="muted">${esc(ev.code)}</span>` : ''}</summary>`;
        $('#chat').appendChild(el);
      }
      break;
    }
    case 'confirm_required':
      showConfirm(ev);
      break;
    case 'error':
      addMsg('sys', 'error').textContent = (ev.code ? `[${ev.code}] ` : '') + (ev.text || '未知错误');
      break;
    case 'done': {
      setPlayState('');
      const wrap = bubble.parentElement;
      const who = wrap && wrap.querySelector('.who');
      // The server measures the turn; only fall back to the local clock when it
      // did not send an account.
      const wallMs = ev.stats && ev.stats.wallMs;
      if (who && (wallMs || state.turnStart)) {
        const secs = wallMs ? wallMs / 1000 : (performance.now() - state.turnStart) / 1000;
        who.textContent += ` · ${secs.toFixed(1)}s`;
        state.turnStart = 0;
      }
      if (wrap && ev.stats) renderTurnStats(wrap, ev.stats);
      refreshTally();
      break;
    }
  }
  scrollChat();
}

function showConfirm(ev) {
  const card = document.createElement('div');
  card.className = 'confirm-card';
  card.innerHTML = `<div class="title">⚠ 需要确认：${esc(ev.tool)}</div>
    <pre>${esc(JSON.stringify(ev.args || {}, null, 2))}</pre>
    <div class="row">
      <button class="btn small primary" data-approve="1" type="button">同意执行</button>
      <button class="btn small" data-approve="0" type="button">拒绝</button>
    </div>`;
  $('#chat').appendChild(card);
  scrollChat();
  card.querySelectorAll('[data-approve]').forEach((btn) => {
    btn.onclick = async () => {
      const approved = btn.dataset.approve === '1';
      const buttons = card.querySelectorAll('button');
      buttons.forEach((b) => { b.disabled = true; });
      try {
        const data = await call('POST', `/api/projects/${encodeURIComponent(state.slug)}/play/confirm`,
          { callId: ev.callId, sessionId: state.sessionId, approved });
        if (!data || data.ok !== true) {
          throw new Error('确认未被接受，工具可能已超时');
        }
        card.remove();
      } catch (e) {
        toast(e.message, true);
        buttons.forEach((b) => { b.disabled = false; });
      }
    };
  });
}

/* ------------------------------------------------------------ export/verify */

async function runVerify() {
  const box = $('#verify-result');
  box.innerHTML = '<div class="check-row"><span class="spinner"></span><span>go build / vet 进行中，首次会久一点…</span></div>';
  try {
    const smoke = $('#verify-smoke').checked;
    const data = await call('POST', `/api/projects/${state.slug}/verify`, { smoke });
    box.innerHTML = (data.checks || []).map((c) => {
      const ok = c.OK !== undefined ? c.OK : c.ok;
      return `<div class="check-row ${ok ? 'ok' : 'err'}">
        <span class="st">${ok ? '✓' : '✗'}</span>
        <div><div>${esc(c.name)} <span class="muted">${c.durMs || 0}ms</span></div>${ok ? '' : `<pre>${esc(c.detail || '')}</pre>`}</div>
      </div>`;
    }).join('') + (data.dir ? `<div class="muted mono" style="font-size:10.5px;margin-top:2px">${esc(data.dir)}</div>` : '');
  } catch (e) {
    box.innerHTML = `<div class="check-row err"><span class="st">✗</span><div>${esc(e.message)}</div></div>`;
  }
}

async function exportDir() {
  const box = $('#export-result');
  box.innerHTML = '<div class="check-row"><span class="spinner"></span><span>生成中…</span></div>';
  try {
    const data = await call('POST', `/api/projects/${state.slug}/export`, { zip: false });
    box.innerHTML = `<div class="check-row ok"><span class="st">✓</span><div>已导出 ${data.files} 个文件，${fmtBytes(data.bytes)}<div class="muted mono" style="font-size:10.5px;margin-top:2px">${esc(data.dir)}</div></div></div>`;
    loadFiles();
  } catch (e) {
    box.innerHTML = `<div class="check-row err"><span class="st">✗</span><div>${esc(e.message)}</div></div>`;
  }
}

async function exportZip() {
  const box = $('#export-result');
  box.innerHTML = '<div class="check-row"><span class="spinner"></span><span>打包中…</span></div>';
  try {
    const resp = await fetch(`/api/projects/${state.slug}/export`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ zip: true }),
    });
    if (!resp.ok) throw new Error(await resp.text());
    const blob = await resp.blob();
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url; a.download = `${state.slug}.zip`; a.click();
    URL.revokeObjectURL(url);
    box.innerHTML = '<div class="check-row ok"><span class="st">✓</span><div>zip 已开始下载</div></div>';
  } catch (e) {
    box.innerHTML = `<div class="check-row err"><span class="st">✗</span><div>${esc(e.message)}</div></div>`;
  }
}

function fmtBytes(n) {
  if (n == null) return '—';
  if (n < 1024) return n + ' B';
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB';
  return (n / 1024 / 1024).toFixed(2) + ' MB';
}

async function loadFiles() {
  if (!state.slug) return;
  try {
    const data = await call('GET', `/api/projects/${state.slug}/files`);
    const files = data.files || [];
    const groups = new Map();
    files.forEach((f) => {
      const i = f.path.lastIndexOf('/');
      const dir = i < 0 ? '.' : f.path.slice(0, i);
      if (!groups.has(dir)) groups.set(dir, []);
      groups.get(dir).push(f);
    });
    const html = Array.from(groups.entries()).map(([dir, list]) => `
      <div class="file-group">
        <div class="file-group-head">${esc(dir)}/ · ${list.length}</div>
        ${list.map((f) => {
          const name = dir === '.' ? f.path : f.path.slice(dir.length + 1);
          return `<div class="file-row"><span class="nm">${esc(name)}</span><span class="sz">${fmtBytes(f.bytes)}</span></div>`;
        }).join('')}
      </div>`).join('');
    $('#file-list').innerHTML = html || '<div class="muted">还没有导出。先点「导出到目录」。</div>';
    $('#file-summary').textContent = `${files.length} 个文件 · ${fmtBytes(data.totalBytes)}`;
  } catch (e) {
    $('#file-list').innerHTML = `<div class="check-row err"><span class="st">✗</span><div>${esc(e.message)}</div></div>`;
  }
}

function fmtVersion(v) {
  const m = /^(\d{4})(\d{2})(\d{2})T(\d{2})(\d{2})(\d{2})/.exec(v || '');
  if (!m) return v;
  const d = new Date(Date.UTC(+m[1], +m[2] - 1, +m[3], +m[4], +m[5], +m[6]));
  const p = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

function renderVersions() {
  const box = $('#version-list');
  if (!box) return;
  if (!state.versions.length) {
    box.innerHTML = '<div class="muted" style="font-size:12px">还没有快照。每次保存都会生成一个。</div>';
    return;
  }
  box.innerHTML = state.versions.slice(0, 30).map((v) => `<div class="version-row">
    <span class="v-name">${esc(fmtVersion(v))}</span>
    <span class="v-time">${esc(v)}</span>
    <button class="btn small" data-rollback="${esc(v)}" type="button">回滚</button>
  </div>`).join('');
  box.querySelectorAll('[data-rollback]').forEach((btn) => {
    btn.onclick = async () => {
      if (!confirm('回滚会先把当前状态存一份快照，再恢复到所选版本。继续？')) return;
      btn.disabled = true;
      try {
        const data = await call('POST', `/api/projects/${state.slug}/rollback`, { version: btn.dataset.rollback });
        if (data.ok === false) {
          toast('回滚失败：该快照无法解析', true);
          btn.disabled = false;
          return;
        }
        state.manifest = data.manifest;
        state.yaml = data.manifestYaml;
        state.issues = data.issues || [];
        state.estimate = data.estimate || null;
        state.versions = (await call('GET', `/api/projects/${state.slug}`)).versions || [];
        $('#yaml-input').value = state.yaml;
        renderForm(); renderIssues(); renderVersions(); renderProjectHead();
        await loadProjects();
        toast('已回滚');
      } catch (e) { toast(e.message, true); btn.disabled = false; }
    };
  });
}

/* ------------------------------------------------------------------- misc */

function toast(msg, isErr) {
  const el = document.createElement('div');
  el.textContent = msg;
  el.style.cssText = `position:fixed;bottom:18px;left:50%;transform:translateX(-50%);z-index:99;
    background:${isErr ? 'var(--err)' : 'var(--bg-4)'};color:${isErr ? '#fff' : 'var(--fg)'};
    border:1px solid ${isErr ? 'transparent' : 'var(--line-2)'};padding:9px 15px;border-radius:9px;
    font-size:12.5px;box-shadow:var(--shadow-2);max-width:70vw;animation:none`;
  document.body.appendChild(el);
  setTimeout(() => el.remove(), 3600);
}

function showTab(tab) {
  state.tab = tab;
  $$('#tabs .tab').forEach((b) => b.classList.toggle('active', b.dataset.tab === tab));
  $$('.tab-pane').forEach((p) => { p.hidden = p.dataset.pane !== tab; });
  if (tab === 'export') loadFiles();
  if (tab === 'versions') renderVersions();
  if (tab === 'evals') loadEvals();
  if (tab === 'yaml') { $('#yaml-input').value = state.yaml; updateYamlMeta(); }
}

function updateYamlMeta() {
  const el = $('#yaml-meta');
  if (!el) return;
  const v = $('#yaml-input').value;
  const lines = v ? v.split('\n').length : 0;
  el.textContent = `${lines} 行 · ${v.length} 字符${state.dirty ? ' · 未保存' : ''}`;
}

function toggleTheme() {
  const next = document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark';
  document.documentElement.dataset.theme = next;
  localStorage.setItem('adks.theme', next);
}

// Below 1080px the trial panel floats over the form, so it starts collapsed:
// covering half the editor before the user asked for it is the wrong default.
function setPlayCollapsed(collapsed, persist) {
  const panel = $('.panel.play');
  const toggle = $('#btn-play-toggle');
  if (!panel) return;
  panel.classList.toggle('collapsed', collapsed);
  if (toggle) toggle.style.transform = collapsed ? 'rotate(180deg)' : '';
  if (persist) localStorage.setItem('adks.playCollapsed', collapsed ? '1' : '0');
}

/* ------------------------------------------------------------------- boot */

function bindGlobal() {
  $('#btn-new').onclick = () => { $('#new-slug').focus(); };
  $('#btn-create').onclick = createProject;
  $('#new-slug').addEventListener('keydown', (e) => { if (e.key === 'Enter') createProject(); });
  $('#btn-save').onclick = () => { state.tab === 'yaml' ? saveYAML() : saveProject(); };
  $('#btn-reload').onclick = () => { if (state.slug) openProject(state.slug); };
  $('#btn-delete').onclick = deleteProject;
  $('#btn-send').onclick = send;
  $('#btn-theme').onclick = toggleTheme;
  $('#btn-search').onclick = () => { $('#project-filter').focus(); $('#project-filter').select(); };

  $('#btn-copy-slug').onclick = async () => {
    const slug = $('#proj-slug').textContent;
    try { await navigator.clipboard.writeText(slug); toast('已复制 ' + slug); }
    catch (e) { toast('复制失败：' + e.message, true); }
  };

  $('#msg').addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send(); }
  });
  $('#btn-reset-session').onclick = () => { state.sessionId = newSessionId(); clearChat(); };

  const playPanel = $('.panel.play');
  const toggle = $('#btn-play-toggle');
  if (toggle) toggle.onclick = () => setPlayCollapsed(!playPanel.classList.contains('collapsed'), true);

  const narrow = window.matchMedia('(max-width: 1080px)');
  const savedCollapse = localStorage.getItem('adks.playCollapsed');
  if (narrow.matches) setPlayCollapsed(savedCollapse !== '0', false);
  narrow.addEventListener('change', () => { if (narrow.matches) setPlayCollapsed(true, false); });

  $('#project-filter').addEventListener('input', (e) => {
    state.filter = e.target.value;
    renderProjects();
  });
  $('#project-filter').addEventListener('keydown', (e) => {
    if (e.key === 'Escape') { e.target.value = ''; state.filter = ''; renderProjects(); e.target.blur(); }
  });

  $('#btn-verify').onclick = runVerify;
  $('#btn-export-dir').onclick = exportDir;
  $('#btn-export-zip').onclick = exportZip;
  $('#btn-evals-save').onclick = saveEvals;
  $('#btn-evals-run').onclick = runEvals;

  $$('#tabs .tab').forEach((b) => { b.onclick = () => showTab(b.dataset.tab); });
  $('#yaml-input').addEventListener('input', () => { markDirty(); updateYamlMeta(); });

  document.addEventListener('keydown', (e) => {
    const mod = e.metaKey || e.ctrlKey;
    if (mod && e.key.toLowerCase() === 's') {
      e.preventDefault();
      state.tab === 'yaml' ? saveYAML() : saveProject();
      return;
    }
    if (mod && e.key.toLowerCase() === 'k') {
      e.preventDefault();
      $('#project-filter').focus();
      $('#project-filter').select();
      return;
    }
    if (e.key === '/' && !/^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement.tagName)) {
      e.preventDefault();
      $('#project-filter').focus();
    }
  });

  window.addEventListener('beforeunload', (e) => {
    if (state.dirty) { e.preventDefault(); e.returnValue = ''; }
  });
}

async function boot() {
  bindGlobal();
  clearChat();
  try { await loadProjects(); } catch (e) { toast(e.message, true); }
  if (state.projects.length) await openProject(state.projects[0].slug);
}

boot();
