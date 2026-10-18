// Execute complete production JavaScript; mock HTTP boundary only.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { execFileSync } = require('node:child_process');
const { JSDOM } = require(process.env.JSDOM_PATH || 'jsdom');
let pages;
function rendered(language = 'ru') {
  if (!pages) {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'warpd-ui-locales-'));
    try {
      execFileSync(
        process.env.GO_BINARY || 'go',
        [
          'test',
          'webui.go',
          'webui_i18n_test.go',
          '-run',
          '^TestWebUILocalizationFixtures$',
          '-count=1',
        ],
        {
          cwd: __dirname,
          env: { ...process.env, WARPD_UI_FIXTURES: dir },
          stdio: 'pipe',
        },
      );
      pages = Object.fromEntries(
        ['ru', 'en'].map((l) => [
          l,
          fs.readFileSync(path.join(dir, l + '.html'), 'utf8'),
        ]),
      );
    } finally {
      fs.rmSync(dir, { recursive: true, force: true });
    }
  }
  return pages[language];
}
const clone = (x) => JSON.parse(JSON.stringify(x));
const awg = {
  preset: 'supported-v1',
  jc: 4,
  jmin: 40,
  jmax: 70,
  s1: 0,
  s2: 0,
  h1: 1,
  h2: 2,
  h3: 3,
  h4: 4,
  i1: '',
};
function profile(id, type = 'warp', extra = {}) {
  return {
    id,
    name: id,
    type,
    transport: type === 'masque' ? 'quic' : '',
    enabled: true,
    effective_enabled: true,
    listen: '0.0.0.0',
    port: 1080,
    endpoint: '192.0.2.1:2408',
    relay_id: '',
    relay_country: '',
    exclude_nodes: '',
    exclude_countries: '',
    effective_exclude_nodes: 'DME,SVO',
    effective_exclude_countries: 'RU',
    awg: clone(awg),
    ...extra,
  };
}
function status() {
  const profiles = [
    profile('p-a'),
    profile('p-b', 'warp', { port: 1081 }),
    profile('p-h2', 'masque', { port: 1082, transport: 'http2' }),
  ];
  return {
    revision: 1,
    profiles,
    relays: [],
    defaults: { awg },
    config: {
      check_period: 30,
      dme_rescan_gap: 300,
      fail_restart: 3,
      fail_rescan: 5,
      scan_sample: 2,
      scan_jobs: 4,
      max_ping: 0,
      autoscan_start: true,
      exclude_nodes: 'DME',
      exclude_countries: 'RU',
      web_language: 'ru',
    },
    scan: { scanning: false, log: [] },
    mihomo: { running: true, pid: 123, generation: 4 },
    proxies: Object.fromEntries(
      profiles.map((p) => [
        p.id,
        {
          ...p,
          running: true,
          transport_ready: true,
          policy_ready: true,
          ready: true,
          last_check: new Date().toISOString(),
          checks: 10,
          ok_checks: 10,
          exit_node: 'FRA',
          exit_node_country: 'DE',
          exit_country: 'RU',
        },
      ]),
    ),
  };
}
// DTOs mirror DTO-API-CONTRACT.md; no credential-bearing readback fixture.
function relayAccountState(extra = {}) {
  return { state: 'idle', pending: false, credential_mode: '',
    credential_configured: false, runtime_available: false, ...extra };
}
function withRelayProfile() {
  const s = status();
  const p = profile('p-relay', 'relay-warp', { relay_id: 'r-access',
    relay_country: 'DE', credential_mode: '', credential_configured: false });
  s.profiles.push(p);
  s.proxies[p.id] = { ...p, running: false, ready: false,
    transport_ready: false, policy_ready: false,
    error: 'Relay runtime unavailable' };
  s.relays.push({ id: 'r-access', name: 'Access', enabled: true,
    configured: true, path_confirmed: true, monitor_interval: 300,
    source_ip: '', interface: '', linked_profiles: 1 });
  return s;
}
const response = (data, code = 200) => ({
  ok: code >= 200 && code < 300,
  status: code,
  json: async () => clone(data),
});
async function settle() {
  for (let i = 0; i < 10; i++) await new Promise((r) => setImmediate(r));
}
async function mount(options = {}) {
  const state = options.state || status(),
    calls = [],
    timers = [],
    errors = [];
  let handler = options.handler;
  const dom = new JSDOM(rendered(options.language), {
    url: options.url || 'http://console.test/',
    runScripts: 'dangerously',
    pretendToBeVisual: true,
    beforeParse(w) {
      w.setInterval = (fn) => {
        timers.push(fn);
        return timers.length;
      };
      w.confirm = () => false;
      w.addEventListener('error', (e) => errors.push(e.error));
      w.fetch = async (p, opts = {}) => {
        const body = opts.body ? JSON.parse(opts.body) : undefined;
        calls.push({
          path: p,
          method: opts.method || 'GET',
          headers: clone(opts.headers || {}),
          body,
        });
        if (handler) {
          const r = await handler(p, opts, state);
          if (r) return r;
        }
        if (p === '/api/status') return response(state);
        if (p === '/api/log') return response({ lines: [] });
        if (p === '/api/config') {
          Object.assign(state.config, body);
          return response({ applied: true });
        }
        const m = p.match(/^\/api\/(profiles|relays)\/([^/]+)$/);
        if (m) {
          const list = state[m[1]],
            item = list.find((x) => x.id === m[2]);
          if (opts.method === 'DELETE') {
            list.splice(list.indexOf(item), 1);
            delete state.proxies[m[2]];
          } else {
            Object.assign(item, body);
            if (m[1] === 'profiles')
              Object.assign(state.proxies[item.id], body);
          }
          state.revision++;
          return response({ persisted: true, applied: true });
        }
        if (p === '/api/profiles') {
          const id = 'new-' + state.revision++,
            p = profile(id, body.type, body);
          state.profiles.push(p);
          state.proxies[id] = { ...p };
          return response({ id, persisted: true, applied: true }, 201);
        }
        if (p === '/api/relays') {
          const id = 'r-' + state.revision++;
          state.relays.push({
            id,
            configured: !!body.url,
            linked_profiles: 0,
            ...body,
            url: undefined,
          });
          return response({ id, persisted: true, applied: true }, 201);
        }
        return response({ queued: false });
      };
    },
  });
  await settle();
  assert.deepEqual(errors, [], 'production script errors');
  return {
    dom,
    w: dom.window,
    d: dom.window.document,
    state,
    calls,
    timers,
    setHandler: (fn) => (handler = fn),
    close: () => dom.window.close(),
  };
}
function edit(app, id, value) {
  const n = app.d.getElementById(id);
  assert.ok(n, 'missing ' + id);
  if (n.type === 'checkbox') n.checked = value;
  else n.value = value;
  n.dispatchEvent(new app.w.Event('input', { bubbles: true }));
  n.dispatchEvent(new app.w.Event('change', { bubbles: true }));
  n.blur();
  return n;
}
function submit(app, id) {
  app.d
    .getElementById(id)
    .dispatchEvent(
      new app.w.Event('submit', { bubbles: true, cancelable: true }),
    );
}
function english(d) {
  const b = d.body.cloneNode(true);
  b.querySelectorAll('script').forEach((x) => x.remove());
  assert.doesNotMatch(b.textContent, /[А-Яа-яЁё]/);
  for (const n of d.querySelectorAll('[aria-label],[placeholder],[title]'))
    for (const a of ['aria-label', 'placeholder', 'title'])
      assert.doesNotMatch(n.getAttribute(a) || '', /[А-Яа-яЁё]/);
}

test('Relay manual credentials use exact write-only API and honest persisted-unavailable status in EN/RU', async () => {
  for (const language of ['en', 'ru']) {
    let account = relayAccountState();
    const a = await mount({ language, state: withRelayProfile(), handler: (p, o, s) => {
      if (p !== '/api/profiles/p-relay/account') return;
      if (o.method === 'PUT') {
        account = relayAccountState({ credential_mode: 'manual', credential_configured: true });
        Object.assign(s.profiles.find(p => p.id === 'p-relay'), {
          credential_mode: 'manual', credential_configured: true });
        s.revision++;
        return response({ persisted: true, applied: false, runtime_available: false });
      }
      return response(account);
    }});
    try {
      a.w.confirm = () => true;
      const disclosure = a.d.getElementById('p-relay-account');
      assert.ok(disclosure, 'Relay row needs an account disclosure');
      disclosure.open = true;
      disclosure.dispatchEvent(new a.w.Event('toggle'));
      await settle();
      assert.equal(a.d.getElementById('p-a-account'), null);
      edit(a, 'p-relay-account-mode', 'manual');
      const values = { private_key: 'SYNTHETIC_PRIVATE_ONLY', peer_public_key: 'SYNTHETIC_SERVER_PEER_ONLY',
        tunnel_ipv4: '192.0.2.27/32', tunnel_ipv6: '2001:db8::27/128',
        reserved: 'AAAA', dns: '192.0.2.53,2001:db8::53', mtu: '1400' };
      for (const [key, value] of Object.entries(values)) edit(a, 'p-relay-account-' + key, value);
      for (const key of ['private_key', 'peer_public_key']) {
        const input = a.d.getElementById('p-relay-account-' + key);
        assert.equal(input.type, 'password');
        assert.equal(input.autocomplete, 'new-password');
      }
      assert.match(a.d.querySelector('label[for=p-relay-account-peer_public_key]').textContent,
        language === 'en' ? /server.*\[Peer\].*not.*client/i : /сервера.*\[Peer\].*не.*клиента/i);
      await a.w.refresh();
      assert.equal(a.d.getElementById('p-relay-account-private_key').value, values.private_key);
      submit(a, 'p-relay-account-form');
      await settle();
      const writes = a.calls.filter(c => c.method === 'PUT');
      assert.equal(writes.length, 1);
      assert.deepEqual(writes[0].body, { mode: 'manual', ...values, mtu: 1400 });
      assert.equal(writes[0].path, '/api/profiles/p-relay/account');
      assert.equal(writes[0].headers['Content-Type'], 'application/json');
      for (const key of Object.keys(values)) assert.equal(a.d.getElementById('p-relay-account-' + key).value, '');
      const text = a.d.getElementById('p-relay-account-status').textContent;
      assert.match(text, language === 'en' ? /Saved.*does not apply.*network and policy/i : /Сохранено.*не применяет.*сети и политики/i);
      assert.doesNotMatch(text, /SYNTHETIC|192\.0\.2|2001:db8/);
      assert.match(a.d.getElementById('p-relay-error').textContent, /Relay runtime unavailable/);
      assert.equal(a.calls.filter(c => /generate|register|activate/.test(c.path) && c.method !== 'GET').length, 0);
      assert.equal(a.w.localStorage.length, 0);
      assert.doesNotMatch(a.w.location.href, /SYNTHETIC/);
      if (language === 'en') english(a.d);
    } finally { a.close(); }
  }
});

test('Relay generation requires localized explicit confirmation, fresh revision, pending polling and safe cancellation', async () => {
  for (const language of ['en', 'ru']) {
    let account = relayAccountState(), queued = true;
    const prompts = [];
    const a = await mount({ language, state: withRelayProfile(), handler: (p, o, s) => {
      if (p === '/api/profiles' && o.method === 'GET')
        return response({ revision: s.revision, profiles: s.profiles, relays: s.relays, defaults: s.defaults });
      if (p === '/api/profiles/p-relay/account') return response(account);
      if (p === '/api/profiles/p-relay/account/generate') {
        if (o.method === 'POST') {
          account = relayAccountState({ state: 'pending', pending: true });
          return response({ queued, state: 'pending', runtime_available: false }, 202);
        }
        if (o.method === 'DELETE') {
          account = relayAccountState({ state: 'cancelled' });
          return response(account);
        }
      }
    }});
    try {
      const generate = a.d.getElementById('p-relay-account-generate');
      assert.ok(generate, 'explicit generation action missing');
      assert.equal(a.d.getElementById('p-relay-account-private_key').disabled, true);
      a.w.confirm = text => { prompts.push(text); return false; };
      generate.click(); await settle();
      assert.equal(a.calls.filter(c => c.method === 'POST' && c.path.endsWith('/generate')).length, 0);
      assert.match(prompts[0], language === 'en' ? /register.*NEW.*replace.*not.*activation/i : /НОВЫЙ.*замен.*не.*активация/i);
      a.state.revision = 17; // newer than the rendered status; fresh GET must win.
      a.w.confirm = () => true;
      generate.click(); generate.click(); await settle();
      let writes = a.calls.filter(c => c.method === 'POST' && c.path.endsWith('/generate'));
      assert.equal(writes.length, 1);
      assert.deepEqual(writes[0].body, { confirmed: true, revision: 17 });
      assert.equal(generate.disabled, true);
      assert.equal(a.d.getElementById('p-relay-account-cancel').disabled, false);
      assert.match(a.d.getElementById('p-relay-account-status').textContent, language === 'en' ? /Registration.*pending/i : /Регистрация.*выполняется/i);
      // Poll completion never resubmits or enables/applies a tunnel.
      account = relayAccountState({ state: 'succeeded', credential_mode: 'generated', credential_configured: true });
      a.state.revision++;
      for (const fn of a.timers) fn(); await settle();
      assert.equal(generate.disabled, false);
      assert.match(a.d.getElementById('p-relay-account-status').textContent, language === 'en' ? /saved.*does not apply.*network and policy/i : /сохран.*не применяет.*сети и политики/i);
      queued = false;
      generate.click(); await settle();
      writes = a.calls.filter(c => c.method === 'POST' && c.path.endsWith('/generate'));
      assert.equal(writes.length, 2);
      assert.deepEqual(writes[1].body, { confirmed: true, revision: 18 });
      assert.match(a.d.getElementById('p-relay-account-status').textContent, language === 'en' ? /already pending.*not queued/i : /уже выполняется.*не добавлен/i);
      a.d.getElementById('p-relay-account-cancel').click(); await settle();
      const cancel = a.calls.find(c => c.method === 'DELETE' && c.path.endsWith('/generate'));
      assert.deepEqual(cancel.body, {});
      assert.match(a.d.getElementById('p-relay-account-status').textContent, language === 'en' ? /Cancelled.*provider/i : /Отменено.*провайдер/i);
      assert.equal(a.calls.filter(c => /activate|register/.test(c.path) && c.method !== 'GET').length, 0);
      if (language === 'en') english(a.d);
    } finally { a.close(); }
  }
});

test('Relay account readback failure cannot masquerade as a successful save and newer drafts survive', async () => {
  let release;
  let failReadback = false;
  const a = await mount({ language: 'en', state: withRelayProfile(), handler: (p, o) => {
    if (p !== '/api/profiles/p-relay/account') return;
    if (o.method === 'PUT') return new Promise(r => { release = r; });
    return failReadback ? response({ error: 'SYNTHETIC_PRIVATE_OLD<img src=x onerror=alert(1)>' }, 500)
      : response(relayAccountState());
  }});
  try {
    a.w.confirm = () => true;
    edit(a, 'p-relay-account-mode', 'manual');
    edit(a, 'p-relay-account-private_key', 'SYNTHETIC_PRIVATE_OLD');
    edit(a, 'p-relay-account-peer_public_key', 'SYNTHETIC_PEER_OLD');
    edit(a, 'p-relay-account-tunnel_ipv4', '192.0.2.1');
    submit(a, 'p-relay-account-form'); await settle();
    assert.ok(release);
    edit(a, 'p-relay-account-private_key', 'SYNTHETIC_PRIVATE_NEW');
    edit(a, 'p-relay-account-dns', '192.0.2.53');
    failReadback = true;
    release(response({ persisted: true, applied: false, runtime_available: false }));
    await settle();
    assert.equal(a.d.getElementById('p-relay-account-private_key').value, 'SYNTHETIC_PRIVATE_NEW');
    assert.equal(a.d.getElementById('p-relay-account-peer_public_key').value, '');
    assert.equal(a.d.getElementById('p-relay-account-dns').value, '192.0.2.53');
    assert.ok(a.calls.some(c => c.path.endsWith('/account') && c.method === 'GET'), 'must verify saved account endpoint');
    assert.match(a.d.getElementById('p-relay-account-status').textContent, /Saved.*status.*unavailable/i);
    assert.doesNotMatch(a.d.getElementById('p-relay-account-status').textContent, /SYNTHETIC|<img/);
    assert.equal(a.d.querySelector('#p-relay-account-status img'), null);
  } finally { a.close(); }
});

test('Relay manual replacement still confirms when cached account status predates configured profile metadata', async () => {
  const a = await mount({ language: 'en', state: withRelayProfile(), handler: (p, o) => {
    if (p.endsWith('/account') && o.method === 'GET') return response(relayAccountState());
  }});
  try {
    await a.w.pollAccount('p-relay');
    Object.assign(a.state.profiles.find(p => p.id === 'p-relay'), { credential_mode: 'generated', credential_configured: true });
    await a.w.refresh();
    edit(a, 'p-relay-account-mode', 'manual');
    edit(a, 'p-relay-account-private_key', 'SYNTHETIC_REPLACE_PRIVATE');
    edit(a, 'p-relay-account-peer_public_key', 'SYNTHETIC_REPLACE_PEER');
    edit(a, 'p-relay-account-tunnel_ipv4', '192.0.2.2');
    let prompt = '';
    a.w.confirm = text => { prompt = text; return false; };
    submit(a, 'p-relay-account-form'); await settle();
    assert.match(prompt, /Replace.*keys.*shared account.*activation/i);
    assert.equal(a.calls.filter(c => c.method === 'PUT').length, 0);
    assert.equal(a.d.getElementById('p-relay-account-private_key').value, 'SYNTHETIC_REPLACE_PRIVATE');
  } finally { a.close(); }
});

test('Relay secret drafts edited away and back during save retain their newer edit version', async () => {
  let release;
  const a = await mount({ language: 'en', state: withRelayProfile(), handler: (p, o) => {
    if (!p.endsWith('/account')) return;
    if (o.method === 'PUT') return new Promise(r => { release = r; });
    return response(relayAccountState({ credential_mode: 'manual', credential_configured: true }));
  }});
  try {
    a.w.confirm = () => true;
    edit(a, 'p-relay-account-mode', 'manual');
    edit(a, 'p-relay-account-private_key', 'SYNTHETIC_DRAFT_SAME');
    edit(a, 'p-relay-account-peer_public_key', 'SYNTHETIC_PEER');
    edit(a, 'p-relay-account-tunnel_ipv4', '192.0.2.2');
    submit(a, 'p-relay-account-form'); await settle();
    edit(a, 'p-relay-account-private_key', 'SYNTHETIC_DRAFT_OTHER');
    edit(a, 'p-relay-account-private_key', 'SYNTHETIC_DRAFT_SAME');
    release(response({ persisted: true, applied: false, runtime_available: false })); await settle();
    assert.equal(a.d.getElementById('p-relay-account-private_key').value, 'SYNTHETIC_DRAFT_SAME');
    assert.equal(a.d.getElementById('p-relay-account-peer_public_key').value, '');
  } finally { a.close(); }
});

test('Relay manual save always confirms replacement intent even when cached metadata is unconfigured', async () => {
  const a = await mount({ language: 'en', state: withRelayProfile() });
  try {
    edit(a, 'p-relay-account-mode', 'manual');
    edit(a, 'p-relay-account-private_key', 'SYNTHETIC_CONFIRM_PRIVATE');
    edit(a, 'p-relay-account-peer_public_key', 'SYNTHETIC_CONFIRM_PEER');
    edit(a, 'p-relay-account-tunnel_ipv4', '192.0.2.3');
    let prompt = '';
    a.w.confirm = text => { prompt = text; return false; };
    submit(a, 'p-relay-account-form'); await settle();
    assert.match(prompt, /Replace.*keys.*shared account.*activation/i);
    assert.equal(a.calls.filter(c => c.method === 'PUT').length, 0);
  } finally { a.close(); }
});

for (const language of ['en', 'ru']) {
  test(language + ' Relay account rejection matrix keeps secrets/drafts and action errors through polls without XSS', async () => {
    for (const code of [400, 401, 403, 404, 409, 500, 503]) {
      const a = await mount({ language, state: withRelayProfile(), url: 'http://console.test/?secret=SYNTHETIC_API_TOKEN', handler: (p, o) => {
        if (p.endsWith('/account')) return o.method === 'PUT'
          ? response({ error: 'SYNTHETIC_PRIVATE<img src=x onerror=alert(1)>', persisted: false, applied: false }, code)
          : response(relayAccountState());
      }});
      try {
        a.w.confirm = () => true;
        edit(a, 'p-relay-account-mode', 'manual');
        edit(a, 'p-relay-account-private_key', 'SYNTHETIC_PRIVATE');
        edit(a, 'p-relay-account-peer_public_key', 'SYNTHETIC_PEER');
        edit(a, 'p-relay-account-tunnel_ipv4', '192.0.2.4');
        submit(a, 'p-relay-account-form'); await settle();
        const sent = a.calls.find(c => c.method === 'PUT');
        assert.deepEqual(sent.body, { mode: 'manual', private_key: 'SYNTHETIC_PRIVATE', peer_public_key: 'SYNTHETIC_PEER', tunnel_ipv4: '192.0.2.4' });
        assert.equal(sent.headers.Authorization, 'Bearer SYNTHETIC_API_TOKEN');
        const text = a.d.getElementById('p-relay-account-status').textContent;
        assert.ok(text);
        if (code === 401) assert.match(text, language === 'en' ? /token/i : /токен/i);
        else if (code === 403) assert.match(text, /Origin/);
        else assert.match(text, language === 'en' ? /Could not update/ : /Не удалось/);
        await a.w.pollAccount('p-relay'); await a.w.refresh();
        assert.equal(a.d.getElementById('p-relay-account-status').textContent, text, 'successful poll must not erase failed action');
        assert.equal(a.d.getElementById('p-relay-account-private_key').value, 'SYNTHETIC_PRIVATE');
        assert.equal(a.d.getElementById('p-relay-account-peer_public_key').value, 'SYNTHETIC_PEER');
        assert.equal(a.d.querySelector('#p-relay-account-status img'), null);
        assert.doesNotMatch(a.d.body.textContent, /SYNTHETIC_PRIVATE|SYNTHETIC_PEER/);
        assert.doesNotMatch(a.w.location.href, /secret=|SYNTHETIC/);
        assert.equal(a.w.localStorage.length, 0);
        assert.equal(a.calls.filter(c => /generate|register|activate/.test(c.path) && c.method !== 'GET').length, 0);
        if (language === 'en') english(a.d);
      } finally { a.close(); }
    }
  });
  test(language + ' Relay account states are localized safe DTOs; old/malformed metadata cannot invent readiness or fields', async () => {
    let dto = relayAccountState();
    const s = withRelayProfile();
    delete s.profiles.at(-1).credential_mode;
    delete s.profiles.at(-1).credential_configured;
    const a = await mount({ language, state: s, handler: (p) => {
      if (p.endsWith('/account')) return response(dto);
    }});
    try {
      assert.match(a.d.getElementById('p-relay-account-status').textContent, language === 'en' ? /not configured/ : /не настроены/);
      for (const state of ['idle', 'pending', 'succeeded', 'error', 'cancelled', 'stale']) {
        dto = relayAccountState({ state, pending: state === 'pending',
          credential_mode: 'generated', credential_configured: true,
          private_key: 'SYNTHETIC_RETURNED_PRIVATE', peer_public_key: 'SYNTHETIC_RETURNED_PEER',
          tunnel_ipv4: '192.0.2.7', dns: '192.0.2.53', error: state === 'error' ? 'Relay account generation failed' : undefined });
        await a.w.pollAccount('p-relay');
        const text = a.d.getElementById('p-relay-account-status').textContent;
        assert.match(text, language === 'en' ? /network and policy status/ : /статусах сети и политики/);
        if (state === 'error') assert.match(text, language === 'en' ? /Registration failed/ : /Регистрация не удалась/);
        if (state === 'stale') assert.match(text, language === 'en' ? /discarded/ : /отброшен/);
        for (const key of ['private_key', 'peer_public_key', 'tunnel_ipv4', 'dns'])
          assert.equal(a.d.getElementById('p-relay-account-' + key).value, '');
        assert.doesNotMatch(text, /SYNTHETIC|192\.0\.2/);
      }
      dto = { state: '<img src=x>', pending: true, credential_mode: 'manual', credential_configured: true, runtime_available: true };
      await a.w.pollAccount('p-relay');
      assert.match(a.d.getElementById('p-relay-account-status').textContent, language === 'en' ? /Could not/ : /Не удалось/);
      assert.equal(a.d.querySelector('#p-relay-account-status img'), null);
      if (language === 'en') english(a.d);
    } finally { a.close(); }
  });
}

test('Relay cancellation supersedes a stale pending poll and does not delete credentials or auto-retry', async () => {
  let release;
  const a = await mount({ language: 'en', state: withRelayProfile(), handler: (p, o) => {
    if (p.endsWith('/account')) return response(relayAccountState({ state: 'pending', pending: true, credential_mode: 'manual', credential_configured: true }));
    if (p.endsWith('/account/generate') && o.method === 'DELETE') return response(relayAccountState({ state: 'cancelled', credential_mode: 'manual', credential_configured: true }));
  }});
  try {
    await a.w.pollAccount('p-relay');
    a.setHandler((p, o) => {
      if (p.endsWith('/account')) return new Promise(r => { release = r; });
      if (p.endsWith('/account/generate') && o.method === 'DELETE') return response(relayAccountState({ state: 'cancelled', credential_mode: 'manual', credential_configured: true }));
    });
    const old = a.w.pollAccount('p-relay'); await settle();
    a.d.getElementById('p-relay-account-cancel').click(); await settle();
    release(response(relayAccountState({ state: 'pending', pending: true }))); await old;
    assert.match(a.d.getElementById('p-relay-account-status').textContent, /Cancelled.*Existing credentials were not deleted/);
    assert.equal(a.d.getElementById('p-relay-account-generate').disabled, false);
    assert.equal(a.calls.filter(c => c.method === 'POST').length, 0);
  } finally { a.close(); }
});

test('Relay generation conflicts/auth/origin/unavailable responses never queue retries and invalid revision never posts', async () => {
  for (const code of [400, 401, 403, 404, 409, 503]) {
    const a = await mount({ language: 'en', state: withRelayProfile(), handler: (p, o, s) => {
      if (p === '/api/profiles' && o.method === 'GET') return response({ revision: s.revision, profiles: s.profiles });
      if (p.endsWith('/generate')) return response({ error: '<img src=x> SYNTHETIC_GENERATION_ERROR' }, code);
    }});
    try {
      a.w.confirm = () => true;
      a.d.getElementById('p-relay-account-generate').click(); await settle();
      for (const fn of a.timers) fn(); await settle();
      assert.equal(a.calls.filter(c => c.path.endsWith('/generate') && c.method === 'POST').length, 1);
      const text = a.d.getElementById('p-relay-account-status').textContent;
      assert.doesNotMatch(text, /SYNTHETIC|<img|saved|queued/i);
      assert.ok(text);
      assert.equal(a.d.querySelector('#p-relay-account-status img'), null);
    } finally { a.close(); }
  }
  const a = await mount({ language: 'en', state: withRelayProfile(), handler: (p, o) => {
    if (p === '/api/profiles' && o.method === 'GET') return response({ revision: 0, profiles: [] });
  }});
  try {
    a.w.confirm = () => true;
    a.d.getElementById('p-relay-account-generate').click(); await settle();
    assert.equal(a.calls.filter(c => c.method === 'POST').length, 0);
  } finally { a.close(); }
});

if (process.env.WARPD_ACCOUNT_DTO_FIXTURE) {
  test('production backend serialized account/profile DTO fixture runs through the complete production DOM', async () => {
    const fixture = JSON.parse(fs.readFileSync(process.env.WARPD_ACCOUNT_DTO_FIXTURE, 'utf8'));
    assert.deepEqual(fixture.account_idle, relayAccountState());
    assert.deepEqual(fixture.manual_success, { persisted: true, applied: false, runtime_available: false });
    for (const listing of [fixture.profiles_unconfigured, fixture.profiles_manual]) {
      const s = { ...status(), ...listing };
      s.proxies = Object.fromEntries(s.profiles.map(p => [p.id, {
        ...p, running: false, ready: false, transport_ready: false,
        policy_ready: false, error: p.type === 'relay-warp' ? 'Relay runtime unavailable' : '',
      }]));
      const relay = s.profiles.find(p => p.type === 'relay-warp');
      for (const p of s.profiles) {
        if (p.type === 'relay-warp') {
          assert.equal(typeof p.credential_configured, 'boolean');
          assert.equal(typeof p.credential_mode, 'string');
        } else {
          assert.equal('credential_mode' in p, false);
          assert.equal('credential_configured' in p, false);
        }
      }
      let dto = fixture.account_idle;
      const a = await mount({ language: 'en', state: s, handler: p => p.endsWith('/account') ? response(dto) : undefined });
      try {
        for (const state of Object.keys(fixture.states)) {
          dto = fixture.states[state];
          await a.w.pollAccount(relay.id);
          assert.equal(a.d.getElementById(relay.id + '-account-private_key').value, '');
          assert.match(a.d.getElementById(relay.id + '-account-status').textContent, /Tunnel and exit readiness: see network and policy status/);
        }
        english(a.d);
      } finally { a.close(); }
    }
  });
}

test('dynamic stable-ID profiles include multiple WARP instances and preserve intentional empty list', async () => {
  const a = await mount();
  try {
    assert.equal(a.d.querySelectorAll('#services [role=switch]').length, 3);
    assert.ok(a.d.getElementById('p-a-toggle'));
    assert.ok(a.d.getElementById('p-b-toggle'));
    assert.equal(a.d.getElementById('warp-toggle'), null);
    a.state.profiles = [];
    a.state.proxies = {};
    await a.w.refresh();
    assert.equal(a.d.querySelectorAll('#services [role=switch]').length, 0);
    assert.equal(a.d.getElementById('empty-profiles').hidden, false);
  } finally {
    a.close();
  }
});

test('Relay monitor-only resources keep URL write-only, gate activation and pause children without changing saved choices', async () => {
  const a = await mount();
  try {
    assert.equal(
      a.d
        .getElementById('new-profile-type')
        .querySelector('option[value="relay-warp"]').disabled,
      true,
    );
    edit(a, 'new-relay-name', 'Monitor');
    edit(a, 'new-relay-url', 'https://de.tribukvy.ltd/test-only-credential');
    submit(a, 'new-relay-form');
    await settle();
    const post = a.calls.find(
      (c) => c.path === '/api/relays' && c.method === 'POST',
    );
    assert.ok(post);
    assert.equal(post.body.enabled, false);
    assert.equal(post.body.path_confirmed, false);
    assert.equal(a.state.profiles.length, 3);
    const r = a.state.relays[0];
    assert.equal(a.d.getElementById('new-relay-url').value, '');
    assert.equal(a.d.getElementById(r.id + '-url').value, '');
    assert.ok(!a.d.body.textContent.includes('test-only-credential'));
    assert.equal(a.d.getElementById(r.id + '-activate').disabled, true);
    edit(a, r.id + '-name', 'Renamed');
    submit(a, r.id + '-relay-form');
    await settle();
    const patch = a.calls
      .filter((c) => c.path === '/api/relays/' + r.id)
      .at(-1);
    assert.deepEqual(patch.body, { name: 'Renamed' });
    r.enabled = true;
    const child = profile('child', 'relay-warp', {
      port: 1090,
      relay_id: r.id,
      relay_country: 'DE',
    });
    a.state.profiles.push(child);
    a.state.proxies.child = { ...child };
    await a.w.refresh();
    a.d.getElementById(r.id + '-toggle').click();
    await settle();
    assert.equal(r.enabled, false);
    assert.equal(child.enabled, true);
    assert.equal(
      a.d.getElementById('child-toggle').getAttribute('aria-checked'),
      'true',
    );
    assert.equal(a.d.getElementById('child-toggle').disabled, true);
    assert.match(a.d.getElementById('child-transport').textContent, /Relay/);
    a.d.getElementById(r.id + '-toggle').click();
    await settle();
    assert.equal(child.enabled, true);
    assert.equal(a.d.getElementById('child-toggle').disabled, false);
    a.w.confirm = () => true;
    a.setHandler(async (p) =>
      p === '/api/relays/' + r.id
        ? response({ error: 'referenced' }, 409)
        : null,
    );
    a.d.getElementById(r.id + '-delete').click();
    await settle();
    assert.match(a.d.getElementById('message').textContent, /referenced/);
    assert.ok(a.state.relays.length);
    a.setHandler(null);
    edit(a, r.id + '-path_confirmed', true);
    submit(a, r.id + '-relay-form');
    await settle();
    assert.equal(a.d.getElementById(r.id + '-activate').disabled, false);
    const prompts = [];
    a.w.confirm = (t) => {
      prompts.push(t);
      return false;
    };
    a.d.getElementById(r.id + '-activate').click();
    await settle();
    assert.equal(a.calls.filter((c) => c.path.endsWith('/activate')).length, 0);
    assert.match(prompts[0], /IP|путь/);
    a.w.confirm = () => true;
    a.d.getElementById(r.id + '-activate').click();
    await settle();
    assert.equal(a.calls.filter((c) => c.path.endsWith('/activate')).length, 1);
    assert.match(
      a.d.getElementById(r.id + '-state').textContent,
      /не подтвержд/,
    );
  } finally {
    a.close();
  }
});

for (const language of ['ru', 'en'])
  test(
    language +
      ' mutations use exact profile contract, preserve drafts, and show persisted-but-unapplied truth',
    async () => {
      const a = await mount({ language });
      try {
        a.d.getElementById('p-a-toggle').click();
        await settle();
        assert.deepEqual(
          a.calls.find((c) => c.path === '/api/profiles/p-a').body,
          { enabled: false },
        );
        assert.equal(
          a.d.getElementById('p-a-toggle').getAttribute('aria-checked'),
          'false',
        );
        const sibling = edit(a, 'p-b-epin', '192.0.2.77:443');
        edit(a, 'p-a-epin', '192.0.2.88:443');
        a.d.getElementById('p-a-apply').click();
        await settle();
        assert.deepEqual(
          a.calls.filter((c) => c.path === '/api/profiles/p-a').at(-1).body,
          { endpoint: '192.0.2.88:443' },
        );
        assert.equal(sibling.value, '192.0.2.77:443');
        a.d.getElementById('p-a-clearbtn').click();
        await settle();
        assert.deepEqual(
          a.calls.filter((c) => c.path === '/api/profiles/p-a').at(-1).body,
          { endpoint: '' },
        );
        a.setHandler(async (p, o, s) => {
          if (p === '/api/profiles/p-a') {
            const b = JSON.parse(o.body);
            Object.assign(s.profiles[0], b);
            Object.assign(s.proxies['p-a'], b, { transport_ready: false });
            return response(
              { persisted: true, applied: false, error: 'engine unavailable' },
              503,
            );
          }
        });
        a.d.getElementById('p-a-toggle').click();
        await settle();
        assert.equal(
          a.d.getElementById('p-a-toggle').getAttribute('aria-checked'),
          'true',
        );
        assert.match(
          a.d.getElementById('p-a-feedback').textContent,
          /engine unavailable/,
        );
        assert.match(
          a.d.getElementById('message').textContent,
          language === 'en' ? /Saved, but/ : /Сохранено, но/,
        );
        assert.notEqual(a.d.getElementById('p-a-policy').className, 'ok');
        edit(a, 'p-b-name', 'keep me');
        a.setHandler(async (p) => {
          if (p === '/api/status') throw new Error('offline');
        });
        await a.w.refresh();
        assert.equal(a.d.getElementById('p-b-name').value, 'keep me');
        assert.equal(a.d.getElementById('p-b-toggle').disabled, true);
        assert.notEqual(a.d.getElementById('p-b-transport').className, 'ok');
      } finally {
        a.close();
      }
    },
  );

test('effective exclusions show union sources; policy separates HTTP-colo, public IP and independent legacy/Relay proof', async () => {
  const s = status();
  s.profiles[0].exclude_countries = 'BY';
  s.profiles[0].exclude_nodes = 'FRA';
  s.profiles[0].effective_exclude_nodes = 'DME,SVO,FRA';
  s.profiles[0].effective_exclude_countries = 'RU,BY';
  s.profiles.push(profile('legacy', 'legacy-m2w', { port: 1088 }));
  s.proxies.legacy = {
    ...s.proxies['p-b'],
    foreign_ip_verified: false,
    geo_country: 'DE',
    geo_check: new Date().toISOString(),
    geo_error: 'independent verification failed',
  };
  const a = await mount({ state: s });
  try {
    assert.match(
      a.d.getElementById('p-a-effective-countries').textContent,
      /RU.*глобальн.*BY.*локальн/,
    );
    assert.match(
      a.d.getElementById('p-a-effective-nodes').textContent,
      /FRA.*локальн/,
    );
    assert.notEqual(a.d.getElementById('p-a-policy').className, 'ok');
    assert.equal(a.d.getElementById('p-b-policy').className, 'ok');
    assert.equal(a.d.getElementById('p-b-ip-country').textContent, 'RU');
    assert.notEqual(a.d.getElementById('legacy-policy').className, 'ok');
    assert.match(a.d.getElementById('legacy-geo').textContent, /DE/);
    assert.match(
      a.d.getElementById('legacy-geo-error').textContent,
      /independent verification failed/,
    );
    assert.match(a.d.getElementById('legacy-details').textContent, /миграция/);
    assert.equal(
      a.d
        .getElementById('new-profile-type')
        .querySelector('option[value="legacy-m2w"]'),
      null,
    );
    for (const country of ['RU', '']) {
      s.proxies['p-b'].exit_node_country = country;
      await a.w.refresh();
      assert.notEqual(a.d.getElementById('p-b-policy').className, 'ok');
    }
  } finally {
    a.close();
  }
});

for (const language of ['ru', 'en'])
  test(
    language +
      ' first paint, pending widgets, confirmations, ARIA and dates use production localization',
    async () => {
      const page = new JSDOM(rendered(language));
      try {
        assert.equal(page.window.document.documentElement.lang, language);
        assert.equal(
          page.window.document.title,
          language === 'en'
            ? 'warpd — proxy management'
            : 'warpd — управление прокси',
        );
        if (language === 'en') english(page.window.document);
      } finally {
        page.window.close();
      }
      let release;
      const gate = new Promise((r) => (release = r)),
        a = await mount({
          language,
          handler: async (p) => (p === '/api/status' ? gate : null),
        });
      try {
        assert.equal(a.d.getElementById('restart-btn').disabled, true);
        assert.equal(a.d.querySelectorAll('#services [role=switch]').length, 0);
        assert.equal(
          a.d.getElementById('connection').textContent,
          language === 'en' ? 'Connecting…' : 'Подключение…',
        );
        if (language === 'en') english(a.d);
        release(response(a.state));
        await settle();
        if (language === 'en') english(a.d);
        const iso = '2026-10-18T08:09:00Z';
        assert.equal(
          a.w.fmtTime(iso),
          new a.w.Date(iso).toLocaleString(
            language === 'en' ? 'en-US' : 'ru-RU',
          ),
        );
        assert.match(
          a.d.getElementById('p-a-toggle').getAttribute('aria-label'),
          language === 'en' ? /enable or disable/ : /включить или выключить/,
        );
        const prompts = [];
        a.w.confirm = (t) => {
          prompts.push(t);
          return false;
        };
        for (const id of [
          'restart-btn',
          'rotate-btn',
          'newaccount-btn',
          'p-a-delete',
        ])
          a.d.getElementById(id).click();
        await settle();
        assert.equal(prompts.length, 4);
        assert.equal(a.calls.filter((c) => c.method !== 'GET').length, 0);
        assert.match(
          prompts[0],
          language === 'en' ? /all.*profiles/ : /всех.*профил/,
        );
        if (language === 'en')
          for (const p of prompts) assert.doesNotMatch(p, /[А-Яа-яЁё]/);
        const cfg = edit(
          a,
          'cfg-web_language',
          language === 'en' ? 'ru' : 'en',
        );
        submit(a, 'config-form');
        await settle();
        assert.equal(a.state.config.web_language, cfg.value);
        assert.match(
          a.d.getElementById('message').textContent,
          language === 'en' ? /reload/i : /обновите/i,
        );
      } finally {
        release(response(a.state));
        a.close();
      }
    },
  );

test('poll races, pending double clicks, stale status and incomplete responses never invent readiness', async () => {
  const a = await mount();
  let release;
  try {
    const before = clone(a.state),
      gate = new Promise((r) => (release = r));
    let once = true;
    a.setHandler(async (p) => {
      if (p === '/api/status' && once) {
        once = false;
        return gate;
      }
    });
    const old = a.w.refresh();
    await settle();
    a.d.getElementById('p-a-toggle').click();
    await settle();
    assert.equal(
      a.d.getElementById('p-a-toggle').getAttribute('aria-checked'),
      'false',
    );
    release(response(before));
    await old;
    assert.equal(
      a.d.getElementById('p-a-toggle').getAttribute('aria-checked'),
      'false',
    );
    const applyGate = new Promise((r) => (release = r));
    a.setHandler(async (p) => (p === '/api/profiles/p-b' ? applyGate : null));
    const sw = a.d.getElementById('p-b-toggle');
    sw.click();
    await settle();
    sw.click();
    await a.w.refresh();
    assert.equal(sw.getAttribute('aria-checked'), 'true');
    assert.equal(sw.getAttribute('aria-busy'), 'true');
    assert.equal(sw.disabled, true);
    release(response({ persisted: false, applied: false }));
    await settle();
    assert.match(
      a.d.getElementById('p-b-feedback').textContent,
      /не подтверждено/,
    );
    assert.notEqual(a.d.getElementById('p-b-policy').className, 'ok');
    a.setHandler(async (p) =>
      p === '/api/status' ? response({ profiles: [], proxies: {} }) : null,
    );
    await a.w.refresh();
    assert.equal(sw.disabled, true);
    assert.notEqual(a.d.getElementById('p-b-policy').className, 'ok');
    a.setHandler(null);
    await a.w.refresh();
    const now = a.w.Date.now;
    a.w.Date.now = () => now() + 16000;
    a.timers.at(-1)();
    assert.equal(sw.disabled, true);
    assert.notEqual(a.d.getElementById('p-a-transport').className, 'ok');
    a.w.Date.now = now;
  } finally {
    a.close();
  }
});

test('inline bearer recovery preserves dirty drafts; CSRF refusals explain origin rather than prompting for a token', async () => {
  const token = String(Math.random()),
    a = await mount({
      language: 'en',
      url: 'http://console.test/?secret=' + token + '&keep=1',
    });
  let release;
  try {
    assert.equal(a.w.location.search, '?keep=1');
    assert.equal(a.calls[0].headers.Authorization, 'Bearer ' + token);
    assert.equal(a.w.localStorage.length, 0);
    assert.equal(a.w.sessionStorage.length, 0);
    const field = edit(a, 'p-a-name', 'draft');
    a.setHandler(async (p) =>
      p === '/api/status'
        ? {
            ok: false,
            status: 401,
            json: async () => {
              throw Error('not json');
            },
          }
        : null,
    );
    await a.w.refresh();
    assert.equal(a.d.getElementById('auth').hidden, false);
    assert.equal(a.d.getElementById('p-a-name'), field);
    assert.equal(field.value, 'draft');
    const gate = new Promise((r) => (release = r));
    let once = true;
    a.setHandler(async (p) => {
      if (p === '/api/status' && once) {
        once = false;
        return gate;
      }
    });
    const old = a.w.refresh();
    await settle();
    a.d.getElementById('token').value = 'new-' + token;
    submit(a, 'auth-form');
    await settle();
    release(response({ error: 'unauthorized' }, 401));
    await old;
    assert.equal(a.d.getElementById('auth').hidden, true);
    assert.equal(a.d.getElementById('token').value, '');
    assert.equal(a.d.getElementById('p-a-toggle').disabled, false);
    a.setHandler(async (p) =>
      p === '/api/profiles/p-a'
        ? response({ error: 'cross-site request rejected' }, 403)
        : null,
    );
    a.d.getElementById('p-a-toggle').click();
    await settle();
    assert.equal(a.d.getElementById('auth').hidden, true);
    assert.match(
      a.d.getElementById('message').textContent,
      /origin|router address/i,
    );
    assert.equal(field.value, 'draft');
    english(a.d);
  } finally {
    a.close();
  }
});

test('draft typed during an in-flight save survives its readback; lower server revisions are ignored', async () => {
  const a = await mount();
  let release;
  try {
    edit(a, 'p-a-name', 'submitted');
    const gate = new Promise((r) => (release = r));
    a.setHandler(async (p) => (p === '/api/profiles/p-a' ? gate : null));
    submit(a, 'p-a-profile-form');
    await settle();
    edit(a, 'p-a-name', 'newer draft');
    a.state.profiles[0].name = 'submitted';
    a.state.revision = 5;
    release(response({ persisted: true, applied: true }));
    await settle();
    assert.equal(a.d.getElementById('p-a-name').value, 'newer draft');
    assert.equal(a.d.getElementById('p-a-name').dataset.dirty, 'true');
    const older = clone(a.state);
    older.revision = 4;
    older.profiles = [];
    older.proxies = {};
    a.setHandler(async (p) => (p === '/api/status' ? response(older) : null));
    await a.w.refresh();
    assert.ok(a.d.getElementById('p-a-toggle'));
    assert.equal(a.d.getElementById('p-a-name').value, 'newer draft');
  } finally {
    a.close();
  }
});

for (const language of ['ru', 'en'])
  test(
    language +
      ' Relay-WARP wizard and resource diagnostics stay separate, localized and safely unverified',
    async () => {
      const s = status();
      s.relays = [
        {
          id: 'resource',
          name: 'Resource',
          enabled: true,
          configured: true,
          source_ip: '',
          interface: '',
          path_confirmed: true,
          monitor_interval: 300,
          linked_profiles: 0,
          status: {
            state: 'unverified',
            observed_ip: '198.51.100.4',
            activation_outcome: 'unverified',
            binding_verified: false,
            tunnel_verified: false,
            error: 'raw monitor error',
            last_observation_at: new Date().toISOString(),
          },
        },
      ];
      const a = await mount({ language, state: s });
      try {
        assert.ok(
          a.d.getElementById('resource-toggle').querySelector('.track'),
        );
        assert.equal(
          a.d.getElementById('resource-observed').textContent,
          '198.51.100.4',
        );
        assert.match(
          a.d.getElementById('resource-activation').textContent,
          /^unverified · unverified$/,
        );
        assert.match(
          a.d.getElementById('resource-monitor-error').textContent,
          /raw monitor error/,
        );
        edit(a, 'new-profile-type', 'relay-warp');
        edit(a, 'new-profile-name', 'Relay output');
        edit(a, 'new-profile-port', '1099');
        edit(a, 'new-profile-relay_id', 'resource');
        edit(a, 'new-profile-relay_country', 'FI');
        submit(a, 'new-profile-form');
        await settle();
        const c = a.calls
          .filter((c) => c.path === '/api/profiles' && c.method === 'POST')
          .at(-1);
        assert.equal(c.body.relay_id, 'resource');
        assert.equal(c.body.relay_country, 'FI');
        assert.equal(c.body.type, 'relay-warp');
        const p = a.state.profiles.at(-1);
        Object.assign(a.state.proxies[p.id], {
          transport_ready: true,
          policy_ready: true,
          ready: true,
          last_check: new Date().toISOString(),
          exit_node: 'FRA',
          exit_node_country: 'DE',
          exit_country: 'DE',
          foreign_ip_verified: false,
        });
        await a.w.refresh();
        assert.notEqual(a.d.getElementById(p.id + '-policy').className, 'ok');
        assert.match(
          a.d.getElementById(p.id + '-policy-error').textContent,
          language === 'en'
            ? /Foreign IP not verified/
            : /Иностранный IP не подтверждён/,
        );
        assert.match(
          a.d.getElementById(p.id + '-type-label').textContent,
          /Relay-WARP/,
        );
        const prompts = [];
        a.w.confirm = (t) => {
          prompts.push(t);
          return false;
        };
        a.d.getElementById('resource-activate').click();
        await settle();
        assert.match(prompts[0], /HTTPS\/UDP/);
        if (language === 'en') {
          assert.doesNotMatch(prompts[0], /[А-Яа-яЁё]/);
          english(a.d);
        }
        for (const n of a.d.querySelectorAll('[role=switch],summary'))
          assert.ok(
            parseInt(a.w.getComputedStyle(n).minHeight) >= 44,
            'declared hit area ' + n.id,
          );
      } finally {
        a.close();
      }
    },
  );

for (const language of ['ru', 'en'])
  test(
    language +
      ' queued scans, progress, rejected requests and shared maintenance report async truth',
    async () => {
      const a = await mount({ language });
      try {
        a.setHandler(async (p) =>
          p === '/api/scan' ? response({ queued: true }) : null,
        );
        a.d.getElementById('p-a-scan').click();
        await settle();
        assert.deepEqual(
          a.calls.filter((c) => c.path === '/api/scan').at(-1).body,
          { for: 'p-a' },
        );
        assert.equal(a.d.getElementById('scan-spinner').hidden, false);
        assert.match(
          a.d.getElementById('scan-state').textContent,
          language === 'en' ? /Queued/ : /очередь/,
        );
        assert.equal(a.d.getElementById('p-b-toggle').disabled, true);
        const now = a.w.Date.now;
        a.w.Date.now = () => now() + 21000;
        a.w.renderAll();
        assert.match(
          a.d.getElementById('message').textContent,
          language === 'en'
            ? /not confirmed within 20 seconds/
            : /не подтверждён за 20 секунд/,
        );
        a.w.Date.now = now;
        a.state.scan = {
          scanning: true,
          for: 'p-a',
          started: new Date(Date.now() - 65000).toISOString(),
          log: ['raw scanner line'],
          last_result: 'raw result',
        };
        await a.w.refresh();
        assert.equal(a.d.getElementById('scan-details').open, true);
        assert.match(
          a.d.getElementById('scan-state').textContent,
          language === 'en' ? /1 min/ : /1 мин/,
        );
        assert.equal(
          a.d.getElementById('scan-log').textContent,
          'raw scanner line',
        );
        assert.equal(a.d.getElementById('restart-btn').disabled, true);
        a.state.scan.scanning = false;
        await a.w.refresh();
        a.setHandler(async (p) =>
          p === '/api/scan' ? response({ queued: false }) : null,
        );
        a.d.getElementById('scan-btn').click();
        await settle();
        assert.match(
          a.d.getElementById('message').textContent,
          language === 'en' ? /not accepted/ : /не принят/,
        );
        a.w.confirm = () => true;
        for (const id of ['restart-btn', 'rotate-btn', 'newaccount-btn']) {
          a.d.getElementById(id).click();
          await settle();
          assert.match(
            a.d.getElementById('message').textContent,
            language === 'en' ? /request accepted/ : /Запрос .*принят/,
          );
          assert.doesNotMatch(
            a.d.getElementById('message').textContent,
            language === 'en'
              ? /successfully|account created/
              : /успешно|аккаунт создан/,
          );
        }
        assert.deepEqual(a.calls.find((c) => c.path === '/api/restart').body, {
          proxy: 'both',
        });
        assert.deepEqual(
          a.calls.filter((c) => c.path === '/api/register').map((c) => c.body),
          [{ fresh: false }, { fresh: true }],
        );
        if (language === 'en') english(a.d);
      } finally {
        a.close();
      }
    },
  );

test('auto profile displays real runtime endpoint, not empty pin; server strings stay inert and copy uses actual IPv6 bind', async () => {
  const a = await mount();
  let copied;
  try {
    a.state.profiles[0].endpoint = '';
    a.state.proxies['p-a'].endpoint = '192.0.2.222:2408';
    a.state.profiles[0].listen = '::1';
    a.state.proxies['p-a'].exit_info = '<img src=x onerror=alert(1)>';
    await a.w.refresh();
    assert.equal(a.d.getElementById('p-a-ep').textContent, '192.0.2.222:2408');
    assert.equal(a.d.getElementById('p-a-exit').querySelector('img'), null);
    Object.defineProperty(a.w.navigator, 'clipboard', {
      value: { writeText: async (v) => (copied = v) },
    });
    a.d.getElementById('p-a-copy').click();
    await settle();
    assert.equal(copied, 'socks5h://[::1]:1080');
    assert.ok(!a.d.body.textContent.includes(':undefined'));
  } finally {
    a.close();
  }
});

test('MASQUE edits validate without AWG; migrated legacy transport controls are explicitly read-only', async () => {
  const s = status();
  delete s.profiles[2].awg;
  s.profiles.push(profile('legacy', 'legacy-m2w', { port: 1088 }));
  s.proxies.legacy = { ...s.proxies['p-b'] };
  const a = await mount({ state: s });
  try {
    edit(a, 'p-h2-name', 'renamed H2');
    edit(a, 'p-h2-transport-choice', 'quic');
    submit(a, 'p-h2-profile-form');
    await settle();
    const c = a.calls.find((c) => c.path === '/api/profiles/p-h2');
    assert.ok(c, 'hidden AWG validation must not block MASQUE save');
    assert.deepEqual(c.body, { name: 'renamed H2', transport: 'quic' });
    assert.equal(
      a.d.getElementById('p-h2-type-label').textContent,
      'MASQUE · QUIC',
    );
    assert.equal(a.d.getElementById('legacy-pinform').hidden, true);
    assert.match(
      a.d.getElementById('legacy-details').textContent,
      /только для чтения/,
    );
  } finally {
    a.close();
  }
});

test('Relay source changes require a new path acknowledgement and in-flight saves preserve newer resource drafts', async () => {
  const s = status();
  s.relays = [
    {
      id: 'resource',
      name: 'Resource',
      enabled: true,
      configured: true,
      source_ip: '',
      interface: '',
      path_confirmed: true,
      monitor_interval: 300,
    },
  ];
  const a = await mount({ state: s });
  let release;
  try {
    edit(a, 'resource-source_ip', '198.51.100.8');
    assert.equal(a.d.getElementById('resource-path_confirmed').checked, false);
    assert.equal(a.d.getElementById('resource-activate').disabled, true);
    submit(a, 'resource-relay-form');
    await settle();
    assert.deepEqual(
      a.calls.filter((c) => c.path === '/api/relays/resource').at(-1).body,
      { source_ip: '198.51.100.8', path_confirmed: false },
    );
    assert.equal(a.d.getElementById('resource-activate').disabled, true);
    edit(a, 'resource-name', 'sent');
    const gate = new Promise((r) => (release = r));
    a.setHandler(async (p) => (p === '/api/relays/resource' ? gate : null));
    submit(a, 'resource-relay-form');
    await settle();
    edit(a, 'resource-name', 'later');
    a.state.relays[0].name = 'sent';
    release(response({ persisted: true, applied: true }));
    await settle();
    assert.equal(a.d.getElementById('resource-name').value, 'later');
    assert.equal(a.d.getElementById('resource-name').dataset.dirty, 'true');
  } finally {
    a.close();
  }
});

for (const language of ['ru', 'en'])
  test(
    language +
      ' errors redact tokens/activation URLs and never interpret backend diagnostics as HTML',
    async () => {
      const token = String(Math.random()),
        url = 'https://de.tribukvy.ltd/test-' + token,
        a = await mount({
          language,
          url: 'http://console.test/?secret=' + token,
        });
      try {
        const raw =
          'raw <img src=x onerror=alert(1)> Bearer ' +
          token +
          ' ?secret=' +
          token;
        a.state.scan.log = [raw];
        a.state.proxies['p-a'].exit_info = raw;
        await a.w.refresh();
        assert.ok(!a.d.getElementById('scan-log').textContent.includes(token));
        assert.equal(a.d.getElementById('p-a-exit').querySelector('img'), null);
        edit(a, 'new-relay-url', url);
        a.setHandler(async (p) =>
          p === '/api/relays'
            ? response({ error: 'invalid ' + url }, 400)
            : p === '/api/log'
              ? response({ lines: [raw] })
              : null,
        );
        submit(a, 'new-relay-form');
        await settle();
        await a.w.refreshLog();
        assert.equal(a.d.getElementById('new-relay-url').value, '');
        assert.ok(!a.d.body.textContent.includes(token));
        assert.equal(a.d.getElementById('message').querySelector('img'), null);
        assert.match(
          a.d.getElementById('message').textContent,
          language === 'en' ? /\[redacted\]/ : /\[скрыто\]/,
        );
      } finally {
        a.close();
      }
    },
  );

for (const language of ['ru', 'en'])
  test(
    language +
      ' failed global settings remain dirty after blur and can be discarded; readback confirms typed settings only',
    async () => {
      const a = await mount({ language });
      try {
        const field = edit(a, 'cfg-check_period', '45');
        edit(a, 'cfg-autoscan_start', false);
        edit(a, 'cfg-exclude_countries', 'RU,BY');
        a.setHandler(async (p) =>
          p === '/api/config'
            ? response({ error: 'write denied', persisted: false }, 400)
            : null,
        );
        submit(a, 'config-form');
        await settle();
        assert.equal(field.value, '45');
        assert.match(
          a.d.getElementById('config-state').textContent,
          language === 'en' ? /Unsaved fields: 3/ : /Несохранённых полей: 3/,
        );
        assert.match(a.d.getElementById('message').textContent, /write denied/);
        a.state.config.check_period = 60;
        await a.w.refresh();
        assert.equal(field.value, '45');
        a.d.getElementById('reset-config').click();
        assert.equal(field.value, '60');
        edit(a, 'cfg-check_period', '50');
        a.setHandler(null);
        submit(a, 'config-form');
        await settle();
        assert.deepEqual(
          a.calls.filter((c) => c.path === '/api/config').at(-1).body,
          { check_period: 50 },
        );
        assert.equal(
          a.d.getElementById('config-state').textContent,
          language === 'en' ? 'No changes' : 'Нет изменений',
        );
        if (language === 'en') english(a.d);
      } finally {
        a.close();
      }
    },
  );

test('normalized readback confirms filters/name without hiding mismatches or depending on AWG JSON key order', async () => {
  const a = await mount({
    handler: async (path, opts, state) => {
      if (path === '/api/profiles/p-a') {
        const body = JSON.parse(opts.body);
        Object.assign(state.profiles[0], body);
        if (body.name !== undefined) state.profiles[0].name = body.name.trim();
        if (body.exclude_countries !== undefined)
          state.profiles[0].exclude_countries = 'RU,BY';
        if (body.awg)
          state.profiles[0].awg = Object.fromEntries(
            Object.entries(body.awg).reverse(),
          );
        state.revision++;
        return response({ persisted: true, applied: true });
      }
    },
  });
  try {
    edit(a, 'p-a-name', '  renamed  ');
    edit(a, 'p-a-exclude_countries', 'ru, by,ru');
    edit(a, 'p-a-awg-jc', '7');
    submit(a, 'p-a-profile-form');
    await settle();
    assert.match(a.d.getElementById('message').textContent, /Сохранено\./);
    assert.equal(a.d.getElementById('p-a-exclude_countries').value, 'RU,BY');
    assert.equal(a.d.getElementById('p-a-name').value, 'renamed');
    assert.equal(a.d.getElementById('p-a-awg-jc').dataset.dirty, undefined);
  } finally {
    a.close();
  }
});

test('missing revision fails closed and never refreshes stale green readiness', async () => {
  const a = await mount();
  try {
    delete a.state.revision;
    await a.w.refresh();
    assert.equal(a.d.getElementById('p-a-toggle').disabled, true);
    assert.notEqual(a.d.getElementById('p-a-policy').className, 'ok');
  } finally {
    a.close();
  }
});

test('profile wizard creates WARP/MASQUE, edits only its stable row, restores API AWG preset and deletes last profile', async () => {
  const a = await mount();
  try {
    edit(a, 'new-profile-name', 'another WARP');
    edit(a, 'new-profile-port', '1090');
    submit(a, 'new-profile-form');
    await settle();
    let c = a.calls
      .filter((c) => c.path === '/api/profiles' && c.method === 'POST')
      .at(-1);
    assert.ok(c);
    assert.equal(c.body.type, 'warp');
    assert.equal(c.body.name, 'another WARP');
    assert.equal(c.body.port, 1090);
    assert.deepEqual(c.body.awg, awg);
    edit(a, 'new-profile-type', 'masque');
    edit(a, 'new-profile-name', 'H2');
    edit(a, 'new-profile-port', '1091');
    edit(a, 'new-profile-transport', 'http2');
    submit(a, 'new-profile-form');
    await settle();
    c = a.calls
      .filter((c) => c.path === '/api/profiles' && c.method === 'POST')
      .at(-1);
    assert.equal(c.body.type, 'masque');
    assert.equal(c.body.transport, 'http2');
    assert.ok(!('awg' in c.body));
    const other = edit(a, 'p-b-name', 'unsaved sibling');
    edit(a, 'p-a-name', 'edited');
    edit(a, 'p-a-exclude_countries', 'BY');
    edit(a, 'p-a-awg-jc', '7');
    await a.w.refresh();
    assert.equal(other.value, 'unsaved sibling');
    assert.equal(a.d.getElementById('p-a-awg-jc').value, '7');
    submit(a, 'p-a-profile-form');
    await settle();
    c = a.calls
      .filter((c) => c.path === '/api/profiles/p-a' && c.method === 'PATCH')
      .at(-1);
    assert.equal(c.body.name, 'edited');
    assert.equal(c.body.exclude_countries, 'BY');
    assert.equal(c.body.awg.jc, 7);
    assert.equal(other.value, 'unsaved sibling');
    assert.ok(!a.calls.some((c) => c.path === '/api/profiles/p-b'));
    a.d.getElementById('p-a-awg-reset').click();
    assert.equal(a.d.getElementById('p-a-awg-jc').value, String(awg.jc));
    submit(a, 'p-a-profile-form');
    await settle();
    assert.deepEqual(
      a.calls.filter((c) => c.path === '/api/profiles/p-a').at(-1).body.awg,
      awg,
    );
    a.w.confirm = () => false;
    a.d.getElementById('p-a-delete').click();
    await settle();
    assert.ok(a.state.profiles.some((p) => p.id === 'p-a'));
    a.w.confirm = () => true;
    for (const p of [...a.state.profiles]) {
      a.d.getElementById(p.id + '-delete').click();
      await settle();
    }
    assert.equal(a.state.profiles.length, 0);
    assert.equal(a.d.querySelectorAll('#services [role=switch]').length, 0);
  } finally {
    a.close();
  }
});
