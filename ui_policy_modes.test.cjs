const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const Module = require('node:module');
const path = require('node:path');
const helper = new Module(path.join(__dirname, 'ui_policy_modes.helpers.cjs'), module);
helper.filename = path.join(__dirname, 'ui_policy_modes.helpers.cjs');
helper.paths = module.paths;
const existing = fs.readFileSync(path.join(__dirname, 'ui_interaction.test.cjs'), 'utf8');
helper._compile(existing.slice(0, existing.indexOf('\ntest(')) + '\nmodule.exports={mount,status,edit,submit,settle,response,withRelayProfile};', helper.filename);
const { mount, status, edit, submit, settle, response, withRelayProfile } = helper.exports;
const byId = (a, id) => a.d.getElementById(id);
const writes = a => a.calls.filter(c => c.method !== 'GET');

for (const language of ['ru', 'en']) {
  test(language + ' policy controls are advanced, strict public-IP target is explicit, and legacy edits do not silently migrate policy', async () => {
    const a = await mount({ language, state: withRelayProfile() });
    try {
      const mode = byId(a, 'new-profile-policy_mode'), country = byId(a, 'new-profile-policy_country');
      assert.ok(mode && country, 'per-profile policy controls');
      assert.ok(byId(a, 'new-profile-advanced').contains(mode));
      assert.ok(byId(a, 'new-profile-advanced').contains(country));
      assert.equal(mode.value, 'exclusions');
      assert.equal(mode.querySelector('option[value=""]'), null, 'no compatibility policy on create');
      assert.equal(country.disabled, true);
      assert.equal(country.required, false);
      edit(a, 'new-profile-type', 'relay-warp');
      edit(a, 'new-profile-relay_id', 'r-access');
      edit(a, 'new-profile-relay_country', 'DE');
      edit(a, 'new-profile-policy_mode', 'strict');
      assert.equal(country.disabled, false);
      assert.equal(country.required, true);
      assert.equal(country.value, '', 'server country is not the public-IP target');
      assert.equal(byId(a, 'new-profile-form').checkValidity(), false);
      edit(a, 'new-profile-policy_country', 'NL');
      await a.w.refresh();
      assert.equal(mode.value, 'strict');
      assert.equal(country.value, 'NL');
      submit(a, 'new-profile-form');
      await settle();
      const post = writes(a).find(c => c.path === '/api/profiles');
      assert.equal(post.body.policy_mode, 'strict');
      assert.equal(post.body.policy_country, 'NL');
      assert.equal(post.body.relay_country, 'DE');
      const compat = byId(a, 'p-a-policy_mode');
      assert.equal(compat.value, '');
      assert.equal(compat.querySelector('option[value=""]').disabled, true);
      assert.equal(compat.querySelector('option[value=""]').textContent,
        language === 'en' ? 'Existing policy (compatibility)' : 'Текущая политика (совместимость)');
      edit(a, 'p-a-name', 'Only name');
      submit(a, 'p-a-profile-form');
      await settle();
      assert.deepEqual(writes(a).at(-1).body, { name: 'Only name' });
      edit(a, 'p-a-policy_mode', 'strict');
      edit(a, 'p-a-policy_country', 'DE');
      submit(a, 'p-a-profile-form');
      await settle();
      assert.deepEqual(writes(a).at(-1).body, { policy_mode: 'strict', policy_country: 'DE' });
      edit(a, 'p-a-policy_mode', 'off');
      assert.equal(byId(a, 'p-a-policy_country').disabled, true);
      assert.equal(byId(a, 'p-a-policy_country').required, false);
      submit(a, 'p-a-profile-form');
      await settle();
      assert.deepEqual(writes(a).at(-1).body, { policy_mode: 'off', policy_country: '' });
      assert.equal(byId(a, 'p-a-policy_country').value, '', 'readback clears the disabled strict target');
      edit(a, 'p-a-policy_mode', 'strict');
      assert.equal(byId(a, 'p-a-profile-form').checkValidity(), false, 'strict needs a new explicit target after clearing');
      [...byId(a, 'p-a-profile-form').querySelectorAll('button')].find(b => b.textContent === (language === 'en' ? 'Discard edits' : 'Отменить правки')).click();
      assert.equal(byId(a, 'p-a-policy_mode').value, 'off');
      assert.equal(byId(a, 'p-a-policy_country').disabled, true, 'discard updates country applicability immediately');
    } finally { a.close(); }
  });

  test(language + ' explicit policy modes trust fresh backend verdicts; unknown modes fail closed and legacy geography stays guarded', async () => {
    const state = withRelayProfile();
    for (const p of state.profiles) Object.assign(p, { policy_mode: 'off', policy_country: '' });
    for (const p of Object.values(state.proxies)) Object.assign(p, {
      transport_ready: true, policy_ready: true, running: true,
      last_check: new Date().toISOString(), exit_node: 'DME', exit_node_country: 'RU',
      exit_country: 'RU', foreign_ip_verified: false, error: ''
    });
    const a = await mount({ language, state });
    try {
      assert.equal(byId(a, 'p-a-policy').className, 'ok', 'off permits transport without UI geography veto');
      assert.equal(byId(a, 'p-a-policy').textContent, language === 'en' ? 'Policy: off' : 'Политика: отключена');
      assert.equal(byId(a, 'p-relay-policy').className, 'ok', 'off Relay has no legacy foreign proof veto');
      assert.equal(byId(a, 'p-relay-policy-error').textContent, language === 'en' ? 'None' : 'Нет');
      const p = a.state.profiles[0], runtime = a.state.proxies[p.id];
      p.policy_mode = 'exclusions';
      await a.w.refresh();
      assert.equal(byId(a, 'p-a-policy').className, 'ok', 'backend verdict is authoritative for explicit exclusions too');
      p.policy_mode = 'strict'; p.policy_country = 'DE';
      runtime.policy_ready = false; runtime.policy_error = 'public IP country mismatch';
      await a.w.refresh();
      assert.notEqual(byId(a, 'p-a-policy').className, 'ok', 'strict mismatch never green');
      assert.match(byId(a, 'p-a-policy-error').textContent, /mismatch/);
      runtime.policy_ready = true; p.policy_mode = 'future-mode';
      await a.w.refresh();
      assert.notEqual(byId(a, 'p-a-policy').className, 'ok', 'unknown explicit mode fails closed');
      p.policy_mode = '';
      await a.w.refresh();
      assert.notEqual(byId(a, 'p-a-policy').className, 'ok', 'legacy keeps existing geography rules');
      p.policy_mode = 'off'; runtime.checking = true;
      await a.w.refresh();
      assert.notEqual(byId(a, 'p-a-policy').className, 'ok', 'pending stays unready');
      runtime.checking = false; runtime.last_check = '2000-01-01T00:00:00Z';
      await a.w.refresh();
      assert.notEqual(byId(a, 'p-a-policy').className, 'ok', 'stale stays unready');
    } finally { a.close(); }
  });
}
