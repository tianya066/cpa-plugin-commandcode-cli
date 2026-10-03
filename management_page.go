package plugin

// The panel page: a single self-contained HTML document served from
// /v0/resource/plugins/commandcode/index.html. It renders the JSON from the
// plugin's own /status route, so no build step or external asset is involved.
//
// Authentication: the document itself is served unauthenticated, while
// /v0/management/commandcode/status requires the management key. The page
// therefore keeps its own key entry (the same approach the clinepass plugin page
// takes) and additionally tries the places the CPA panel may have left a key.

import (
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const managementPageHTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>CommandCode 渠道状态</title>
<style>
:root{color-scheme:light dark}
*{box-sizing:border-box}
body{margin:0;padding:24px;font:14px/1.55 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,"Helvetica Neue",Arial,"PingFang SC","Microsoft YaHei",sans-serif;background:#f6f7f9;color:#1c1f23}
@media (prefers-color-scheme:dark){body{background:#16181d;color:#e6e8eb}.card{background:#1e2126!important;border-color:#2c3037!important}.muted{color:#8b939e!important}.bar{background:#2c3037!important}input{border-color:#3a3f47!important}}
.wrap{max-width:960px;margin:0 auto}
h1{font-size:20px;margin:0 0 4px}
.sub{color:#6b7280;margin:0 0 20px;font-size:13px}
.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(280px,1fr));gap:14px}
.card{background:#fff;border:1px solid #e5e7eb;border-radius:10px;padding:16px}
.card h2{font-size:13px;margin:0 0 12px;text-transform:uppercase;letter-spacing:.04em;color:#6b7280;font-weight:600}
.kv{display:flex;justify-content:space-between;gap:12px;padding:5px 0;border-bottom:1px dashed rgba(128,128,128,.18)}
.kv:last-child{border-bottom:0}
.k{color:#6b7280}
.v{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;text-align:right;word-break:break-all}
.pill{display:inline-block;padding:1px 8px;border-radius:999px;font-size:12px;background:#e8f0fe;color:#1a56db}
.pill.warn{background:#fef3c7;color:#92400e}
.pill.err{background:#fee2e2;color:#991b1b}
.bar{height:8px;background:#e5e7eb;border-radius:999px;overflow:hidden;margin:6px 0 2px}
.bar>i{display:block;height:100%;background:linear-gradient(90deg,#3b82f6,#60a5fa)}
table{width:100%;border-collapse:collapse;font-size:13px}
th,td{text-align:left;padding:6px 8px;border-bottom:1px solid rgba(128,128,128,.16)}
th{color:#6b7280;font-weight:600}
code{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
.muted{color:#6b7280}
.err{color:#991b1b}
#err{display:none;margin:0 0 16px;padding:10px 12px;border-radius:8px;background:#fee2e2;color:#991b1b}
.keyrow{display:flex;gap:8px;flex-wrap:wrap;align-items:center}
.keyrow input{flex:1 1 320px;min-width:220px;padding:7px 10px;border:1px solid #d1d5db;border-radius:7px;background:transparent;color:inherit;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
.keyrow button{padding:7px 14px;border-radius:7px;cursor:pointer}
#save{border:0;background:#2563eb;color:#fff}
#forget{border:1px solid #d1d5db;background:transparent;color:inherit}
</style>
</head>
<body>
<div class="wrap">
  <h1>CommandCode 渠道状态</h1>
  <p class="sub">插件 <code id="ver">…</code> · 数据由插件自身接口提供 · <span id="at" class="muted"></span></p>
  <div id="err"></div>

  <div class="card" id="keybar" style="margin-bottom:14px">
    <h2>管理密钥</h2>
    <div class="keyrow">
      <input id="token" type="password" placeholder="粘贴 CPA 管理密钥（面板登录用的那串）">
      <button id="save">保存</button>
      <button id="forget">清除</button>
    </div>
    <div class="muted" id="keystate" style="font-size:12px;margin-top:8px">尚未保存密钥</div>
  </div>

  <div class="grid">
    <div class="card">
      <h2>传输与端点</h2>
      <div id="transport"></div>
    </div>
    <div class="card">
      <h2>账号</h2>
      <div id="account"></div>
    </div>
    <div class="card">
      <h2>套餐额度</h2>
      <div id="quota"></div>
    </div>
    <div class="card">
      <h2>密钥池</h2>
      <div id="keys"></div>
    </div>
  </div>

  <div class="card" style="margin-top:14px">
    <h2>模型映射</h2>
    <div id="models"></div>
  </div>
</div>
<script>
(function () {
  var BASE = '/v0/management/commandcode/status';
  // Storage key this page owns. The CPA panel keeps its own key out of reach
  // (its zustand blob only carries apiBase/rememberPassword unless the user ticks
  // "remember password"), so this page offers a one-time paste box — the same
  // approach the clinepass plugin page uses.
  var TOKEN_KEY = 'commandcode-management-key';
  var OBFUSCATION_PREFIX = 'enc::v1::';
  var OBFUSCATION_MATERIAL = 'cli-proxy-api-webui::secure-storage';

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"]/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c];
    });
  }
  function kv(k, v, cls) {
    return '<div class="kv"><span class="k">' + esc(k) + '</span><span class="v ' + (cls || '') + '">' + esc(v) + '</span></div>';
  }
  function pill(text, kind) { return '<span class="pill ' + (kind || '') + '">' + esc(text) + '</span>'; }
  function pct(used, cap) {
    if (!cap || cap <= 0) return null;
    return Math.max(0, Math.min(100, (used / cap) * 100));
  }
  function resetText(ms) {
    if (!ms) return '';
    var d = new Date(ms);
    if (isNaN(d.getTime())) return '';
    var mins = Math.round((d.getTime() - Date.now()) / 60000);
    if (mins <= 0) return '已重置';
    if (mins < 60) return mins + ' 分钟后重置';
    return Math.round(mins / 60) + ' 小时后重置';
  }

  // The panel obfuscates its stored values with a repeating XOR key derived from
  // "cli-proxy-api-webui::secure-storage|<host>|<userAgent>", base64-encoded and
  // prefixed with "enc::v1::". Same origin, so a persisted panel key is readable.
  function deobfuscate(stored) {
    if (!stored) return '';
    if (stored.indexOf(OBFUSCATION_PREFIX) !== 0) return stored;
    try {
      var material = new TextEncoder().encode(
        OBFUSCATION_MATERIAL + '|' + window.location.host + '|' + navigator.userAgent);
      var bin = atob(stored.slice(OBFUSCATION_PREFIX.length));
      var bytes = new Uint8Array(bin.length);
      for (var i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
      var out = new Uint8Array(bytes.length);
      for (var j = 0; j < bytes.length; j++) out[j] = bytes[j] ^ material[j % material.length];
      var text = new TextDecoder().decode(out);
      if (text.charAt(0) === '"' || text.charAt(0) === '{') {
        try {
          var parsed = JSON.parse(text);
          if (typeof parsed === 'string') return parsed;
          if (parsed && typeof parsed.value === 'string') return parsed.value;
        } catch (e) { /* keep the decoded text */ }
      }
      return text;
    } catch (e) { return ''; }
  }

  function readManagementKey() {
    try {
      var own = window.localStorage.getItem(TOKEN_KEY);
      if (own) return own;
    } catch (e) { /* ignore */ }
    var stores = [window.localStorage, window.sessionStorage];
    for (var s = 0; s < stores.length; s++) {
      try {
        var dec = deobfuscate(stores[s].getItem('managementKey') || '');
        if (dec) return dec;
      } catch (e) { /* ignore */ }
    }
    try {
      var blob = deobfuscate(window.localStorage.getItem('cli-proxy-auth') || '');
      if (blob) {
        var state = (JSON.parse(blob) || {}).state || {};
        if (typeof state.managementKey === 'string' && state.managementKey) return state.managementKey;
      }
    } catch (e) { /* ignore */ }
    try {
      var params = new URLSearchParams(window.location.search);
      var q = params.get('key') || params.get('managementKey');
      if (q) return q;
    } catch (e) { /* ignore */ }
    return '';
  }

  var mgmtKey = readManagementKey();
  var input = document.getElementById('token');
  var stateEl = document.getElementById('keystate');
  var errEl = document.getElementById('err');

  function setKeyState(text, isError) {
    if (!stateEl) return;
    stateEl.textContent = text;
    stateEl.className = isError ? 'err' : 'muted';
  }
  function describe(key) {
    return key ? ('已保存密钥（' + key.slice(0, 4) + '…' + key.slice(-4) + '）') : '尚未保存密钥';
  }
  function showError(message) {
    if (!errEl) return;
    errEl.style.display = message ? 'block' : 'none';
    errEl.textContent = message || '';
  }

  function load() {
    var headers = { 'Accept': 'application/json' };
    if (mgmtKey) headers['Authorization'] = 'Bearer ' + mgmtKey;
    fetch(BASE, { headers: headers, cache: 'no-store' })
      .then(function (r) {
        if (r.status === 401 || r.status === 403) {
          throw new Error('HTTP ' + r.status + ' —— 管理密钥无效或未保存，请在上方粘贴 CPA 管理密钥后点“保存”。');
        }
        if (!r.ok) throw new Error('HTTP ' + r.status);
        return r.json();
      })
      .then(function (d) {
        showError('');
        document.getElementById('ver').textContent = 'v' + (d.version || '?');
        document.getElementById('at').textContent = d.generated_at ? ('采集于 ' + d.generated_at) : '';

        var mode = d.transport || 'provider';
        var t = [];
        t.push(kv('传输方式', mode));
        t.push(kv('Provider 端点', d.base_url));
        if (mode !== 'provider') {
          t.push(kv('CLI 端点', d.cli_base));
          t.push(kv('CLI 版本头', d.cli_version));
        }
        document.getElementById('transport').innerHTML = t.join('');

        var a = d.account || {};
        var acc = [];
        var who = a.whoami && a.whoami.user;
        if (who) {
          acc.push(kv('账号', who.email || who.userName || who.name || ''));
          if (who.name && who.name !== who.email) acc.push(kv('用户名', who.name));
        }
        var sub = a.subscription && a.subscription.data;
        if (sub) {
          acc.push(kv('订阅状态', sub.status || ''));
          if (sub.createdAt) acc.push(kv('开通时间', String(sub.createdAt).slice(0, 10)));
        }
        if (!acc.length) {
          acc.push('<div class="muted">未取到账号信息' + (a.whoami_error ? '：' + esc(a.whoami_error) : '') + '</div>');
        }
        document.getElementById('account').innerHTML = acc.join('');

        var q = [];
        var credits = a.credits || {};
        var wl = credits.windowLimits || {};
        if (credits.credits) {
          var cr = credits.credits;
          q.push(kv('本月已用额度', cr.monthlyCredits != null ? cr.monthlyCredits.toFixed(4) : '—'));
          q.push(kv('购买额度', cr.purchasedCredits != null ? cr.purchasedCredits : '—'));
          q.push(kv('免费额度', cr.freeCredits != null ? cr.freeCredits : '—'));
        }
        ['fiveHour', 'weekly'].forEach(function (k) {
          var w = wl[k];
          if (!w) return;
          var label = k === 'fiveHour' ? '5 小时窗口' : '本周窗口';
          var p = pct(w.used, w.cap);
          q.push('<div class="kv"><span class="k">' + label + '</span><span class="v">' +
            esc(w.used != null ? w.used.toFixed(3) : '?') + ' / ' + esc(w.cap) +
            (w.exceeded ? ' ' + pill('已超限', 'err') : '') + '</span></div>');
          if (p !== null) q.push('<div class="bar"><i style="width:' + p.toFixed(1) + '%"></i></div>');
          var rt = resetText(w.resetAt);
          if (rt) q.push('<div class="muted" style="font-size:12px;margin-bottom:6px">' + esc(rt) + '</div>');
        });
        if (!q.length) {
          q.push('<div class="muted">未取到额度' + (credits.credits_error ? '：' + esc(credits.credits_error) : '') + '</div>');
        }
        document.getElementById('quota').innerHTML = q.join('');

        var ks = d.keys || [];
        var kk = ['<table><tr><th>密钥</th><th>权重</th><th>代理</th><th>状态</th></tr>'];
        ks.forEach(function (row) {
          kk.push('<tr><td><code>' + esc(row.label) + '</code></td><td>' + esc(row.weight) + '</td><td>' +
            (row.proxy ? '有' : '<span class="muted">无</span>') + '</td><td>' +
            (row.disabled ? pill('已禁用', 'warn') : pill('启用')) + '</td></tr>');
        });
        kk.push('</table>');
        document.getElementById('keys').innerHTML = ks.length ? kk.join('') : '<div class="muted">未配置密钥</div>';

        var ms = d.models || [];
        var mm = ['<table><tr><th>客户端别名</th><th>上游模型</th><th>命名空间 ID</th></tr>'];
        ms.forEach(function (row) {
          mm.push('<tr><td><code>' + esc(row.alias) + '</code></td><td><code>' + esc(row.upstream) +
            '</code></td><td><code>' + esc(row.namespace) + '</code></td></tr>');
        });
        mm.push('</table>');
        document.getElementById('models').innerHTML = ms.length ? mm.join('') : '<div class="muted">未配置模型</div>';
      })
      .catch(function (e) { showError('读取插件状态失败：' + e.message); });
  }

  var saveBtn = document.getElementById('save');
  if (saveBtn) saveBtn.addEventListener('click', function () {
    var value = (input && input.value || '').trim();
    if (!value) { setKeyState('请先粘贴管理密钥', true); return; }
    try {
      window.localStorage.setItem(TOKEN_KEY, value);
    } catch (e) { setKeyState('无法写入 localStorage：' + e.message, true); return; }
    mgmtKey = value;
    if (input) input.value = '';
    setKeyState(describe(mgmtKey), false);
    load();
  });

  var forgetBtn = document.getElementById('forget');
  if (forgetBtn) forgetBtn.addEventListener('click', function () {
    try { window.localStorage.removeItem(TOKEN_KEY); } catch (e) { /* ignore */ }
    mgmtKey = '';
    setKeyState(describe(''), false);
    showError('');
  });

  setKeyState(describe(mgmtKey), false);
  load();
})();
</script>
</body>
</html>
`

// managementPage serves the panel document.
func managementPage() pluginapi.ManagementResponse {
	h := http.Header{}
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	return pluginapi.ManagementResponse{StatusCode: http.StatusOK, Headers: h, Body: []byte(strings.TrimSpace(managementPageHTML))}
}
