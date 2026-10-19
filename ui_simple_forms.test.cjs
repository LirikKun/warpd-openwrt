// New UX coverage reuses the existing offline production-DOM harness only.
// The helper prefix contains no test registrations and mocks HTTP, never JS.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const Module = require('node:module');
const path = require('node:path');
const helper = new Module(path.join(__dirname, 'ui_simple_forms.helpers.cjs'), module);
helper.filename = path.join(__dirname, 'ui_simple_forms.helpers.cjs');
helper.paths = module.paths;
const existing = fs.readFileSync(path.join(__dirname, 'ui_interaction.test.cjs'), 'utf8');
helper._compile(existing.slice(0, existing.indexOf('\ntest(')) + '\nmodule.exports={mount,status,edit,submit,settle,response,withRelayProfile};', helper.filename);
const { mount, status, edit, submit, settle, response, withRelayProfile } = helper.exports;
const byId = (a, id) => a.d.getElementById(id);
test('active Relay monitoring is neutral and points to linked proxy readiness; failures remain visible', async () => {
  const state = withRelayProfile();
  Object.assign(state.relays[0], { source_ip: '192.0.2.2', path_confirmed: true, status: {} });
  const a = await mount({ state });
  try {
    assert.equal(byId(a, 'r-access-state').textContent, 'Мониторинг включён · туннель — в профиле прокси');
    assert.equal(byId(a, 'r-access-state').classList.contains('warn'), false);
    a.state.relays[0].status.error = 'synthetic observation failure';
    await a.w.refresh();
    assert.equal(byId(a, 'r-access-state').textContent, 'Ошибка мониторинга');
    assert.equal(byId(a, 'r-access-state').classList.contains('bad'), true);
  } finally { a.close(); }
});

const writes = a => a.calls.filter(c => c.method !== 'GET');

test('Relay endpoint presentation uses the production country host/UDP port, never its loopback bridge or readiness', async () => {
  // Bind this UI expectation to the unchanged authoritative transport mapping.
  const backend = fs.readFileSync(path.join(__dirname, 'relay_udp.go'), 'utf8');
  assert.match(backend, /case "DE", "NL", "FI", "LV", "EE":\s*host = strings.ToLower\(cfg.Country\) \+ "\.tribukvy.ltd"/);
  assert.match(backend, /netip.AddrPortFrom\(chosen, 4500\)/);
  const state = withRelayProfile();
  state.proxies['p-relay'].endpoint = '127.0.0.1:23451';
  const a = await mount({ state });
  try {
    for (const country of ['DE', 'NL', 'FI', 'LV', 'EE']) {
      a.state.profiles.find(p => p.id === 'p-relay').relay_country = country;
      a.state.proxies['p-relay'].relay_country = country;
      await a.w.refresh();
      assert.equal(byId(a, 'p-relay-ep').textContent, country.toLowerCase() + '.tribukvy.ltd:4500');
      assert.notEqual(byId(a, 'p-relay-transport').className, 'ok');
    }
    a.state.profiles.find(p => p.id === 'p-relay').relay_country = 'ZZ';
    a.state.proxies['p-relay'].relay_country = 'ZZ';
    await a.w.refresh();
    assert.equal(byId(a, 'p-relay-ep').textContent, '—', 'no invented mapping for unknown country');
  } finally { a.close(); }
});

for (const language of ['ru', 'en']) {
  test(language + ' obvious per-row Rename opens settings and selects the name; saving changes only that profile', async () => {
    const a = await mount({ language, state: withRelayProfile() });
    try {
      for (const p of a.state.profiles) {
        const button = byId(a, p.id + '-rename');
        assert.ok(button, 'rename action for ' + p.type);
        assert.equal(button.textContent, language === 'en' ? 'Rename' : 'Переименовать');
        const details = byId(a, p.id + '-details');
        assert.ok(details.contains(button), 'rename belongs inside Connection and details');
        assert.equal(byId(a, 'row-' + p.id).querySelector('.service-main').contains(button), false);
        assert.equal(details.open, false, 'rename stays behind the initially collapsed details');
        details.open = true;
        button.click();
        const input = byId(a, p.id + '-name');
        assert.equal(byId(a, p.id + '-details').open, true);
        assert.equal(byId(a, p.id + '-advanced').open, true);
        assert.equal(a.d.activeElement, input);
        assert.equal(input.selectionStart, 0);
        assert.equal(input.selectionEnd, input.value.length);
      }
      assert.equal(writes(a).length, 0, 'Rename action only edits a draft');
      edit(a, 'p-a-name', 'Renamed WARP');
      await a.w.refresh();
      assert.equal(byId(a, 'p-a-name').value, 'Renamed WARP');
      submit(a, 'p-a-profile-form');
      await settle();
      assert.deepEqual(writes(a).map(c => ({ path: c.path, body: c.body })),
        [{ path: '/api/profiles/p-a', body: { name: 'Renamed WARP' } }]);
      assert.equal(byId(a, 'row-p-a').querySelector('h3').textContent, 'Renamed WARP');
    } finally { a.close(); }
  });

  test(language + ' acknowledged but unbound Relay remains honestly unconfirmed after successful desired-state save', async () => {
    const state = status();
    state.relays = [{ id: 'r-bound', name: 'Relay', configured: true, enabled: false,
      source_ip: '', interface: '', path_confirmed: true, monitor_interval: 300 }];
    const a = await mount({ language, state });
    try {
      byId(a, 'r-bound-toggle').click();
      await settle();
      assert.match(byId(a, 'r-bound-state').textContent,
        language === 'en' ? /Activation path not confirmed/ : /Путь активации не подтверждён/);
      assert.equal(byId(a, 'r-bound-path-help').hidden, false);
      assert.equal(byId(a, 'r-bound-advanced').open, true);
    } finally { a.close(); }
  });

  test(language + ' proxy primary fields stay type/link/country; advanced defaults follow type without overwriting drafts or choosing a port', async () => {
    const state = withRelayProfile();
    const a = await mount({ language, state });
    try {
      const f = byId(a, 'new-profile-form'), advanced = byId(a, 'new-profile-advanced');
      assert.ok(advanced, 'proxy Advanced disclosure');
      assert.equal(advanced.open, false);
      for (const key of ['name', 'listen', 'port', 'transport', 'exclude_nodes', 'exclude_countries', 'awg-preset'])
        assert.ok(advanced.contains(byId(a, 'new-profile-' + key)), key);
      for (const key of ['type', 'relay_id', 'relay_country'])
        assert.equal(advanced.contains(byId(a, 'new-profile-' + key)), false, key);
      assert.equal(byId(a, 'new-profile-name').value, 'WARP');
      assert.equal(byId(a, 'new-profile-port').value, '', 'let backend allocate the port');
      assert.equal(byId(a, 'new-profile-port').required, false);
      edit(a, 'new-profile-type', 'masque');
      assert.equal(byId(a, 'new-profile-name').value, 'MASQUE');
      edit(a, 'new-profile-type', 'relay-warp');
      assert.equal(byId(a, 'new-profile-name').value, 'Relay-WARP');
      edit(a, 'new-profile-relay_id', 'r-access');
      assert.equal(f.checkValidity(), true);
      edit(a, 'new-profile-name', 'My draft');
      await a.w.refresh();
      assert.equal(byId(a, 'new-profile-name').value, 'My draft');
      edit(a, 'new-profile-type', 'warp');
      assert.equal(byId(a, 'new-profile-name').value, 'My draft');
      submit(a, 'new-profile-form');
      await settle();
      const post = writes(a).find(c => c.path === '/api/profiles');
      assert.ok(post);
      assert.equal(Object.hasOwn(post.body, 'port'), false);
      assert.equal(post.body.name, 'My draft');
      assert.equal(post.body.type, 'warp');
      assert.equal(Object.hasOwn(post.body, 'policy_mode'), false, 'untouched default is assigned by the backend');
    } finally { a.close(); }
  });
}

for (const language of ['ru', 'en']) {
  test(language + ' enable without a confirmed bound path opens Advanced with an inline action, never inventing consent', async () => {
    const state = status();
    state.relays = [{ id: 'r-safe', name: 'Relay', configured: true, enabled: false,
      source_ip: '', interface: '', path_confirmed: false, monitor_interval: 300 }];
    const a = await mount({ language, state, handler: async (p, opts, s) => {
      if (p === '/api/relays/r-safe') {
        Object.assign(s.relays[0], JSON.parse(opts.body));
        return response({ persisted: true, applied: false, error: 'Relay source path is not confirmed' }, 503);
      }
    } });
    try {
      byId(a, 'r-safe-toggle').click();
      await settle();
      assert.equal(byId(a, 'r-safe-advanced').open, true);
      const hint = byId(a, 'r-safe-path-help');
      assert.ok(hint && !hint.hidden, 'visible inline path guidance');
      assert.match(hint.textContent, language === 'en' ? /source IP or interface/ : /исходный IP или интерфейс/);
      assert.deepEqual(writes(a).map(c => c.body), [{ enabled: true }]);
      assert.equal(byId(a, 'r-safe-path_confirmed').checked, false);
      assert.equal(byId(a, 'r-safe-activate').disabled, true);
      assert.equal(byId(a, 'r-safe-source_ip').value, '');
      byId(a, 'r-safe-advanced').open = false;
      byId(a, 'r-safe-configure-path').click();
      assert.equal(byId(a, 'r-safe-advanced').open, true);
      assert.equal(a.d.activeElement, byId(a, 'r-safe-source_ip'));
    } finally { a.close(); }
  });
}

for (const language of ['ru', 'en']) {
  test(language + ' new Relay has only activation URL outside collapsed Advanced; URL-only save is explicitly disabled', async () => {
    const a = await mount({ language });
    try {
      const f = byId(a, 'new-relay-form');
      assert.match(f.textContent, language === 'en'
        ? /Saved disabled, without activation/
        : /Сохраняется выключенным, без активации/);
      const primary = [...f.querySelectorAll('[data-key]')].filter(n => !f.contains(n.closest('details')));
      assert.deepEqual(primary.map(n => n.dataset.key), ['url']);
      const advanced = byId(a, 'new-relay-advanced');
      assert.ok(advanced);
      assert.equal(advanced.open, false);
      assert.equal(advanced.querySelector('summary').textContent, language === 'en' ? 'Advanced settings' : 'Расширенные настройки');
      for (const key of ['name', 'source_ip', 'interface', 'monitor_interval', 'path_confirmed'])
        assert.ok(advanced.contains(byId(a, 'new-relay-' + key)), key);
      assert.equal(byId(a, 'new-relay-name').value, 'Relay');
      edit(a, 'new-relay-url', 'https://relay.tribukvy.ltd/activate/SYNTHETIC_TEST_ONLY');
      submit(a, 'new-relay-form');
      await settle();
      const post = writes(a).find(c => c.path === '/api/relays');
      assert.ok(post);
      assert.equal(post.body.enabled, false);
      assert.equal(post.body.path_confirmed, false);
      assert.equal(post.body.source_ip, '');
      assert.equal(post.body.interface, '');
      assert.equal(post.body.name, 'Relay');
      assert.equal(writes(a).length, 1, 'save must not activate or generate an account');
      assert.equal(byId(a, 'new-relay-url').value, '');
    } finally { a.close(); }
  });
}
