package plugin

import (
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Provider credentials are write-only. Only the management credential may be
// remembered in this browser. This document has no external asset dependencies.
const managementPageHTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="referrer" content="no-referrer">
<title>CommandCode 管理</title>
<style>
:root{color-scheme:light;--bg:#f4f6fb;--panel:#fff;--text:#18243c;--muted:#65728a;--line:#dce3ef;--blue:#315ce5;--soft:#edf2ff;--green:#187450;--red:#b73340;--amber:#8b6310}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font:14px/1.6 system-ui,-apple-system,"Segoe UI","Microsoft YaHei",sans-serif}button,input,select{font:inherit;max-width:100%}button{cursor:pointer;min-height:40px;border:1px solid var(--line);border-radius:9px;background:var(--panel);color:var(--text);padding:8px 14px;font-weight:600}button:hover:not(:disabled){border-color:var(--blue);color:var(--blue)}button.primary{background:var(--blue);border-color:var(--blue);color:white}button.danger{color:var(--red)}button:disabled{opacity:.5;cursor:not-allowed}input,select{width:100%;min-width:0;border:1px solid var(--line);border-radius:8px;padding:9px 11px;background:var(--panel);color:var(--text);min-height:42px}input[type=checkbox],input[type=radio]{width:17px;min-height:17px;flex:0 0 17px;accent-color:var(--blue)}:focus-visible{outline:3px solid #96b0ff;outline-offset:2px}label{display:block;font-size:13px;font-weight:600;margin-bottom:5px}h1,h2,h3,p{margin:0}h1{font-size:27px;letter-spacing:-.7px}h2{font-size:18px}h3{font-size:15px}fieldset{border:0;padding:0;margin:0;min-width:0}.wrap{max-width:1152px;padding:30px 24px 124px;margin:auto}.top,.section-head{display:flex;align-items:center;justify-content:space-between;gap:12px;flex-wrap:wrap}.top{margin-bottom:24px}.eyebrow{font-size:11px;font-weight:800;letter-spacing:2px;color:var(--blue);margin-bottom:4px}.sub,.hint,.muted{color:var(--muted)}.sub{margin-top:5px}.hint{font-size:12px;margin-top:5px;overflow-wrap:anywhere}.panel{background:var(--panel);border:1px solid var(--line);border-radius:15px;padding:22px;margin:16px 0}.section-head{margin-bottom:18px}.section-title{display:flex;align-items:center;gap:10px;flex-wrap:wrap}.step{display:inline-grid;place-items:center;width:26px;height:26px;background:var(--soft);color:var(--blue);border-radius:7px;font-size:12px;font-weight:800}.badge{display:inline-flex;align-items:center;border-radius:99px;background:var(--soft);color:var(--blue);padding:3px 10px;font-size:12px;font-weight:600;max-width:100%;overflow-wrap:anywhere}.badge.ok{color:var(--green);background:#e5f5ed}.badge.warn{color:var(--amber);background:#fff3d3}.badge.error{color:var(--red);background:#ffebec}.connection{display:grid;grid-template-columns:minmax(0,1fr) auto auto;gap:9px;align-items:end}.auth-label{grid-column:1/-1;margin-bottom:-4px}.notice{border:1px solid #f1bdc1;color:var(--red);background:#fff0f1;border-radius:10px;padding:12px 15px;margin:14px 0;overflow-wrap:anywhere;white-space:pre-wrap}.notice.info{background:var(--soft);color:var(--blue);border-color:#ccd7ff}[hidden]{display:none!important}.toolbar{display:flex;gap:8px;align-items:center;flex-wrap:wrap}.key-list{display:grid;gap:14px}.key-card{border:1px solid var(--line);border-radius:12px;padding:18px;min-width:0}.key-card.removed{border-style:dashed;background:var(--bg)}.key-header{display:flex;justify-content:space-between;gap:12px;margin-bottom:15px;align-items:flex-start}.key-title{min-width:0;overflow-wrap:anywhere}.key-title .badge{margin:6px 5px 0 0}.fields{display:grid;grid-template-columns:minmax(0,1.3fr) minmax(0,1.7fr) minmax(85px,.5fr);gap:12px}.fields>*{min-width:0}.check{display:flex;align-items:center;gap:8px;margin:8px 0 0;font-weight:400}.proxy{margin-top:13px}.proxy-grid{display:grid;grid-template-columns:minmax(0,1fr) auto;gap:12px;align-items:center}.account{margin-top:17px;padding-top:15px;border-top:1px solid var(--line)}.account-grid{display:grid;grid-template-columns:minmax(0,1fr) minmax(0,1fr);gap:20px}.kv{display:flex;justify-content:space-between;gap:12px;padding:3px 0;min-width:0}.kv span:first-child{color:var(--muted)}.kv span:last-child{text-align:right;overflow-wrap:anywhere;min-width:0}.quota-line{margin-bottom:10px}.meter{height:6px;background:var(--line);border-radius:6px;overflow:hidden;margin:5px 0}.meter i{height:100%;display:block;background:var(--blue)}.errors{margin-top:8px;color:var(--red);font-size:12px;white-space:pre-wrap;overflow-wrap:anywhere}.test{display:grid;grid-template-columns:minmax(0,1fr) auto;gap:9px;margin-top:14px;align-items:end}.test-result{margin-top:8px;padding:10px 12px;background:var(--bg);border-radius:8px;white-space:pre-wrap;overflow-wrap:anywhere;font-size:12px}.model-row{display:grid;grid-template-columns:minmax(0,1fr) minmax(0,1.3fr) minmax(0,1fr) auto;gap:10px;padding:12px 0;border-bottom:1px solid var(--line);align-items:end}.model-row:last-child{border-bottom:0}.model-row>*{min-width:0}.choices{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:12px}.choice{padding:15px;border:1px solid var(--line);border-radius:10px;font-weight:400;margin:0}.choice:has(input:checked){background:var(--soft);border-color:var(--blue)}.choice-title{display:flex;gap:8px;align-items:center;font-weight:700}.choice .hint{display:block;margin-top:8px}.advanced{margin-top:18px;border-top:1px solid var(--line);padding-top:15px}summary{cursor:pointer;font-weight:600;padding:5px 0}.advanced-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:14px;margin-top:15px}.advanced-grid>*{min-width:0}.empty{border:1px dashed var(--line);background:var(--bg);padding:25px;text-align:center;border-radius:10px;color:var(--muted)}.footer{position:fixed;left:0;right:0;bottom:0;z-index:10;background:var(--panel);border-top:1px solid var(--line);box-shadow:0 -5px 25px #1b29420a}.footer-inner{max-width:1152px;margin:auto;padding:14px 24px;display:flex;justify-content:space-between;align-items:center;gap:14px}.footer-note{font-size:12px;color:var(--muted);margin-top:2px}.footer-state{min-width:0;overflow-wrap:anywhere}.footer .primary{min-width:130px}.small{font-size:12px}.intro{margin-bottom:15px}.loading{color:var(--muted);font-size:13px}.count{font-size:12px;color:var(--muted);font-weight:400}
@media(max-width:760px){.wrap{padding:22px 16px 130px}.panel{padding:17px}.fields{grid-template-columns:minmax(0,1fr) minmax(0,1fr)}.fields .key-secret{grid-column:1/-1;grid-row:2}.account-grid{grid-template-columns:minmax(0,1fr);gap:12px}.choices{grid-template-columns:minmax(0,1fr)}.model-row{grid-template-columns:minmax(0,1fr) minmax(0,1fr)}.model-row .model-display{grid-column:1}.advanced-grid{grid-template-columns:minmax(0,1fr)}.footer-inner{padding:12px 16px}.key-card{padding:14px}.connection{grid-template-columns:minmax(0,1fr) auto}.auth-label{grid-column:1/-1}.connection #token{grid-column:1/-1}}
@media(max-width:400px){.wrap{padding:18px 12px 160px}h1{font-size:24px}.panel{padding:14px}.key-header{flex-wrap:wrap}.model-row{grid-template-columns:minmax(0,1fr)}.model-row .model-display{grid-column:auto}.model-row button{justify-self:start}.proxy-grid{grid-template-columns:minmax(0,1fr)}.test{grid-template-columns:minmax(0,1fr)}.footer-inner{padding:10px 12px;gap:10px}.footer .primary{min-width:100px;padding:8px 10px}.toolbar{width:100%}.toolbar button{flex:1}.kv{gap:8px}.footer-actions{max-width:112px}.footer-actions button{width:100%}}
@media(prefers-color-scheme:dark){:root{color-scheme:dark;--bg:#111827;--panel:#1b2536;--text:#e3eaf7;--muted:#a1aec3;--line:#354258;--blue:#8daaff;--soft:#26395c;--green:#8bd7b2;--red:#ffabb0;--amber:#eac986}button.primary{background:#486edc;border-color:#486edc;color:#fff}.badge.ok{background:#1d4539}.badge.warn{background:#4c3d25}.badge.error,.notice{background:#482932;border-color:#76505b}.notice.info{background:var(--soft);border-color:var(--line)}}
</style>
</head>
<body>
<main class="wrap">
<header class="top"><div><div class="eyebrow">COMMANDCODE · 管理中心</div><h1>账号与模型</h1><p class="sub">在这里统一管理上游密钥、额度和模型；CPA 负责客户端鉴权与转发。</p></div><span id="connection-state" class="badge">未连接</span></header>
<section class="panel" aria-labelledby="auth-title"><div class="section-head"><h2 id="auth-title">连接管理接口</h2><span class="small muted" id="version"></span></div><div class="connection"><label class="auth-label" for="token">CPA 管理密钥</label><input id="token" type="password" autocomplete="off" placeholder="输入面板登录时使用的管理密钥"><button id="connect" class="primary">连接并读取</button><button id="forget">清除凭据</button></div><label class="check"><input id="remember" type="checkbox">在此浏览器记住管理密钥</label><p class="hint" id="key-state">管理密钥仅用于请求鉴权，不会放入网址。CommandCode 密钥不会存入浏览器存储。</p></section>
<div id="error" class="notice" role="alert" hidden></div><div id="message" class="notice info" role="status" hidden></div>
<div id="initial" class="empty">连接后即可读取配置、查看每个账号的额度并管理模型映射。</div>
<form id="editor" hidden><fieldset id="editable">
<section class="panel" aria-labelledby="keys-title"><div class="section-head"><div class="section-title"><span class="step">01</span><h2 id="keys-title">账号密钥 <span class="count" id="key-count"></span></h2></div><div class="toolbar"><button type="button" id="refresh-status">刷新账号与额度</button><button type="button" id="add-key">＋ 新增密钥</button></div></div><p class="hint intro">每个密钥独立设置权重、启停和代理。权重越高，接收请求的比例越大；账号信息与额度按已生效配置查询。</p><div class="key-list" id="keys"></div><p class="hint" id="status-time"></p></section>
<section class="panel" aria-labelledby="models-title"><div class="section-head"><div class="section-title"><span class="step">02</span><h2 id="models-title">模型映射</h2></div><button type="button" id="add-model">＋ 添加模型</button></div><p class="hint intro">客户端使用“调用名称”，插件将其转换为 CommandCode 的“上游模型”。修改后保存即可应用。</p><div id="models"></div></section>
<section class="panel" aria-labelledby="route-title"><div class="section-head"><div class="section-title"><span class="step">03</span><h2 id="route-title">调用通道</h2></div></div><div class="choices"><label class="choice"><span class="choice-title"><input type="radio" name="transport" value="cli">CLI 通道</span><span class="hint">使用 CommandCode CLI 接口，可用于 Go / GOAT 套餐。</span></label><label class="choice"><span class="choice-title"><input type="radio" name="transport" value="provider">Provider 通道</span><span class="hint">使用标准 Provider 接口，需要包含 API 访问权限的套餐。</span></label><label class="choice"><span class="choice-title"><input type="radio" name="transport" value="auto">自动回退</span><span class="hint">先使用 Provider；套餐不支持 API 时改用 CLI。</span></label></div><details class="advanced"><summary>高级设置</summary><p class="hint">端点和客户端参数通常保持当前值即可。留空使用插件默认值。</p><div class="advanced-grid"><div><label for="base-url">Provider 地址</label><input id="base-url" data-setting="base_url" type="url" placeholder="https://api.commandcode.ai/provider/v1"></div><div><label for="cli-base-url">CLI 地址</label><input id="cli-base-url" data-setting="cli_base_url" type="url" placeholder="https://api.commandcode.ai"></div><div><label for="cli-version">CLI 版本</label><input id="cli-version" data-setting="cli_version" placeholder="1.73.0"></div><div><label for="cli-working-dir">CLI 工作目录</label><input id="cli-working-dir" data-setting="cli_working_dir" placeholder="/tmp"></div><div><label for="cli-user-agent">CLI User-Agent</label><input id="cli-user-agent" data-setting="cli_user_agent" placeholder="使用插件默认标识"></div><div><label for="priority">模型路由优先级</label><input id="priority" data-setting="priority" type="number" step="1"><p class="hint">多个插件声明同一模型时，数值越高越优先。</p></div></div></details></section>
</fieldset></form></main>
<footer class="footer"><div class="footer-inner"><div class="footer-state"><strong id="save-state" role="status" aria-live="polite">等待连接</strong><div class="footer-note" id="save-note">保存后自动生效，无需重启 CPA。</div></div><div class="toolbar footer-actions" style="width:auto"><button type="button" id="reload" disabled>重新读取</button><button type="button" id="save-settings" class="primary" disabled>保存配置</button></div></div></footer>
<script>
(function () {
  'use strict';
  var ROOT='/v0/management/commandcode', TOKEN_KEY='commandcode-management-key';
  var seq=0, mgmtKey='', authFailed=false, draft=null, saved=null, dirty=false, saving=false, loading=false, statusLoading=false, epoch=0;
  var accounts=Object.create(null), testResults=Object.create(null), runningTests=Object.create(null);
  var $=function (id) { return document.getElementById(id); };
  function esc(value) { return String(value==null?'':value).replace(/[&<>"']/g,function (c) { return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]; }); }
  function safeError(value) {
    var text=typeof value==='object' && value!==null?JSON.stringify(value):String(value==null?'':value), secrets=[mgmtKey];
    if(draft) draft.keys.forEach(function(k){ if(k.key) secrets.push(k.key); if(k.proxy_url) secrets.push(k.proxy_url); });
    secrets.forEach(function(s){ if(s) text=text.split(s).join('[已隐藏凭据]'); });
    return text.replace(/Bearer\s+[^\s"']+/gi,'Bearer [已隐藏]').replace(/\b(?:sk|cc|cmd)[-_][A-Za-z0-9_-]{16,}/g,'[已隐藏密钥]').replace(/(https?:\/\/)[^\s/@]+:[^\s/@]+@/g,'$1[已隐藏凭据]@');
  }
  function notice(id,text){ $(id).hidden=!text; $(id).textContent=text?safeError(text):''; }
  function badge(text,kind){ return '<span class="badge '+(kind||'')+'">'+esc(text)+'</span>'; }
  function kv(label,value){ return '<div class="kv"><span>'+esc(label)+'</span><span>'+esc(value==null || value===''?'—':value)+'</span></div>'; }
  function connected(text,kind){ $('connection-state').className='badge '+(kind||''); $('connection-state').textContent=text; }
  function setSaveState(text,note){ $('save-state').textContent=text; if(note!=null) $('save-note').textContent=note; }
  function controls(){
    $('editable').disabled=saving || loading; $('connect').disabled=saving || loading; $('forget').disabled=saving || loading;
    $('reload').disabled=!draft || saving || loading; $('save-settings').disabled=!draft || !dirty || saving || loading || authFailed;
    $('refresh-status').disabled=statusLoading || saving || authFailed;
  }
  function changed(){ dirty=true; setSaveState('有未保存的修改','保存后自动生效；账号查询与调用测试使用已生效配置。'); controls(); }
  function deobfuscate(stored){
    if(!stored || stored.indexOf('enc::v1::')!==0) return stored||'';
    try {
      var material=new TextEncoder().encode('cli-proxy-api-webui::secure-storage|'+window.location.host+'|'+navigator.userAgent);
      var bin=atob(stored.slice(9)),bytes=new Uint8Array(bin.length);
      for(var i=0;i<bin.length;i++) bytes[i]=bin.charCodeAt(i)^material[i%material.length];
      var text=new TextDecoder().decode(bytes);
      try { var parsed=JSON.parse(text); if(typeof parsed==='string') return parsed; if(parsed && typeof parsed.value==='string') return parsed.value; } catch(e){ /* raw value */ }
      return text;
    } catch(e){ return ''; }
  }
  function readManagementKey(){
    try { var own=localStorage.getItem(TOKEN_KEY); if(own){ $('remember').checked=true; return own; } } catch(e){ /* optional storage */ }
    try { var key=deobfuscate(localStorage.getItem('managementKey')); if(key) return key; } catch(e){ /* optional panel storage */ }
    try { var session=deobfuscate(sessionStorage.getItem('managementKey')); if(session) return session; } catch(e){ /* optional panel storage */ }
    try { var state=(JSON.parse(deobfuscate(localStorage.getItem('cli-proxy-auth')))||{}).state||{}; if(typeof state.managementKey==='string') return state.managementKey; } catch(e){ /* optional panel storage */ }
    return '';
  }
  async function request(path,options,timeout){
    if(!mgmtKey || authFailed) throw new Error('请在上方输入有效的 CPA 管理密钥并连接。');
    var requestEpoch=epoch,controller=new AbortController(),timer=setTimeout(function(){ controller.abort(); },timeout||30000),opts=Object.assign({},options||{});
    opts.headers={'Accept':'application/json','Authorization':'Bearer '+mgmtKey}; if(opts.body) opts.headers['Content-Type']='application/json';
    opts.cache='no-store'; opts.signal=controller.signal;
    try {
      var response=await fetch(ROOT+path,opts),raw=await response.text(),body;
      try { body=JSON.parse(raw); } catch(e){ body=null; }
      if(!response.ok){
        if(response.status===401 || response.status===403){ if(requestEpoch===epoch){ authFailed=true; connected('鉴权失败','error'); controls(); } throw Object.assign(new Error('HTTP '+response.status+'：管理密钥无效或访问被拒绝，请重新连接。已停止后续请求。'),{status:response.status}); }
        var detail=body && (body.error||body.message) || raw.slice(0,1200);
        throw Object.assign(new Error('HTTP '+response.status+(detail?'：'+safeError(detail):'')),{status:response.status});
      }
      if(!body || typeof body!=='object') throw new Error('接口返回了无效的 JSON 数据。');
      return body;
    } catch(error){ if(error.name==='AbortError') throw new Error('请求超时，请稍后手动重试。'); throw error; }
    finally { clearTimeout(timer); }
  }
  function applySettings(data){
    saved=data; draft={revision:data.revision,transport:data.transport||'provider',keys:[],models:[]};
    ['cli_version','cli_base_url','cli_working_dir','cli_user_agent','base_url','priority'].forEach(function(key){ draft[key]=data[key]==null?(key==='priority'?0:''):data[key]; });
    draft.keys=(data.keys||[]).map(function(k){ return {_uid:++seq,id:k.id,label:k.label||'已保存密钥',name:k.name||'',weight:k.weight==null?1:k.weight,disabled:!!k.disabled,proxy:!!k.proxy,key:'',proxy_url:'',clear_proxy:false,_deleted:false}; });
    draft.models=(data.models||[]).map(function(m){ return {_uid:++seq,alias:m.alias||'',name:m.name||m.upstream||'',display_name:m.display_name||''}; });
    dirty=false; $('initial').hidden=true; $('editor').hidden=false;
    document.querySelectorAll('[data-setting]').forEach(function(input){ input.value=draft[input.dataset.setting]; });
    document.querySelectorAll('[name=transport]').forEach(function(input){ input.checked=input.value===draft.transport; });
    renderKeys(); renderModels(); controls();
  }
  function field(label,id,type,value,attr){ return '<div><label for="'+id+'">'+esc(label)+'</label><input id="'+id+'" type="'+type+'" value="'+esc(value)+'" '+(attr||'')+'></div>'; }
  function renderKeys(){
    if(!draft) return;
    $('key-count').textContent=draft.keys.filter(function(k){ return !k._deleted; }).length+' 个密钥';
    $('keys').innerHTML=draft.keys.length?draft.keys.map(function(k,i){
      var p='key-'+k._uid;
      var header='<div class="key-header"><div class="key-title"><h3>'+esc(k.name||('密钥 '+(i+1)))+'</h3>'+badge(k.id?'已保存 · '+k.label:'新增 · 待保存')+badge(k._deleted?'待删除':(k.disabled?'已禁用':'已启用'),k._deleted||k.disabled?'warn':'ok')+'</div><button type="button" class="'+(k._deleted?'':'danger')+'" data-key-action="delete" data-uid="'+k._uid+'">'+(k._deleted?'撤销删除':'删除')+'</button></div>';
      if(k._deleted) return '<article class="key-card removed">'+header+'<p class="hint">保存配置后才会删除。现在可以撤销。</p></article>';
      return '<article class="key-card" data-key-card="'+k._uid+'">'+header+'<div class="fields">'+field('备注名称',p+'-name','text',k.name,'data-key-field="name" data-uid="'+k._uid+'" placeholder="例如：主账号"')+'<div class="key-secret"><label for="'+p+'-secret">'+(k.id?'替换密钥（留空保留）':'CommandCode 密钥')+'</label><input id="'+p+'-secret" type="password" autocomplete="new-password" data-key-field="key" data-uid="'+k._uid+'" placeholder="'+(k.id?'已保存；输入新密钥才会替换':'粘贴 CommandCode API key')+'"></div>'+field('分配权重',p+'-weight','number',k.weight,'min="1" step="1" data-key-field="weight" data-uid="'+k._uid+'"')+'</div><label class="check"><input type="checkbox" data-key-field="disabled" data-uid="'+k._uid+'" '+(k.disabled?'checked':'')+'>禁用此密钥（暂停分配请求）</label><details class="proxy" '+(k.proxy_url||k.clear_proxy?'open':'')+'><summary>独立代理 <span class="count">'+(k.proxy?'已设置':'未设置')+'</span></summary><div class="proxy-grid"><div><label for="'+p+'-proxy">代理地址（留空保留当前设置）</label><input id="'+p+'-proxy" type="password" autocomplete="new-password" data-key-field="proxy_url" data-uid="'+k._uid+'" placeholder="http:// 或 socks5:// 代理地址" '+(k.clear_proxy?'disabled':'')+'></div><label class="check"><input type="checkbox" data-key-field="clear_proxy" data-uid="'+k._uid+'" '+(k.clear_proxy?'checked':'')+'>清除代理</label></div><p class="hint">代理中如含用户名与密码，也只会提交给插件，不保存在浏览器存储。</p></details><div class="account" id="'+p+'-account"></div><div id="'+p+'-test"></div></article>';
    }).join(''):'<div class="empty">还没有配置密钥，点击“新增密钥”添加第一个账号。</div>';
    draft.keys.forEach(function(k){ if(k._deleted) return; $('key-'+k._uid+'-secret').value=k.key||''; $('key-'+k._uid+'-proxy').value=k.proxy_url||''; renderAccount(k); renderTest(k); });
  }
  function numeric(value,precision){ var n=Number(value); return value==null || value==='' || !isFinite(n)?'—':n.toLocaleString('zh-CN',{maximumFractionDigits:precision==null?4:precision}); }
  function windowHTML(label,data){
    if(!data) return '';
    var used=Number(data.used),cap=Number(data.cap),percent=cap>0 && isFinite(used)?Math.max(0,Math.min(100,used/cap*100)):0;
    var reset=data.resetAt,date=new Date(typeof reset==='number' && reset<100000000000?reset*1000:reset);
    return '<div class="quota-line">'+kv(label,numeric(data.used,3)+' / '+numeric(data.cap,3))+'<div class="meter"><i style="width:'+percent+'%"></i></div>'+(data.exceeded?badge('已超限','error'):'')+(reset && !isNaN(date.getTime())?'<div class="hint">重置时间：'+esc(date.toLocaleString('zh-CN'))+'</div>':'')+'</div>';
  }
  function renderAccount(k){
    var target=$('key-'+k._uid+'-account'); if(!target) return;
    if(!k.id){ target.innerHTML='<p class="hint">保存后可查询此账号和额度，并进行调用测试。</p>'; return; }
    if(k.disabled){ target.innerHTML='<p class="hint">此密钥已设为禁用，暂停查询账号和额度。修改后需保存生效。</p>'; return; }
    var row=accounts[k.id];
    if(!row){ target.innerHTML='<p class="loading">'+(statusLoading?'正在查询账号与额度…':'尚未查询账号信息。点击“刷新账号与额度”读取。')+'</p>'; return; }
    if(row.disabled){ target.innerHTML='<p class="hint">已生效配置中此密钥为禁用状态，未查询上游。启用并保存后可刷新。</p>'; return; }
    var a=row.account||{},user=a.whoami && a.whoami.user||{},sub=a.subscription && a.subscription.data||{},credits=a.credits||{},cr=credits.credits||{},windows=credits.windowLimits||{};
    var left=kv('账号',user.email||user.userName||user.name||'未获取')+(user.name?kv('用户名',user.name):'')+kv('订阅状态',sub.status||'未获取');
    if(sub.createdAt) left+=kv('开通时间',String(sub.createdAt).slice(0,10));
    var right=kv('月度额度',numeric(cr.monthlyCredits))+kv('购买额度',numeric(cr.purchasedCredits))+kv('免费额度',numeric(cr.freeCredits))+windowHTML('5 小时窗口',windows.fiveHour)+windowHTML('每周窗口',windows.weekly);
    var errors=[['查询',a.error],['账号',a.whoami_error],['额度',a.credits_error||credits.credits_error],['订阅',a.subscription_error]].filter(function(e){ return !!e[1]; }).map(function(e){ return e[0]+'：'+safeError(e[1]); });
    target.innerHTML='<div class="account-grid"><div>'+left+'</div><div>'+right+'</div></div>'+(errors.length?'<div class="errors">'+esc(errors.join('\n'))+'</div>':'');
  }
  function renderTest(k){
    var target=$('key-'+k._uid+'-test'); if(!target) return;
    if(!k.id || k.disabled){ target.innerHTML=''; return; }
    var models=saved && saved.models||[],result=testResults[k.id],busy=!!runningTests[k.id];
    target.innerHTML='<div class="test"><div><label for="test-model-'+k._uid+'">测试已保存的模型</label><select id="test-model-'+k._uid+'" '+(!models.length||busy?'disabled':'')+'>'+models.map(function(m){ return '<option value="'+esc(m.alias)+'">'+esc(m.display_name?m.alias+' · '+m.display_name:m.alias)+'</option>'; }).join('')+'</select></div><button type="button" data-key-action="test" data-uid="'+k._uid+'" '+(!models.length||busy||authFailed?'disabled':'')+'>'+(busy?'正在测试…':'测试调用')+'</button></div><p class="hint">会发送一次简短请求，消耗少量额度。测试使用已保存的密钥、代理与模型。</p>'+(result?'<div class="test-result" role="status">'+esc(safeError(result))+'</div>':'');
  }
  function renderModels(){
    $('models').innerHTML=draft.models.length?draft.models.map(function(m){ var p='model-'+m._uid; return '<div class="model-row">'+field('调用名称',p+'-alias','text',m.alias,'data-model-field="alias" data-uid="'+m._uid+'" placeholder="例如：deepseek-flash"')+field('上游模型',p+'-name','text',m.name,'data-model-field="name" data-uid="'+m._uid+'" placeholder="例如：deepseek/deepseek-v4.1-flash"')+'<div class="model-display"><label for="'+p+'-display">显示名称（选填）</label><input id="'+p+'-display" value="'+esc(m.display_name)+'" data-model-field="display_name" data-uid="'+m._uid+'"></div><button type="button" class="danger" data-model-delete="'+m._uid+'" aria-label="删除此模型映射">删除</button></div>'; }).join(''):'<div class="empty">暂无自定义映射。添加后可设置客户端调用名称与上游模型。</div>';
  }
  async function refreshStatus(force){
    if(statusLoading || authFailed || !draft) return;
    statusLoading=true; controls(); var currentEpoch=epoch,currentRevision=saved && saved.revision; draft.keys.forEach(renderAccount);
    try {
      var data=await request('/status'+(force?'?refresh=1':''),null,60000); if(currentEpoch!==epoch || !saved || saved.revision!==currentRevision) return;
      accounts=Object.create(null); (data.accounts||[]).forEach(function(a){ accounts[a.id]=a; });
      if(data.version) $('version').textContent='插件 v'+data.version;
      $('status-time').textContent='最近查询：'+new Date().toLocaleString('zh-CN')+' · 额度由 CommandCode 返回';
    } catch(error){ if(currentEpoch===epoch) notice('error','查询账号失败：'+error.message); }
    finally { if(currentEpoch===epoch){ statusLoading=false; if(draft) draft.keys.forEach(renderAccount); controls(); } }
  }
  async function loadSettings(){
    if(loading || saving) return;
    loading=true; controls(); notice('error',''); notice('message',''); connected('正在连接'); var currentEpoch=epoch,success=false;
    try {
      var data=await request('/settings'); if(currentEpoch!==epoch) return;
      accounts=Object.create(null); testResults=Object.create(null); applySettings(data); connected('已连接','ok'); success=true;
      $('key-state').textContent='管理密钥已连接'+($('remember').checked?'，此浏览器已记住凭据。':'，仅在当前页面使用。');
      if(String(data.runtime_revision)===String(data.revision)) setSaveState('配置已生效','保存后自动生效，无需重启 CPA。');
      else setSaveState('已保存，等待生效','配置版本与运行版本尚未一致，可稍后重新读取确认。');
    } catch(error){ if(currentEpoch===epoch){ notice('error','读取配置失败：'+error.message); if(!authFailed) connected('连接失败','error'); setSaveState(dirty?'修改尚未保存':'连接失败','检查管理密钥或网络后手动重试。'); } }
    finally { loading=false; controls(); }
    if(success && currentEpoch===epoch && !authFailed) refreshStatus(false);
  }
  function buildPayload(){
    var body={revision:draft.revision,transport:draft.transport};
    ['cli_version','cli_base_url','cli_working_dir','cli_user_agent','base_url'].forEach(function(key){ body[key]=String(draft[key]||'').trim(); });
    body.priority=Number(draft.priority); if(!Number.isInteger(body.priority)) throw new Error('模型路由优先级必须是整数。');
    body.keys=draft.keys.filter(function(k){ return !k._deleted; }).map(function(k,i){
      var weight=Number(k.weight); if(!Number.isInteger(weight)||weight<1) throw new Error('第 '+(i+1)+' 个密钥的权重必须是大于 0 的整数。');
      var item={name:k.name.trim(),weight:weight,disabled:!!k.disabled}; if(k.id) item.id=k.id;
      if(k.key.trim()) item.key=k.key.trim(); else if(!k.id) throw new Error('请填写第 '+(i+1)+' 个新增账号的 CommandCode 密钥。');
      if(k.clear_proxy) item.clear_proxy=true; else if(k.proxy_url.trim()) item.proxy_url=k.proxy_url.trim(); return item;
    });
    var aliases=Object.create(null);
    body.models=draft.models.map(function(m,i){ var alias=m.alias.trim(),name=m.name.trim(); if(!alias) throw new Error('第 '+(i+1)+' 个模型的调用名称不能为空。'); if(aliases[alias]) throw new Error('模型调用名称不能重复：'+alias); aliases[alias]=true; return {alias:alias,name:name,display_name:m.display_name.trim()}; });
    return body;
  }
  async function saveSettings(){
    if(saving || !dirty || !draft) return;
    notice('error',''); notice('message',''); var body;
    try { body=buildPayload(); } catch(error){ notice('error',error.message); return; }
    if(!$('editor').reportValidity()) return;
    saving=true; controls(); setSaveState('正在保存…','正在提交配置，请稍候。'); var persisted=false;
    try {
      var result=await request('/settings',{method:'POST',body:JSON.stringify(body)});
      if(!result.saved || result.revision==null) throw new Error('接口未确认配置已保存，请重新读取核实。');
      persisted=true; var expected=String(result.revision),deadline=Date.now()+20000; draft.revision=result.revision; dirty=false;
      setSaveState('已保存，正在生效…','正在等待插件加载新配置，无需重启 CPA。');
      while(Date.now()<deadline){
        var current=await request('/settings',null,Math.max(1,deadline-Date.now()));
        if(String(current.revision)!==expected) throw new Error('配置已被其他页面更新，请重新读取当前配置。');
        applySettings(current);
        if(String(current.runtime_revision)===expected){ setSaveState('配置已生效','所有修改已应用。账号信息可点击“刷新账号与额度”更新。'); notice('message','配置已保存并生效。'); accounts=Object.create(null); testResults=Object.create(null); renderKeys(); return; }
        await new Promise(function(resolve){ setTimeout(resolve,Math.min(800,Math.max(0,deadline-Date.now()))); });
      }
      throw new Error('已保存，但 20 秒内未确认运行配置生效。请稍后点击“重新读取”核实。');
    } catch(error){
      if(error.status===409){ notice('error','配置已被其他页面修改（HTTP 409）。你的编辑仍保留。请先记录需要保留的内容，再点击“重新读取”合并后保存。'); setSaveState('保存冲突，编辑已保留','重新读取会提示确认，不会自动覆盖当前编辑。'); }
      else { notice('error',error.message); setSaveState(persisted?'已保存，尚未确认生效':'保存失败，编辑已保留',persisted?'请点击“重新读取”核实运行状态。':'修正错误后可以重新保存。'); }
    } finally { saving=false; controls(); }
  }
  async function runTest(k){
    if(!k.id || k.disabled || runningTests[k.id] || authFailed) return;
    var selected=$('test-model-'+k._uid),model=selected && selected.value; if(!model) return;
    runningTests[k.id]=true; testResults[k.id]=''; renderTest(k); var currentEpoch=epoch;
    try {
      var result=await request('/test',{method:'POST',body:JSON.stringify({id:k.id,model:model})},90000); if(currentEpoch!==epoch) return;
      testResults[k.id]=(result.ok?'调用成功':'调用失败')+' · '+(result.model||model)+(result.status?' · HTTP '+result.status:'')+(result.seconds!=null?' · '+numeric(result.seconds,2)+' 秒':'')+'\n'+safeError(result.error||result.text||'接口未返回文本');
    } catch(error){ if(currentEpoch===epoch) testResults[k.id]='调用失败：'+safeError(error.message); }
    finally { delete runningTests[k.id]; if(currentEpoch===epoch) renderTest(k); }
  }
  $('keys').addEventListener('input',function(event){
    var input=event.target,field=input.dataset.keyField; if(!field || !draft) return;
    var k=draft.keys.find(function(row){ return row._uid===Number(input.dataset.uid); }); if(!k) return;
    k[field]=input.type==='checkbox'?input.checked:input.value;
    if(field==='clear_proxy'){ var proxy=$('key-'+k._uid+'-proxy'); proxy.disabled=input.checked; if(input.checked){ k.proxy_url=''; proxy.value=''; } }
    if(field==='disabled') renderKeys(); changed();
  });
  $('keys').addEventListener('click',function(event){ var button=event.target.closest('[data-key-action]'); if(!button || !draft) return; var k=draft.keys.find(function(row){ return row._uid===Number(button.dataset.uid); }); if(!k) return; if(button.dataset.keyAction==='delete'){ k._deleted=!k._deleted; changed(); renderKeys(); } if(button.dataset.keyAction==='test') runTest(k); });
  $('models').addEventListener('input',function(event){ var input=event.target,field=input.dataset.modelField; if(!field || !draft) return; var m=draft.models.find(function(row){ return row._uid===Number(input.dataset.uid); }); if(m){ m[field]=input.value; changed(); } });
  $('models').addEventListener('click',function(event){ var button=event.target.closest('[data-model-delete]'); if(!button || !draft) return; draft.models=draft.models.filter(function(m){ return m._uid!==Number(button.dataset.modelDelete); }); changed(); renderModels(); });
  $('add-key').addEventListener('click',function(){ var k={_uid:++seq,name:'',key:'',weight:1,disabled:false,proxy:false,proxy_url:'',clear_proxy:false}; draft.keys.push(k); changed(); renderKeys(); $('key-'+k._uid+'-secret').focus(); });
  $('add-model').addEventListener('click',function(){ var m={_uid:++seq,alias:'',name:'',display_name:''}; draft.models.push(m); changed(); renderModels(); $('model-'+m._uid+'-alias').focus(); });
  document.querySelectorAll('[data-setting]').forEach(function(input){ input.addEventListener('input',function(){ if(draft){ draft[input.dataset.setting]=input.value; changed(); } }); });
  document.querySelectorAll('[name=transport]').forEach(function(input){ input.addEventListener('change',function(){ if(draft && input.checked){ draft.transport=input.value; changed(); } }); });
  $('editor').addEventListener('submit',function(event){ event.preventDefault(); }); $('save-settings').addEventListener('click',saveSettings);
  $('refresh-status').addEventListener('click',function(){ refreshStatus(true); });
  $('reload').addEventListener('click',function(){ if(dirty && !window.confirm('重新读取将放弃当前未保存的修改，是否继续？')) return; loadSettings(); });
  $('connect').addEventListener('click',function(){
    var value=$('token').value.trim()||mgmtKey; if(!value){ notice('error','请先输入 CPA 管理密钥。'); $('token').focus(); return; }
    if(dirty && !window.confirm('重新连接并读取会放弃未保存的修改，是否继续？')) return;
    epoch++; statusLoading=false; mgmtKey=value; authFailed=false; $('token').value='';
    try { if($('remember').checked) localStorage.setItem(TOKEN_KEY,value); else localStorage.removeItem(TOKEN_KEY); } catch(e){ notice('message','浏览器无法保存凭据，当前页面仍可正常使用。'); }
    loadSettings();
  });
  $('token').addEventListener('keydown',function(event){ if(event.key==='Enter'){ event.preventDefault(); $('connect').click(); } });
  $('forget').addEventListener('click',function(){
    if(dirty && !window.confirm('清除连接会放弃当前未保存的修改，是否继续？')) return;
    epoch++; try { localStorage.removeItem(TOKEN_KEY); } catch(e){ /* optional storage */ }
    mgmtKey=''; draft=null; saved=null; dirty=false; authFailed=false; statusLoading=false; accounts=Object.create(null); testResults=Object.create(null);
    $('token').value=''; $('keys').innerHTML=''; $('models').innerHTML=''; $('editor').hidden=true; $('initial').hidden=false; $('remember').checked=false;
    $('key-state').textContent='本页保存的管理凭据已清除。需要时可重新输入连接。'; connected('未连接'); setSaveState('等待连接','保存后自动生效，无需重启 CPA。'); notice('error',''); notice('message',''); controls();
  });
  window.addEventListener('beforeunload',function(event){ if(dirty || saving){ event.preventDefault(); event.returnValue=''; } });
  // Never import credentials from query strings or place them in URLs.
  mgmtKey=readManagementKey(); controls();
  if(mgmtKey){ $('key-state').textContent='已读取此浏览器中的管理凭据，正在连接…'; loadSettings(); }
})();
</script>
</body>
</html>`

func managementPage() pluginapi.ManagementResponse {
	h := http.Header{}
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	return pluginapi.ManagementResponse{StatusCode: http.StatusOK, Headers: h, Body: []byte(strings.TrimSpace(managementPageHTML))}
}
