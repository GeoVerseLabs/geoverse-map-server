const TYPES = [
  { code: "postgis", label: "PostGIS" },
  { code: "mysql", label: "MySQL 8" },
  { code: "geojson", label: "GeoJSON" },
  { code: "mbtiles", label: "MBTiles" },
  { code: "pmtiles", label: "PMTiles" },
  { code: "geopackage", label: "GeoPackage" },
];

const VIEWS = {
  sources: {
    eyebrow: "DATA INFRASTRUCTURE / 01",
    title: "数据源控制台",
    subtitle: "把数据库与静态归档接入同一套 XYZ、WMTS 与 OGC 分发接口。",
  },
  catalog: {
    eyebrow: "SERVICE DISTRIBUTION / 02",
    title: "服务目录",
    subtitle: "集中查看每个图层已经开放的切片、要素与归档分发地址。",
  },
  stats: {
    eyebrow: "RUNTIME OBSERVABILITY / 03",
    title: "运行统计",
    subtitle: "查看请求、缓存、数据源与 Go 运行时的即时状态。",
  },
};

const state = {
  sources: [],
  catalog: [],
  stats: null,
  view: "sources",
  type: "postgis",
  editing: null,
  writable: false,
};
const $ = (selector) => document.querySelector(selector);
const grid = $("#source-grid");
const form = $("#source-form");
const backdrop = $("#drawer-backdrop");
const fields = $("#connection-fields");
const probeResult = $("#probe-result");

const escapeHTML = (value = "") =>
  String(value).replace(/[&<>"']/g, (char) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[char]);

function authHeaders() {
  const key = $("#api-key").value.trim() || sessionStorage.getItem("geoverse-api-key") || "";
  return key ? { "X-API-Key": key } : {};
}

async function api(path, options = {}) {
  const response = await fetch(path, {
    ...options,
    headers: { Accept: "application/json", ...authHeaders(), ...(options.headers || {}) },
  });
  const text = await response.text();
  let body = {};
  try { body = text ? JSON.parse(text) : {}; } catch { body = { description: text }; }
  if (!response.ok) {
    if (response.status === 401) throw new Error("需要 API Key。请在左下角填写后重试。");
    throw new Error(body.description || `请求失败（HTTP ${response.status}）`);
  }
  return body;
}

function toast(message, error = false) {
  const node = $("#toast");
  node.textContent = message;
  node.className = `toast${error ? " error" : ""}`;
  node.hidden = false;
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => { node.hidden = true; }, 4200);
}

function setServerState(kind, text) {
  const parent = $("#server-state").parentElement;
  parent.className = `server-state ${kind}`;
  $("#server-state").textContent = text;
}

function refreshedAt() {
  return `更新于 ${new Date().toLocaleTimeString("zh-CN", { hour12: false })}`;
}

function setView(view, updateHash = true) {
  if (!VIEWS[view]) view = "sources";
  state.view = view;
  document.querySelectorAll(".view-pane").forEach((pane) => {
    pane.hidden = pane.id !== `view-${view}`;
  });
  document.querySelectorAll(".nav-item[data-view]").forEach((button) => {
    button.classList.toggle("active", button.dataset.view === view);
  });
  const meta = VIEWS[view];
  $("#page-eyebrow").textContent = meta.eyebrow;
  $("#page-title").textContent = meta.title;
  $("#page-subtitle").textContent = meta.subtitle;
  $("#add-source").hidden = view !== "sources";
  if (updateHash) history.replaceState(null, "", view === "sources" ? "#sources" : `#${view}`);
  if (view === "sources") loadSources();
  if (view === "catalog") loadCatalog();
  if (view === "stats") loadStats();
}

async function loadSources() {
  $("#refresh").disabled = true;
  try {
    const data = await api("/admin/sources");
    state.sources = data.sources || [];
    state.writable = Boolean(data.writable);
    renderSources();
    setServerState("ok", "服务连接正常");
    $("#last-refresh").textContent = refreshedAt();
  } catch (error) {
    grid.innerHTML = `<div class="empty">${escapeHTML(error.message)}</div>`;
    setServerState("error", "服务连接失败");
    toast(error.message, true);
  } finally {
    $("#refresh").disabled = false;
  }
}

function renderSources() {
  const sources = state.sources;
  $("#count-total").textContent = sources.length;
  $("#count-ok").textContent = sources.filter((source) => source.status === "ok").length;
  $("#count-tile").textContent = sources.filter((source) => source.tile).length;
  $("#write-state").textContent = state.writable ? "可写" : "只读";
  $("#add-source").disabled = !state.writable;
  if (!sources.length) {
    grid.innerHTML = '<div class="empty">尚未登记数据源。至少保留一个来源后，服务才能启动。</div>';
    return;
  }
  grid.innerHTML = sources.map((source, index) => `
    <article class="source-card" data-index="${String(index + 1).padStart(2, "0")}">
      <div class="card-top">
        <span class="source-type">${escapeHTML(source.type)}</span>
        <span class="health ${source.status === "ok" ? "" : "error"}" title="${escapeHTML(source.status_detail || "")}">
          ${source.status === "ok" ? "连接正常" : "连接异常"}
        </span>
      </div>
      <h3>${escapeHTML(source.title || source.name)}</h3>
      <p class="description">${escapeHTML(source.description || source.name)}</p>
      <div class="meta">
        <span class="tag">${source.tile ? "TILE" : "NO TILE"}</span>
        <span class="tag">${source.feature ? "FEATURES" : "ARCHIVE"}</span>
        ${source.min_zoom != null || source.max_zoom != null ? `<span class="tag">Z ${source.min_zoom ?? "auto"}–${source.max_zoom ?? "auto"}</span>` : ""}
      </div>
      ${source.archive ? `<a class="archive-link" href="${escapeHTML(source.archive)}" title="${escapeHTML(source.archive)}">${escapeHTML(source.archive)}</a>` : ""}
      <div class="card-actions">
        <button type="button" data-action="edit" data-name="${escapeHTML(source.name)}">配置</button>
        <button type="button" data-action="probe" data-name="${escapeHTML(source.name)}">测试</button>
        <button class="danger" type="button" data-action="delete" data-name="${escapeHTML(source.name)}">移除</button>
      </div>
    </article>
  `).join("");
}

function displayEndpoint(label, value, link = false) {
  if (!value) return "";
  const safe = escapeHTML(value);
  return `<div class="endpoint-row">
    <span>${escapeHTML(label)}</span>
    ${link
      ? `<a href="${safe}" target="_blank" rel="noreferrer">${safe}</a>`
      : `<code>${safe}</code>`}
    <button type="button" data-copy="${safe}" title="复制地址">复制</button>
  </div>`;
}

function renderCatalog() {
  const layers = state.catalog;
  $("#catalog-total").textContent = layers.length;
  $("#catalog-tile").textContent = layers.filter((layer) => layer.tiles).length;
  $("#catalog-feature").textContent = layers.filter((layer) => layer.items).length;
  $("#catalog-archive").textContent = layers.filter((layer) => layer.archive).length;
  const catalogGrid = $("#catalog-grid");
  if (!layers.length) {
    catalogGrid.innerHTML = '<div class="empty">当前没有可分发图层。请先登记至少一个数据源。</div>';
    return;
  }
  catalogGrid.innerHTML = layers.map((layer, index) => {
    const capabilities = [
      layer.tiles ? "XYZ / WMTS" : "",
      layer.items ? "OGC FEATURES" : "",
      layer.archive ? "RANGE ARCHIVE" : "",
    ].filter(Boolean);
    const zooms = layer.zooms ? `Z ${layer.zooms.min}–${layer.zooms.max}` : "";
    const bounds = Array.isArray(layer.bounds)
      ? layer.bounds.map((value) => Number(value).toFixed(3)).join(", ")
      : "未声明";
    return `<article class="source-card catalog-card" data-index="${String(index + 1).padStart(2, "0")}">
      <div class="card-top">
        <span class="source-type">${escapeHTML(layer.format || "features")}</span>
        <span class="health">可访问</span>
      </div>
      <h3>${escapeHTML(layer.title || layer.name)}</h3>
      <p class="description">${escapeHTML(layer.name)} · bounds ${escapeHTML(bounds)}</p>
      <div class="meta">
        ${capabilities.map((item) => `<span class="tag">${item}</span>`).join("")}
        ${zooms ? `<span class="tag">${zooms}</span>` : ""}
      </div>
      <div class="endpoint-list">
        ${displayEndpoint("TileJSON", layer.tilejson, true)}
        ${displayEndpoint("XYZ", layer.tiles)}
        ${displayEndpoint("Features", layer.items, true)}
        ${displayEndpoint("Archive", layer.archive, true)}
      </div>
    </article>`;
  }).join("");
}

async function loadCatalog() {
  const button = $("#refresh-catalog");
  button.disabled = true;
  try {
    const data = await api("/catalog");
    state.catalog = data.layers || [];
    renderCatalog();
    setServerState("ok", "服务连接正常");
    $("#catalog-refresh").textContent = refreshedAt();
  } catch (error) {
    $("#catalog-grid").innerHTML = `<div class="empty">${escapeHTML(error.message)}</div>`;
    setServerState("error", "目录读取失败");
    toast(error.message, true);
  } finally {
    button.disabled = false;
  }
}

function formatNumber(value) {
  return Number(value || 0).toLocaleString("zh-CN");
}

function formatBytes(value) {
  let bytes = Number(value || 0);
  const units = ["B", "KiB", "MiB", "GiB"];
  let unit = 0;
  while (bytes >= 1024 && unit < units.length - 1) {
    bytes /= 1024;
    unit++;
  }
  return `${bytes.toFixed(unit ? 1 : 0)} ${units[unit]}`;
}

function formatUptime(seconds) {
  let remaining = Math.max(0, Number(seconds || 0));
  const days = Math.floor(remaining / 86400);
  remaining %= 86400;
  const hours = Math.floor(remaining / 3600);
  remaining %= 3600;
  const minutes = Math.floor(remaining / 60);
  if (days) return `${days}天 ${hours}时`;
  if (hours) return `${hours}时 ${minutes}分`;
  return `${minutes}分 ${Math.floor(remaining % 60)}秒`;
}

function metricRows(rows) {
  return rows.map(([label, value]) =>
    `<div class="metric-row"><span>${escapeHTML(label)}</span><strong>${escapeHTML(value)}</strong></div>`
  ).join("");
}

function renderStats() {
  const stats = state.stats || {};
  const requests = stats.requests || {};
  const sources = stats.sources || {};
  const runtime = stats.runtime || {};
  const cache = stats.cache || {};
  const memory = cache.memory || {};
  const disk = cache.disk || {};
  const status = requests.byStatusClass || {};
  const featureFlags = Object.entries(stats.features || {})
    .map(([name, enabled]) => `<span class="tag ${enabled ? "enabled" : ""}">${escapeHTML(name)} ${enabled ? "ON" : "OFF"}</span>`)
    .join("");
  const sourceTypes = Object.entries(sources.byType || {})
    .map(([name, count]) => `${name} ${count}`)
    .join(" · ") || "未上报";

  $("#stats-uptime").textContent = formatUptime(stats.uptimeSeconds);
  $("#stats-requests").textContent = formatNumber(requests.total);
  $("#stats-inflight").textContent = formatNumber(requests.inFlight);
  $("#stats-version").textContent = stats.version || "dev";
  $("#stats-grid").innerHTML = `
    <article class="stats-card">
      <span class="source-type">HTTP</span><h3>请求流量</h3>
      ${metricRows([
        ["平均耗时", `${formatNumber(requests.avgDurationMicro)} μs`],
        ["响应流量", formatBytes(requests.bytesOut)],
        ["2xx / 4xx / 5xx", `${formatNumber(status["2xx"])} / ${formatNumber(status["4xx"])} / ${formatNumber(status["5xx"])}`],
      ])}
    </article>
    <article class="stats-card">
      <span class="source-type">CACHE</span><h3>派生切片缓存</h3>
      ${metricRows([
        ["状态", cache.enabled ? "已启用" : "未启用"],
        ["内存命中率", `${(Number(memory.hitRate || 0) * 100).toFixed(1)}%`],
        ["内存条目 / 容量", `${formatNumber(memory.entries)} / ${formatNumber(memory.maxEntries)}`],
        ["内存 / 磁盘体积", `${formatBytes(memory.bytes)} / ${formatBytes(disk.bytes)}`],
        ["淘汰", formatNumber(memory.evictions)],
      ])}
    </article>
    <article class="stats-card">
      <span class="source-type">SOURCES</span><h3>服务能力</h3>
      ${metricRows([
        ["数据源", formatNumber(sources.total)],
        ["切片 / 要素", `${formatNumber(sources.tile)} / ${formatNumber(sources.feature)}`],
        ["类型", sourceTypes],
        ["网络", (sources.networks || []).join(", ") || "无"],
        ["算法", (stats.algorithms || []).join(", ") || "无"],
      ])}
      <div class="meta">${featureFlags}</div>
    </article>
    <article class="stats-card">
      <span class="source-type">RUNTIME</span><h3>Go 进程</h3>
      ${metricRows([
        ["平台", `${runtime.os || "?"} / ${runtime.arch || "?"}`],
        ["Go", runtime.go || "?"],
        ["Goroutine", formatNumber(runtime.goroutines)],
        ["Heap / Sys", `${formatBytes(runtime.heapBytes)} / ${formatBytes(runtime.sysBytes)}`],
        ["GC", formatNumber(runtime.gcCount)],
      ])}
    </article>`;
}

async function loadStats() {
  const button = $("#refresh-stats");
  button.disabled = true;
  try {
    state.stats = await api("/admin/stats");
    renderStats();
    setServerState("ok", "服务连接正常");
    $("#stats-refresh").textContent = refreshedAt();
  } catch (error) {
    $("#stats-grid").innerHTML = `<div class="empty">${escapeHTML(error.message)}</div>`;
    setServerState("error", "统计读取失败");
    toast(error.message, true);
  } finally {
    button.disabled = false;
  }
}

function renderTypeSwitch() {
  $("#type-switch").innerHTML = TYPES.map((type) =>
    `<button type="button" data-type="${type.code}" class="${state.type === type.code ? "active" : ""}">${type.label}</button>`
  ).join("");
}

function field(name, label, placeholder = "", options = "") {
  return `<label><span>${label}</span><input name="${name}" placeholder="${placeholder}" ${options}></label>`;
}

function renderConnectionFields(source = {}) {
  if (state.type === "postgis" || state.type === "mysql") {
    const mysql = state.type === "mysql";
    const databaseName = mysql ? "MySQL 8" : "PostgreSQL / PostGIS";
    fields.innerHTML = `
      <div class="form-grid two">
        ${field("db_host", "主机 *", "127.0.0.1", "required")}
        ${field("db_port", "端口 *", mysql ? "3306" : "5432", 'required inputmode="numeric"')}
      </div>
      ${field("db_database", "数据库 *", "geoverse", "required")}
      <div class="form-grid two">
        ${field("db_user", "用户名 *", mysql ? "geoverse_reader" : "postgres", 'required autocomplete="username"')}
        ${field("db_password", state.editing ? "密码（留空保留原连接）" : "密码", "", 'type="password" autocomplete="new-password"')}
      </div>
      <div class="form-grid two">
        ${field("table", "表 / 视图 *", mysql ? "geoverse.features" : "public.roads", "required")}
        ${field("geometry_column", "几何列", "geom")}
      </div>
      <div class="form-grid two">
        ${field("id_column", "主键列", "自动探测")}
        ${field("srid", "SRID", "自动探测", 'type="number" min="0"')}
      </div>
      ${field("fields", "属性列", "name, class（留空自动发现）")}
      <p class="field-note">${databaseName} 连接只用于只读探测与查询。${mysql
        ? "MySQL 会把 bbox 过滤下推到空间索引，单瓦片最多编码 50,000 个候选要素。"
        : "PostGIS 会在数据库内直接生成 MVT。"}生产环境建议使用最小权限账号，并在启用 API Key 后开放管理入口。</p>
    `;
    hydrateDatabase(source);
    updateLocalExampleButton();
    return;
  }
  if (state.type === "geopackage") {
    fields.innerHTML = `
      ${field("path", "GeoPackage 文件路径 *", "./data/parcels.gpkg", "required")}
      ${field("layer", "要素表", "仅一个要素表时可留空")}
      <p class="field-note">文件路径由 Serve 进程读取；容器部署时请填写容器内只读挂载路径。</p>
    `;
  } else {
    const labels = {
      geojson: ["GeoJSON 文件路径 *", "./data/places.geojson"],
      mbtiles: ["MBTiles 文件路径 *", "./data/basemap.mbtiles"],
      pmtiles: ["PMTiles v3 文件路径 *", "./data/basemap.pmtiles"],
    };
    const [label, placeholder] = labels[state.type];
    fields.innerHTML = `${field("path", label, placeholder, "required")}
      <p class="field-note">${state.type === "pmtiles"
        ? "归档会同时提供 XYZ 解包切片与 /archives/{name}.pmtiles Range/206 原始分发。"
        : "文件路径由 Serve 进程读取；保存前会实际打开并探测内容。"}</p>`;
  }
  hydrateSimple(source);
}

function hydrateSimple(source) {
  for (const name of ["path", "layer"]) {
    const input = form.elements[name];
    if (input) input.value = source[name] || "";
  }
}

function hydrateDatabase(source) {
  let parsed = null;
  try { parsed = source.dsn_hint ? new URL(source.dsn_hint) : null; } catch {}
  const values = {
    db_host: parsed?.hostname || "",
    db_port: parsed?.port || (state.type === "mysql" ? "3306" : "5432"),
    db_database: parsed?.pathname?.replace(/^\//, "") || "",
    db_user: parsed ? decodeURIComponent(parsed.username) : "",
    table: source.table || "",
    geometry_column: source.geometry_column || "",
    id_column: source.id_column || "",
    srid: source.srid || "",
    fields: Array.isArray(source.fields) ? source.fields.join(", ") : "",
  };
  Object.entries(values).forEach(([name, value]) => {
    if (form.elements[name]) form.elements[name].value = value;
  });
}

function openDrawer(source = null) {
  state.editing = source?.name || null;
  state.type = source?.type || "postgis";
  form.reset();
  $("#drawer-title").textContent = source ? `配置 ${source.name}` : "添加数据源";
  form.elements.name.readOnly = Boolean(source);
  if (source) {
    for (const name of ["name", "title", "description", "min_zoom", "max_zoom"]) {
      if (form.elements[name]) form.elements[name].value = source[name] ?? "";
    }
  }
  renderTypeSwitch();
  renderConnectionFields(source || {});
  setProbe(null);
  updateLocalExampleButton();
  backdrop.hidden = false;
  document.body.style.overflow = "hidden";
  setTimeout(() => form.elements.name.focus(), 30);
}

function closeDrawer() {
  backdrop.hidden = true;
  document.body.style.overflow = "";
}

function nullableNumber(name) {
  const raw = form.elements[name]?.value;
  return raw === "" || raw == null ? undefined : Number(raw);
}

function buildSource() {
  const data = new FormData(form);
  const source = {
    name: String(data.get("name") || "").trim(),
    type: state.type,
    title: String(data.get("title") || "").trim(),
    description: String(data.get("description") || "").trim(),
    min_zoom: nullableNumber("min_zoom"),
    max_zoom: nullableNumber("max_zoom"),
  };
  if (state.type === "postgis" || state.type === "mysql") {
    const mysql = state.type === "mysql";
    const password = String(data.get("db_password") || "");
    if (!state.editing || password) {
      const user = encodeURIComponent(String(data.get("db_user") || "").trim());
      const pass = password ? `:${encodeURIComponent(password)}` : "";
      const host = String(data.get("db_host") || "").trim();
      const port = String(data.get("db_port") || (mysql ? "3306" : "5432")).trim();
      const database = encodeURIComponent(String(data.get("db_database") || "").trim());
      source.dsn = `${mysql ? "mysql" : "postgres"}://${user}${pass}@${host}:${port}/${database}`;
    }
    source.table = String(data.get("table") || "").trim();
    source.geometry_column = String(data.get("geometry_column") || "").trim();
    source.id_column = String(data.get("id_column") || "").trim();
    source.srid = nullableNumber("srid");
    const selectedFields = String(data.get("fields") || "")
      .split(",")
      .map((value) => value.trim())
      .filter(Boolean);
    if (selectedFields.length) source.fields = selectedFields;
  } else {
    source.path = String(data.get("path") || "").trim();
    if (state.type === "geopackage") source.layer = String(data.get("layer") || "").trim();
  }
  Object.keys(source).forEach((key) => source[key] === undefined && delete source[key]);
  return source;
}

function sourcePayload(source) {
  const keys = [
    "name", "type", "title", "description", "path", "layer", "dsn", "table",
    "geometry_column", "id_column", "srid", "fields", "min_zoom", "max_zoom",
    "buffer", "extent", "simplify", "cache",
  ];
  return Object.fromEntries(keys.filter((key) => source[key] !== undefined).map((key) => [key, source[key]]));
}

function setProbe(result) {
  if (!result) {
    probeResult.hidden = true;
    probeResult.textContent = "";
    probeResult.className = "probe-result";
    return;
  }
  probeResult.hidden = false;
  probeResult.className = `probe-result${result.ok ? "" : " error"}`;
  probeResult.textContent = result.detail;
}

async function probe(source = buildSource()) {
  setProbe({ ok: true, detail: "正在连接并读取元数据…" });
  try {
    const result = await api("/admin/sources/probe", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(source),
    });
    setProbe(result);
    return result.ok;
  } catch (error) {
    setProbe({ ok: false, detail: error.message });
    return false;
  }
}

async function saveSource(event) {
  event.preventDefault();
  if (!form.reportValidity()) return;
  const source = buildSource();
  const submit = form.querySelector('[type="submit"]');
  submit.disabled = true;
  submit.textContent = "保存中…";
  try {
    await api("/admin/sources", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(source),
    });
    toast(`数据源 ${source.name} 已保存并热加载。`);
    closeDrawer();
    await loadSources();
  } catch (error) {
    setProbe({ ok: false, detail: error.message });
  } finally {
    submit.disabled = false;
    submit.textContent = "测试并保存";
  }
}

async function sourceAction(event) {
  const button = event.target.closest("button[data-action]");
  if (!button) return;
  const source = state.sources.find((item) => item.name === button.dataset.name);
  if (!source) return;
  if (button.dataset.action === "edit") return openDrawer(source);
  if (button.dataset.action === "probe") {
    button.disabled = true;
    try {
      const result = await api("/admin/sources/probe", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ...sourcePayload(source), dsn: "" }),
      });
      toast(result.ok ? `${source.name}：${result.detail}` : `${source.name}：${result.detail}`, !result.ok);
      await loadSources();
    } catch (error) { toast(error.message, true); }
    finally { button.disabled = false; }
    return;
  }
  if (button.dataset.action === "delete") {
    if (!confirm(`确认移除数据源 “${source.name}”？\n配置会写回 YAML，当前请求不受影响。`)) return;
    try {
      await api(`/admin/sources/${encodeURIComponent(source.name)}`, { method: "DELETE" });
      toast(`已移除 ${source.name}`);
      await loadSources();
    } catch (error) { toast(error.message, true); }
  }
}

$("#add-source").addEventListener("click", () => openDrawer());
$("#close-drawer").addEventListener("click", closeDrawer);
backdrop.addEventListener("click", (event) => { if (event.target === backdrop) closeDrawer(); });
$("#refresh").addEventListener("click", loadSources);
$("#refresh-catalog").addEventListener("click", loadCatalog);
$("#refresh-stats").addEventListener("click", loadStats);
grid.addEventListener("click", sourceAction);
form.addEventListener("submit", saveSource);
document.querySelector("nav").addEventListener("click", (event) => {
  const button = event.target.closest("[data-view]");
  if (button) setView(button.dataset.view);
});
$("#catalog-grid").addEventListener("click", async (event) => {
  const button = event.target.closest("button[data-copy]");
  if (!button) return;
  try {
    await navigator.clipboard.writeText(button.dataset.copy);
    toast("服务地址已复制。");
  } catch {
    const area = document.createElement("textarea");
    area.value = button.dataset.copy;
    document.body.appendChild(area);
    area.select();
    document.execCommand("copy");
    area.remove();
    toast("服务地址已复制。");
  }
});
$("#probe-source").addEventListener("click", () => { if (form.reportValidity()) probe(); });
$("#type-switch").addEventListener("click", (event) => {
  const button = event.target.closest("button[data-type]");
  if (!button || state.editing) return;
  state.type = button.dataset.type;
  renderTypeSwitch();
  renderConnectionFields();
  updateLocalExampleButton();
  setProbe(null);
});
function updateLocalExampleButton() {
  const button = $("#local-db-example");
  const database = state.type === "postgis" || state.type === "mysql";
  button.hidden = !database;
  button.textContent = state.type === "mysql" ? "填入本地 MySQL / MariaDB 示例" : "填入本地 PostgreSQL 示例";
}
$("#local-db-example").addEventListener("click", () => {
  if (state.type !== "postgis" && state.type !== "mysql") return;
  const mysql = state.type === "mysql";
  const example = {
    name: mysql ? "local-mysql" : "live-features",
    title: mysql ? "本地 MySQL 8 空间表示例" : "GeoVerse Live 要素",
    db_host: "127.0.0.1",
    db_port: mysql ? "3308" : "5432",
    db_database: mysql ? "geoverse_demo" : "geoverse",
    db_user: mysql ? "geoverse_reader" : "geoverse",
    db_password: mysql ? "geoverse_demo" : "geoverse",
    table: mysql ? "geoverse_demo.warehouse" : "geo.feature",
    geometry_column: mysql ? "location" : "geom",
    id_column: mysql ? "id" : "project_id",
    srid: "4326",
    fields: mysql ? "name, address, capacity" : "project_id, layer_id, feature_type",
  };
  Object.entries(example).forEach(([name, value]) => {
    if (form.elements[name]) form.elements[name].value = value;
  });
  setProbe(null);
});
$("#api-key").value = sessionStorage.getItem("geoverse-api-key") || "";
$("#api-key-form").addEventListener("submit", (event) => event.preventDefault());
$("#api-key").addEventListener("change", (event) => {
  const value = event.target.value.trim();
  if (value) sessionStorage.setItem("geoverse-api-key", value);
  else sessionStorage.removeItem("geoverse-api-key");
  setView(state.view, false);
});
document.addEventListener("keydown", (event) => {
  if (event.key === "Escape" && !backdrop.hidden) closeDrawer();
});

setView(location.hash.replace(/^#/, ""), false);
