// Offline DOM integration tests. See ui_interaction.README.md for setup.
const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const {execFileSync} = require('node:child_process');
const {JSDOM} = require(process.env.JSDOM_PATH || 'jsdom');
const html = fs.readFileSync(__dirname + '/webui.html', 'utf8');
let productionPages;
function rendered(language){
  if(!productionPages){
    const dir=fs.mkdtempSync(path.join(os.tmpdir(),'warpd-ui-locales-'));
    try {
      execFileSync(process.env.GO_BINARY||'go',['test','webui.go','webui_i18n_test.go','-run','^TestWebUILocalizationFixtures$','-count=1'],{cwd:__dirname,env:{...process.env,WARPD_UI_FIXTURES:dir},stdio:'pipe'});
      productionPages=Object.fromEntries(['en','ru'].map(locale=>[locale,fs.readFileSync(path.join(dir,locale+'.html'),'utf8')]));
    }finally{fs.rmSync(dir,{recursive:true,force:true});}
  }
  return productionPages[language];
}
function assertEnglishDOM(document){
  const body=document.body.cloneNode(true);
  body.querySelectorAll('script').forEach(node=>node.remove());
  assert.doesNotMatch(body.textContent,/[А-Яа-яЁё]/,'English UI-owned text contains Russian');
  for(const node of document.querySelectorAll('[aria-label],[placeholder],[title]')){
    for(const attr of ['aria-label','placeholder','title'])assert.doesNotMatch(node.getAttribute(attr)||'',/[А-Яа-яЁё]/,node.id+': '+attr);
  }
}
const keys = ['warp','masque','masqueh2','m2w'];
const clone = value => JSON.parse(JSON.stringify(value));
function status(){
  return {time:new Date().toISOString(), version:'offline-test', config:{check_period:30,dme_rescan_gap:300,fail_restart:3,fail_rescan:5,scan_sample:2,scan_jobs:4,max_ping:0,autoscan_start:true,exclude_nodes:'DME',exclude_countries:'RU'},
    scan:{scanning:false,log:[]}, mihomo:{running:true,pid:123,generation:4}, proxies:Object.fromEntries(keys.map((key,i)=>[key,{enabled:true,running:true,ready:true,transport_ready:true,policy_ready:true,foreign_ip_verified:key==='m2w',last_check:new Date().toISOString(),checks:10,ok_checks:10,fail_streak:0,endpoint:'192.0.2.1:2408',outer:key==='m2w'?'192.0.2.2:443':'',listen:'0.0.0.0',port:1080+i,exit_node:'FRA',exit_node_country:'DE',exit_country:'DE',checked_generation:4}]))};
}
const response=(data,code=200)=>({ok:code>=200&&code<300,status:code,json:async()=>clone(data)});
async function settle(){for(let i=0;i<8;i++) await new Promise(resolve=>setImmediate(resolve));}
async function mount(options={}){
  const state=status(), calls=[], timers=[];
  let handler=options.handler;
  const dom=new JSDOM(options.html||html,{url:options.url||'http://console.test/',runScripts:'dangerously',pretendToBeVisual:true,beforeParse(w){
    w.setInterval=(fn)=>{timers.push(fn);return timers.length;};
    w.confirm=()=>false;
    w.fetch=async(path,opts={})=>{
      calls.push({path,method:opts.method||'GET',headers:clone(opts.headers||{}),body:opts.body?JSON.parse(opts.body):undefined});
      if(handler){const result=await handler(path,opts,state);if(result)return result;}
      if(path==='/api/status') return response(state);
      if(path==='/api/log') return response({lines:[]});
      if(path==='/api/proxy'){const b=JSON.parse(opts.body);state.proxies[b.proxy].enabled=b.enabled;return response({ok:true});}
      return response({ok:true});
    };
  }});
  await settle();
  return {dom,w:dom.window,d:dom.window.document,state,calls,timers,setHandler:fn=>{handler=fn;},close:()=>dom.window.close()};
}
test('production-rendered English first paint has localized static text before JavaScript',()=>{
  const page=new JSDOM(rendered('en'));
  try {
    const d=page.window.document;
    assert.equal(d.documentElement.lang,'en');
    assert.equal(d.title,'warpd — proxy management');
    assert.equal(d.getElementById('connection').textContent,'Connecting…');
    assert.equal(d.getElementById('updated').textContent,'Waiting for status');
    assert.equal(d.getElementById('token').placeholder,'API token');
    assertEnglishDOM(d);
  }finally{page.window.close();}
});

test('production-rendered English startup widgets stay English while status is pending',async()=>{
  let release;const gate=new Promise(resolve=>{release=resolve;});
  const app=await mount({html:rendered('en'),handler:async p=>p==='/api/status'?gate:null});
  try {
    assert.equal(app.d.getElementById('warp-toggle').disabled,true);
    assert.equal(app.d.getElementById('connection').textContent,'Connecting…');
    assert.equal(app.d.querySelectorAll('[role="switch"]').length,4);
    assert.equal(app.d.querySelectorAll('[data-config]').length,10);
    assert.equal(app.d.getElementById('warp-epin').placeholder,'IP:port');
    assert.equal(app.d.getElementById('m2w-toggle').getAttribute('aria-label'),'MASQUE → WARP: enable or disable');
    assertEnglishDOM(app.d);
  }finally{release(response(app.state));await settle();app.close();}
});

test('production-rendered English status translates readiness details and date locale without changing codes',async()=>{
  const app=await mount({html:rendered('en')});
  try {
    assert.equal(app.d.getElementById('connection').textContent,'Connected to server');
    assert.equal(app.d.getElementById('warp-toggle-label').textContent,'On');
    assert.equal(app.d.getElementById('warp-policy').textContent,'Policy: suitable');
    assert.equal(app.d.getElementById('warp-ip-country').textContent,'DE');
    const iso='2026-10-18T08:09:00Z';
    assert.equal(app.w.fmtTime(iso),new app.w.Date(iso).toLocaleString('en-US'));
    assertEnglishDOM(app.d);
    app.state.proxies.warp.enabled=false;app.state.proxies.m2w.foreign_ip_verified=false;
    app.state.proxies.masque.transport_ready=false;app.state.proxies.masqueh2.checking=true;
    await app.w.refresh();
    assert.equal(app.d.getElementById('warp-toggle-label').textContent,'Off');
    assert.notEqual(app.d.getElementById('m2w-policy').className,'ok');
    assertEnglishDOM(app.d);
    app.state.proxies.warp.applied_enabled=false;
    app.state.proxies.warp.manual_endpoint=true;app.state.proxies.masque.running=false;
    await app.w.refresh();assertEnglishDOM(app.d);
  }finally{app.close();}
});

for(const language of ['en','ru'])test(`production ${language} pending/failed toggles stay truthful and localize offline/auth errors`,async()=>{
  const en=language==='en',app=await mount({html:rendered(language)});let release;
  try {
    const gate=new Promise(resolve=>{release=resolve;});
    app.setHandler(async p=>p==='/api/proxy'?gate:null);
    const sw=app.d.getElementById('warp-toggle');sw.click();await settle();
    assert.equal(sw.getAttribute('aria-checked'),'true');
    assert.equal(app.d.getElementById('warp-toggle-label').textContent,en?'Applying…':'Применяем…');
    assert.equal(sw.title,en?'Wait for the current request to finish':'Дождитесь завершения текущего запроса');
    release(response({applied:true}));await settle();
    assert.equal(sw.getAttribute('aria-checked'),'true');
    assert.match(app.d.getElementById('warp-feedback').textContent,en?/not confirmed/:/не подтверждено/);
    assert.notEqual(app.d.getElementById('warp-policy').className,'ok');
    if(en)assertEnglishDOM(app.d);
    app.setHandler(async p=>{if(p==='/api/status')throw new Error('offline');});await app.w.refresh();
    assert.equal(app.d.getElementById('message').textContent,en?'No connection to server':'Нет связи с сервером');
    assert.equal(sw.disabled,true);
    if(en)assertEnglishDOM(app.d);
    app.setHandler(async p=>p==='/api/status'?response({error:'unauthorized'},401):null);await app.w.refresh();
    assert.equal(app.d.getElementById('auth').hidden,false);
    assert.equal(app.d.getElementById('connection').textContent,en?'Token required':'Требуется токен');
    assert.equal(app.d.getElementById('message').textContent,en?'Access denied: check the API token':'Нет доступа: проверьте токен API');
    app.d.getElementById('token').value=String(Math.random());
    app.d.getElementById('auth-form').dispatchEvent(new app.w.Event('submit',{cancelable:true,bubbles:true}));await settle();
    assert.equal(app.d.getElementById('auth-error').textContent,en?'Could not connect. Check the token and server availability.':'Не удалось подключиться. Проверьте токен и доступность сервера.');
    if(en)assertEnglishDOM(app.d);
  }finally{app.close();}
});

for(const language of ['en','ru'])test(`production ${language} scanner queue/progress/timeout wording preserves async truth`,async()=>{
  const en=language==='en',app=await mount({html:rendered(language),handler:async p=>p==='/api/scan'?response({queued:true}):null});
  try {
    app.d.getElementById('scan-btn').click();await settle();
    assert.match(app.d.getElementById('scan-state').textContent,en?/Queued/:/очередь/);
    assert.match(app.d.getElementById('message').textContent,en?/queued/:/очередь/);
    assert.equal(app.d.getElementById('warp-toggle').disabled,true);
    if(en)assertEnglishDOM(app.d);
    const now=app.w.Date.now;app.w.Date.now=()=>now()+21000;app.w.renderAll();
    assert.match(app.d.getElementById('message').textContent,en?/not confirmed within 20 seconds/:/не подтверждён за 20 секунд/);
    app.w.Date.now=now;
    app.state.scan={scanning:true,for:'warp',started:new Date(Date.now()-65000).toISOString(),reason:'manual',log:['raw scan line'],last_result:'raw result'};
    await app.w.refresh();
    assert.match(app.d.getElementById('scan-state').textContent,en?/Searching · WARP · 1 min \d+ s · manual/:/Идёт поиск · WARP · 1 мин \d+ с · manual/);
    assert.equal(app.d.getElementById('scan-btn').textContent,en?'Search: please wait…':'Поиск: ожидайте…');
    assert.equal(app.d.getElementById('scan-log').textContent,'raw scan line');
    assert.equal(app.d.getElementById('restart-btn').disabled,true);
    if(en)assertEnglishDOM(app.d);
    app.state.scan.scanning=false;app.state.scan.last_scan=new Date().toISOString();await app.w.refresh();
    app.setHandler(async p=>p==='/api/scan'?response({queued:false}):null);
    app.d.getElementById('scan-btn').click();await settle();
    assert.match(app.d.getElementById('message').textContent,en?/not accepted/:/не принят/);
    if(en)assertEnglishDOM(app.d);
  }finally{app.close();}
});

for(const language of ['en','ru'])test(`production ${language} confirmations and shared-operation feedback are localized`,async()=>{
  const en=language==='en',app=await mount({html:rendered(language)});
  try {
    const prompts=[];app.w.confirm=text=>{prompts.push(text);return false;};
    for(const id of ['restart-btn','rotate-btn','newaccount-btn']){app.d.getElementById(id).click();await settle();}
    assert.equal(app.calls.filter(c=>c.method==='POST').length,0);
    assert.equal(prompts.length,3);
    assert.match(prompts[0],en?/all four services/:/всех четырёх сервисов/);
    assert.match(prompts[1],en?/Rotate the shared account keys/:/Обновить ключи общего аккаунта/);
    assert.match(prompts[2],en?/Replace the current shared account/:/Создать новый общий аккаунт/);
    if(en)for(const prompt of prompts)assert.doesNotMatch(prompt,/[А-Яа-яЁё]/);
    app.w.confirm=()=>true;
    for(const id of ['restart-btn','rotate-btn','newaccount-btn']){
      app.d.getElementById(id).click();await settle();
      assert.match(app.d.getElementById('message').textContent,en?/request accepted/:/Запрос .* принят/);
      assert.doesNotMatch(app.d.getElementById('message').textContent,en?/successfully|account created/:/успешно|аккаунт создан/);
      if(en)assertEnglishDOM(app.d);
    }
  }finally{app.close();}
});

for(const language of ['en','ru'])test(`production ${language} settings preserve dirty forms and localize verified saves`,async()=>{
  const en=language==='en',app=await mount({html:rendered(language)});
  try {
    const field=app.d.getElementById('cfg-check_period');field.value='45';field.dispatchEvent(new app.w.Event('input',{bubbles:true}));field.blur();
    assert.equal(app.d.getElementById('config-state').textContent,en?'Unsaved fields: 1':'Несохранённых полей: 1');
    app.state.config.check_period=60;await app.w.refresh();assert.equal(field.value,'45');
    let release;const gate=new Promise(resolve=>{release=resolve;});app.setHandler(async p=>p==='/api/config'?gate:null);
    app.d.getElementById('save-config').click();await settle();
    assert.equal(app.d.getElementById('message').textContent,en?'Sending request…':'Выполняется запрос…');
    if(en)assertEnglishDOM(app.d);
    app.state.config.check_period=45;release(response({applied:['check_period']}));await settle();
    assert.equal(app.d.getElementById('message').textContent,en?'Settings saved and confirmed by server.':'Настройки сохранены и подтверждены сервером.');
    assert.equal(app.d.getElementById('config-state').textContent,en?'No changes':'Нет изменений');
    app.setHandler(async p=>p==='/api/config'?response({error:'raw backend error'},400):null);
    field.value='50';field.dispatchEvent(new app.w.Event('input',{bubbles:true}));app.d.getElementById('save-config').click();await settle();
    assert.equal(field.value,'50');assert.match(app.d.getElementById('message').textContent,/raw backend error/);
    if(en)assertEnglishDOM(app.d);
    app.setHandler(null);app.d.getElementById('save-config').click();await settle();
    assert.match(app.d.getElementById('message').textContent,en?/Not all edits were confirmed/:/Не все правки подтверждены/);
    assert.equal(field.value,'50');
    app.d.getElementById('reset-config').click();assert.equal(field.value,'45');
    if(en)assertEnglishDOM(app.d);
  }finally{app.close();}
});

for(const language of ['en','ru'])test(`production ${language} redacts credentials while raw diagnostics stay inert and untranslated`,async()=>{
  const value=String(Math.random()),en=language==='en';
  const app=await mount({html:rendered(language),url:'http://console.test/?secret='+encodeURIComponent(value)+'&keep=1'});
  try {
    const raw='исходная диагностика <img src=x onerror=alert(1)> Bearer '+value+' ?secret='+value;
    app.state.scan.log=[raw];app.state.scan.reason='backend_reason';
    app.state.proxies.warp.error=raw;app.state.proxies.warp.policy_error=raw;
    app.state.proxies.warp.exit_info=raw;
    await app.w.refresh();
    assert.equal(app.w.location.search,'?keep=1');
    assert.equal(app.w.localStorage.length,0);assert.equal(app.w.sessionStorage.length,0);
    assert.ok(app.calls.every(c=>!c.path.includes(value)));
    for(const id of ['scan-log','warp-error','warp-policy-error','warp-exit']){
      const node=app.d.getElementById(id);
      assert.ok(!node.textContent.includes(value));
      assert.match(node.textContent,/исходная диагностика <img/);
      assert.match(node.textContent,en?/\[redacted\]/:/\[скрыто\]/);
      assert.equal(node.querySelector('img'),null);
    }
    app.setHandler(async p=>p==='/api/log'?response({lines:[raw]}):p==='/api/proxy'?response({error:raw},503):null);
    await app.w.refreshLog();app.d.getElementById('warp-toggle').click();await settle();
    assert.ok(!app.d.body.textContent.includes(value));
    assert.match(app.d.getElementById('main-log').textContent,/исходная диагностика/);
    assert.equal(app.d.getElementById('message').querySelector('img'),null);
    assert.equal(app.d.getElementById('warp-toggle').getAttribute('aria-checked'),'true');
  }finally{app.close();}
});

for(const language of ['en','ru'])test(`production ${language} endpoint and clipboard feedback remains row-local`,async()=>{
  const en=language==='en',app=await mount({html:rendered(language)});
  try {
    app.d.getElementById('warp-epin').value='192.0.2.10:443';
    app.d.getElementById('warp-apply').click();await settle();
    assert.deepEqual(app.calls.filter(c=>c.path==='/api/endpoint').at(-1).body,{proxy:'warp',endpoint:'192.0.2.10:443'});
    assert.match(app.d.getElementById('message').textContent,en?/Pin request for WARP accepted/:/Запрос закрепления WARP принят/);
    if(en)assertEnglishDOM(app.d);
    app.d.getElementById('masque-clearbtn').click();await settle();
    assert.deepEqual(app.calls.filter(c=>c.path==='/api/endpoint/clear').at(-1).body,{proxy:'masque'});
    assert.match(app.d.getElementById('message').textContent,en?/Automatic selection request for MASQUE accepted/:/Запрос автовыбора MASQUE принят/);
    app.d.getElementById('warp-copy').click();await settle();
    assert.equal(app.d.getElementById('message').textContent,en?'Could not copy. Select the address manually.':'Не удалось скопировать. Выделите адрес вручную.');
    Object.defineProperty(app.w.navigator,'clipboard',{value:{writeText:async()=>{}}});
    app.d.getElementById('warp-copy').click();await settle();
    assert.equal(app.d.getElementById('message').textContent,en?'SOCKS address copied':'Адрес SOCKS скопирован');
    if(en)assertEnglishDOM(app.d);
  }finally{app.close();}
});

for(const language of ['en','ru'])test(`production ${language} malformed, timed-out and stale status errors fail closed`,async()=>{
  const en=language==='en',app=await mount({html:rendered(language)});
  try {
    const cases=[
      [async()=>({ok:true,status:200,json:async()=>{throw new Error('malformed');}}),en?'Server returned an invalid response':'Сервер вернул некорректный ответ'],
      [async()=>response(null),en?'Incomplete server response':'Неполный ответ сервера'],
      [async()=>response({config:{},proxies:{}}),en?'Incomplete server status':'Неполный статус сервера'],
      [async()=>{const error=new Error('timeout');error.name='AbortError';throw error;},en?'Server response timed out':'Сервер не ответил вовремя']
    ];
    for(const [handler,want] of cases){
      app.setHandler(async p=>p==='/api/status'?handler():null);await app.w.refresh();
      assert.equal(app.d.getElementById('message').textContent,want);
      assert.equal(app.d.getElementById('warp-toggle').disabled,true);
      assert.notEqual(app.d.getElementById('warp-policy').className,'ok');
      if(en)assertEnglishDOM(app.d);
    }
    app.setHandler(null);await app.w.refresh();
    const now=app.w.Date.now;app.w.Date.now=()=>now()+16000;app.timers.at(-1)();
    assert.equal(app.d.getElementById('connection').textContent,en?'Status is stale':'Данные устарели');
    assert.equal(app.d.getElementById('warp-toggle').disabled,true);
    assert.equal(app.w.fmtTime('0001-01-01T00:00:00Z'),'—');
    assert.equal(app.w.fmtTime('not-a-date'),'—');
    assert.equal(app.w.fmtElapsed(-1),en?'0 min 0 s':'0 мин 0 с');
    if(en)assertEnglishDOM(app.d);
  }finally{app.close();}
});

test('fresh successful server checks recover a previous failed apply without stale error styling',async()=>{
  const app=await mount();
  try {
    app.setHandler(async path=>{if(path==='/api/proxy')throw new Error('offline');});
    app.d.getElementById('warp-toggle').click();await settle();
    assert.equal(app.d.getElementById('warp-feedback').hidden,false);
    app.state.proxies.warp.last_check=new Date(Date.now()+1000).toISOString();
    await app.w.refresh();
    assert.equal(app.d.getElementById('warp-feedback').hidden,true);
    assert.equal(app.d.getElementById('warp-transport').className,'ok');
  }finally{app.close();}
});

test('incomplete status fails closed instead of enabling missing service controls',async()=>{
  const app=await mount();
  try {
    delete app.state.proxies.m2w;await app.w.refresh();
    assert.equal(app.d.getElementById('warp-toggle').disabled,true);
    assert.equal(app.d.getElementById('m2w-toggle').disabled,true);
    assert.notEqual(app.d.getElementById('warp-policy').className,'ok');
  }finally{app.close();}
});
test('a network-failed toggle never becomes an optimistic persisted switch',async()=>{
  const app=await mount();
  try {
    app.setHandler(async path=>{if(path==='/api/proxy')throw new Error('offline');});
    app.d.getElementById('warp-toggle').click();await settle();
    assert.equal(app.d.getElementById('warp-toggle').getAttribute('aria-checked'),'true');
    assert.match(app.d.getElementById('warp-feedback').textContent,/Ошибка/);
    assert.notEqual(app.d.getElementById('warp-policy').className,'ok');
  }finally{app.close();}
});
test('poll begun before a toggle cannot overwrite its authoritative readback',async()=>{
  const app=await mount();let release;
  try {
    const oldState=clone(app.state);let block=true;const gate=new Promise(resolve=>{release=resolve;});
    app.setHandler(async path=>{if(path==='/api/status'&&block){block=false;return gate;}});
    const old=app.w.refresh();await settle();app.d.getElementById('warp-toggle').click();await settle();
    assert.equal(app.d.getElementById('warp-toggle').getAttribute('aria-checked'),'false');
    release(response(oldState));await old;
    assert.equal(app.d.getElementById('warp-toggle').getAttribute('aria-checked'),'false');
  }finally{app.close();}
});
test('HTTP success without matching status readback is not claimed as applied',async()=>{
  const app=await mount({handler:async path=>path==='/api/proxy'?response({applied:true}):null});
  try {
    app.d.getElementById('warp-toggle').click();await settle();
    assert.equal(app.d.getElementById('warp-toggle').getAttribute('aria-checked'),'true');
    assert.match(app.d.getElementById('warp-feedback').textContent,/не подтверждено/i);
  }finally{app.close();}
});
test('accepted-but-not-started scan remains queued with progress rather than completed',async()=>{
  const app=await mount({handler:async path=>path==='/api/scan'?response({queued:true}):null});
  try {
    app.d.getElementById('scan-btn').click();await settle();
    assert.match(app.d.getElementById('scan-state').textContent,/очередь/);
    assert.equal(app.d.getElementById('scan-spinner').hidden,false);
    assert.equal(app.d.getElementById('warp-toggle').disabled,true);
    assert.doesNotMatch(app.d.getElementById('message').textContent,/завершён|успешно/i);
  }finally{app.close();}
});
test('failed settings requests preserve unsaved data and do not show saved',async()=>{
  const app=await mount({handler:async path=>path==='/api/config'?response({error:'invalid config'},400):null});
  try {
    const input=app.d.getElementById('cfg-exclude_countries');input.value='RU,KZ';input.dispatchEvent(new app.w.Event('input',{bubbles:true}));
    app.d.getElementById('save-config').click();await settle();
    assert.equal(input.value,'RU,KZ');assert.match(app.d.getElementById('config-state').textContent,/Несохранённых/);
    assert.match(app.d.getElementById('message').textContent,/invalid config/);
    app.d.getElementById('reset-config').click();assert.equal(input.value,'RU');
  }finally{app.close();}
});

test('controls are disabled before the first status arrives',async()=>{
  let release;const gate=new Promise(resolve=>{release=resolve;});
  const app=await mount({handler:async path=>path==='/api/status'?gate:null});
  try {
    assert.equal(app.d.getElementById('warp-toggle').disabled,true);
    assert.equal(app.d.getElementById('restart-btn').disabled,true);
    release(response(app.state));await settle();
    assert.equal(app.d.getElementById('warp-toggle').disabled,false);
  }finally{release(response(app.state));app.close();}
});
test('obsolete authorization replies cannot lock out a newer token session',async()=>{
  const app=await mount();let release;
  try {
    const gate=new Promise(resolve=>{release=resolve;});let block=true;
    app.setHandler(async path=>{if(path==='/api/status'&&block){block=false;return gate;}});
    const old=app.w.refresh();await settle();
    // Generated test-only value, never written into fixtures or output.
    app.d.getElementById('token').value=String(Math.random());
    app.d.getElementById('auth-form').dispatchEvent(new app.w.Event('submit',{cancelable:true,bubbles:true}));await settle();
    release(response({error:'unauthorized'},401));await old;
    assert.equal(app.d.getElementById('auth').hidden,true);
    assert.equal(app.d.getElementById('warp-toggle').disabled,false);
    assert.equal(app.d.getElementById('token').value,'');
  }finally{app.close();}
});
test('failed HTTP applies read back desired truth and retain visible row errors',async()=>{
  const app=await mount({handler:async(path,opts,state)=>{if(path==='/api/proxy'){const body=JSON.parse(opts.body);state.proxies[body.proxy].enabled=body.enabled;state.proxies[body.proxy].transport_ready=false;return response({error:'apply failed',applied:false,desired_enabled:body.enabled},503);}}});
  try {
    app.d.getElementById('warp-toggle').click();await settle();
    assert.equal(app.d.getElementById('warp-toggle').getAttribute('aria-checked'),'false');
    assert.match(app.d.getElementById('warp-feedback').textContent,/apply failed/);
    assert.equal(app.d.getElementById('warp-feedback').hidden,false);
    assert.doesNotMatch(app.d.getElementById('warp-transport').className,/ok/);
    assert.match(app.d.getElementById('warp-transport').textContent,/не подтверждено/);
    assert.match(app.d.getElementById('message').textContent,/не удалось/i);
  }finally{app.close();}
});
test('network disconnect and expired status remove green readiness and block writes',async()=>{
  const app=await mount();
  try {
    assert.equal(app.d.getElementById('warp-transport').className,'ok');
    app.setHandler(async path=>{if(path==='/api/status')throw new Error('offline');});await app.w.refresh();
    for(const key of keys){assert.notEqual(app.d.getElementById(key+'-transport').className,'ok');assert.notEqual(app.d.getElementById(key+'-policy').className,'ok');assert.equal(app.d.getElementById(key+'-toggle').disabled,true);}
    app.setHandler(null);await app.w.refresh();
    const now=app.w.Date.now;app.w.Date.now=()=>now()+16000;
    app.timers.at(-1)();
    assert.equal(app.d.getElementById('restart-btn').disabled,true);
    assert.notEqual(app.d.getElementById('warp-transport').className,'ok');
  }finally{app.close();}
});
test('pending switch resists polling and double clicks and waits for server readback',async()=>{
  const app=await mount();let release;
  try {
    const gate=new Promise(resolve=>{release=resolve;});
    app.setHandler(async(path)=>path==='/api/proxy'?gate:null);
    const sw=app.d.getElementById('warp-toggle');sw.click();await settle();sw.click();
    assert.equal(sw.getAttribute('aria-checked'),'true');assert.equal(sw.getAttribute('aria-busy'),'true');
    assert.equal(sw.disabled,true);assert.match(sw.textContent,/Примен/);
    await app.w.refresh();assert.equal(sw.disabled,true);
    app.state.proxies.warp.enabled=false;release(response({applied:true}));await settle();
    assert.equal(sw.getAttribute('aria-checked'),'false');assert.equal(sw.getAttribute('aria-busy'),'false');
    assert.equal(app.calls.filter(c=>c.path==='/api/proxy').length,1);
  }finally{app.close();}
});
test('fail-closed policy distinguishes colo country, IP country, and independent M2W proof',async()=>{
  const app=await mount();
  try {
    app.state.proxies.warp.exit_country='RU';await app.w.refresh();
    assert.equal(app.d.getElementById('warp-policy').className,'ok');
    assert.match(app.d.getElementById('warp-colo').textContent,/DE/);
    assert.equal(app.d.getElementById('warp-ip-country').textContent,'RU');
    app.state.proxies.warp.exit_node='DME';app.state.proxies.warp.exit_node_country='RU';
    app.state.proxies.m2w.foreign_ip_verified=false;await app.w.refresh();
    assert.notEqual(app.d.getElementById('warp-policy').className,'ok');assert.notEqual(app.d.getElementById('m2w-policy').className,'ok');
    assert.equal(app.d.getElementById('warp-transport').className,'ok');
    assert.match(app.d.getElementById('m2w-geo').textContent,/не подтверждён/);
    app.state.proxies.masque.exit_node_country='';await app.w.refresh();assert.notEqual(app.d.getElementById('masque-policy').className,'ok');
  }finally{app.close();}
});
test('server text is rendered inertly and SOCKS copying uses actual listen and port',async()=>{
  const app=await mount();let copied;
  try {
    Object.defineProperty(app.w.navigator,'clipboard',{value:{writeText:async value=>{copied=value;}}});
    app.state.proxies.warp.exit_info='<img src=x onerror=alert(1)>';
    app.state.proxies.warp.listen='::1';await app.w.refresh();
    assert.equal(app.d.getElementById('warp-exit').querySelector('img'),null);
    assert.equal(app.d.getElementById('warp-exit').textContent,'<img src=x onerror=alert(1)>');
    app.d.getElementById('warp-copy').click();await settle();assert.equal(copied,'socks5h://[::1]:1080');
  }finally{app.close();}
});
test('legacy secret query is removed immediately and never persisted or sent in a URL',async()=>{
  const value=String(Math.random());
  const app=await mount({url:'http://console.test/?secret='+encodeURIComponent(value)+'&keep=1'});
  try {
    assert.equal(app.w.location.search,'?keep=1');
    assert.equal(app.w.localStorage.length,0);assert.equal(app.w.sessionStorage.length,0);
    assert.equal(app.calls[0].headers.Authorization,'Bearer '+value);
    assert.ok(app.calls.every(c=>!c.path.includes(value)));
    app.state.scan.log=['Bearer '+value];await app.w.refresh();
    assert.ok(!app.d.body.textContent.includes(value));
  }finally{app.close();}
});

test('authorization failure with a non-JSON body preserves the console and offers inline password entry',async()=>{
  const app=await mount();
  try {
    const field=app.d.getElementById('cfg-exclude_nodes');field.value='FRA';field.dispatchEvent(new app.w.Event('input',{bubbles:true}));field.blur();
    app.setHandler(async path=>path==='/api/status'?{ok:false,status:401,json:async()=>{throw new Error('not JSON');}}:null);
    await app.w.refresh();
    assert.equal(app.d.getElementById('cfg-exclude_nodes'),field);
    assert.equal(field.value,'FRA');
    assert.equal(app.d.getElementById('auth').hidden,false,'HTTP 401 must show token form even without JSON');
    assert.equal(app.d.getElementById('token').type,'password');
    assert.equal(app.d.getElementById('warp-toggle').disabled,true);
  }finally{app.close();}
});
test('endpoint forms prefill from status without losing local drafts after blur',async()=>{
  const app=await mount();
  try {
    const input=app.d.getElementById('warp-epin');
    assert.equal(input.value,'192.0.2.1:2408');
    input.value='192.0.2.50:443';input.dispatchEvent(new app.w.Event('input',{bubbles:true}));input.blur();
    app.state.proxies.warp.endpoint='192.0.2.60:443';await app.w.refresh();
    assert.equal(input.value,'192.0.2.50:443');
    assert.equal(app.d.getElementById('warp-ep').textContent,'192.0.2.60:443');
  }finally{app.close();}
});

test('rejected scans are not called started; running scans expose progress and lock conflicting controls',async()=>{
  const app=await mount({handler:async(path)=>path==='/api/scan'?response({queued:false}):null});
  try {
    app.d.getElementById('scan-btn').click();await settle();
    assert.equal(app.calls.filter(c=>c.path==='/api/scan').length,1,'scan button sends request');
    assert.match(app.d.getElementById('message').textContent,/не принят/i);
    assert.doesNotMatch(app.d.getElementById('message').textContent,/запущен|успешно/i);
    app.state.scan={scanning:true,for:'warp',started:new Date(Date.now()-65000).toISOString(),reason:'manual',log:['candidate 192.0.2.1'],last_result:'previous result'};
    await app.w.refresh();
    assert.equal(app.d.getElementById('scan-spinner').hidden,false);
    assert.match(app.d.getElementById('scan-state').textContent,/1 мин/);
    assert.match(app.d.getElementById('scan-log').textContent,/candidate/);
    assert.equal(app.d.getElementById('scan-details').open,true);
    assert.equal(app.d.getElementById('warp-scan').disabled,true);
    assert.equal(app.d.getElementById('warp-apply').disabled,true);
    assert.equal(app.d.getElementById('restart-btn').disabled,true);
  }finally{app.close();}
});
test('destructive shared operations require explicit confirmation and preserve truthful async wording',async()=>{
  const app=await mount();
  try {
    for(const id of ['restart-btn','rotate-btn','newaccount-btn'])app.d.getElementById(id).click();
    await settle();assert.equal(app.calls.filter(c=>c.method==='POST').length,0);
    const prompts=[];app.w.confirm=text=>{prompts.push(text);return true;};
    app.d.getElementById('restart-btn').click();await settle();
    assert.equal(prompts.length,1,'restart asks for confirmation');
    assert.match(prompts[0],/всех|четыр/);
    assert.deepEqual(app.calls.filter(c=>c.path==='/api/restart').at(-1).body,{proxy:'all'});
    app.d.getElementById('newaccount-btn').click();await settle();
    assert.deepEqual(app.calls.filter(c=>c.path==='/api/register').at(-1).body,{fresh:true});
    assert.doesNotMatch(app.d.getElementById('message').textContent,/аккаунт создан|успешно/i);
  }finally{app.close();}
});

test('settings survive blur and polling; all current fields save with typed values and readback',async()=>{
  const app=await mount({handler:async(path,opts,state)=>{if(path==='/api/config'){Object.assign(state.config,JSON.parse(opts.body));return response({applied:JSON.parse(opts.body)});}}});
  try {
    const input=app.d.getElementById('cfg-check_period');assert.ok(input,'settings field exists');
    assert.equal(input.value,'30');assert.equal(app.d.getElementById('cfg-autoscan_start').checked,true);
    input.focus();input.value='45';input.dispatchEvent(new app.w.Event('input',{bubbles:true}));input.blur();
    app.state.config.check_period=60;await app.w.refresh();assert.equal(input.value,'45');
    const auto=app.d.getElementById('cfg-autoscan_start');auto.checked=false;auto.dispatchEvent(new app.w.Event('change',{bubbles:true}));
    app.d.getElementById('save-config').click();await settle();
    const post=app.calls.find(c=>c.path==='/api/config');assert.deepEqual(post.body,{check_period:45,autoscan_start:false});
    assert.match(app.d.getElementById('config-state').textContent,/Нет изменений/);
    assert.equal(app.state.config.exclude_nodes,'DME');
  }finally{app.close();}
});

test('row-local endpoint application never submits another open form',async()=>{
  const app=await mount();
  try {
    for(const key of keys){app.d.getElementById(key+'-details').open=true;}
    assert.ok(app.d.getElementById('warp-epin'),'row endpoint editor exists');
    app.d.getElementById('warp-epin').value='192.0.2.10:2408';
    app.d.getElementById('masque-epin').value='192.0.2.11:443';
    app.d.getElementById('masque-apply').click();await settle();
    assert.deepEqual(app.calls.filter(c=>c.path==='/api/endpoint').map(c=>c.body),[{proxy:'masque',endpoint:'192.0.2.11:443'}]);
    app.d.getElementById('m2w-outin').value='192.0.2.20:443';
    app.d.getElementById('m2w-inin').value='192.0.2.21:2408';
    app.d.getElementById('m2w-apply').click();await settle();
    assert.deepEqual(app.calls.filter(c=>c.path==='/api/endpoint').at(-1).body,{proxy:'m2w',outer:'192.0.2.20:443',inner:'192.0.2.21:2408'});
    app.d.getElementById('m2w-clearbtn').click();await settle();
    assert.deepEqual(app.calls.filter(c=>c.path==='/api/endpoint/clear').at(-1).body,{proxy:'m2w'});
  }finally{app.close();}
});

test('four proxy switches use the exact proxy contract and authoritative readback',async()=>{
  const app=await mount();
  try {
    assert.equal(app.d.querySelectorAll('[role="switch"]').length,4);
    for(const key of keys){
      const sw=app.d.getElementById(key+'-toggle');
      assert.equal(sw.getAttribute('aria-checked'),'true');
      sw.click();await settle();
      const posts=app.calls.filter(c=>c.method==='POST');
      assert.deepEqual(posts.at(-1),{path:'/api/proxy',method:'POST',headers:{'Content-Type':'application/json'},body:{proxy:key,enabled:false}});
      assert.equal(sw.getAttribute('aria-checked'),'false');
    }
  } finally {app.close();}
});
