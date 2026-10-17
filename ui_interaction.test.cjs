// Offline DOM integration tests. See ui_interaction.README.md for setup.
const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const {JSDOM} = require(process.env.JSDOM_PATH || 'jsdom');
const html = fs.readFileSync(__dirname + '/webui.html', 'utf8');
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
  const dom=new JSDOM(html,{url:options.url||'http://console.test/',runScripts:'dangerously',pretendToBeVisual:true,beforeParse(w){
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
