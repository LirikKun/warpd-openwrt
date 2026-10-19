// Exercise the production page and locale renderer, never a router.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const os = require('node:os');
const { execFileSync } = require('node:child_process');
const { JSDOM } = require(process.env.JSDOM_PATH || 'jsdom');
const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'warpd-workspace-'));
execFileSync(process.env.GO_BINARY || 'go', ['test', 'webui.go', 'webui_i18n_test.go', '-run', '^TestWebUILocalizationFixtures$', '-count=1'], {
  cwd: __dirname, env: { ...process.env, WARPD_UI_FIXTURES: dir }, stdio: 'pipe',
});
const pages = Object.fromEntries(['ru', 'en'].map(l => [l, fs.readFileSync(path.join(dir, l + '.html'), 'utf8')]));
fs.rmSync(dir, { recursive: true, force: true });
function open(language, hash = '') {
  const requests = [];
  const dom = new JSDOM(pages[language], { url: 'http://router.test/' + hash, runScripts: 'dangerously', pretendToBeVisual: true,
    beforeParse(w) {
      w.fetch = async (url, options = {}) => { requests.push({ url, method: options.method || 'GET' }); return { ok: false, status: 401, json: async () => ({ error: 'unauthorized' }) }; };
      w.setInterval = () => 0;
      w.confirm = () => false;
    },
  });
  return { dom, w: dom.window, d: dom.window.document, requests };
}
for (const language of ['ru', 'en']) {
  test(language + ' compact workspace separates Relay, search and maintenance without writes', async () => {
    const { dom, w, d, requests } = open(language);
    try {
      const nav = d.getElementById('workspace-nav');
      assert.ok(nav, 'workspace navigation exists');
      const links = [...nav.querySelectorAll('a')];
      assert.deepEqual(links.map(l => l.hash), ['#proxies', '#relay', '#search', '#settings']);
      assert.equal(nav.getAttribute('aria-label'), language === 'ru' ? 'Разделы' : 'Sections');
      const marker = d.createElement('span');
      marker.className = 'track';
      d.body.append(marker);
      assert.equal(w.getComputedStyle(marker).whiteSpace, 'nowrap', 'terminal switch marker stays on one line');
      assert.equal(w.getComputedStyle(marker).fontSize, '16px', '3ch width matches marker font');
      marker.remove();
      assert.deepEqual(links.map(l => l.textContent.trim()), language === 'ru' ? ['Прокси', 'Relay', 'Поиск', 'Настройки'] : ['Proxies', 'Relay', 'Search', 'Settings']);
      assert.equal(d.querySelectorAll('.workspace-panel:not([hidden])').length, 1);
      assert.equal(d.getElementById('proxies-view').hidden, false);
      const draft = d.getElementById('new-profile-name');
      draft.value = 'unsaved draft';
      for (const route of ['relay', 'search', 'settings', 'proxies']) {
        const link = nav.querySelector('[href="#' + route + '"]');
        link.click();
        assert.equal(d.getElementById(route + '-view').hidden, false);
        assert.equal(d.querySelectorAll('.workspace-panel:not([hidden])').length, 1);
        assert.equal(link.getAttribute('aria-current'), 'page');
      }
      assert.equal(d.getElementById('new-profile-name'), draft, 'navigation preserves nodes');
      assert.equal(draft.value, 'unsaved draft', 'navigation preserves drafts');
      assert.ok(d.getElementById('relay-view').contains(d.getElementById('create-relay')));
      assert.ok(d.getElementById('proxies-view').contains(d.getElementById('create-profile')));
      assert.ok(d.getElementById('search-view').contains(d.getElementById('scan-panel')));
      assert.ok(d.getElementById('settings-view').contains(d.getElementById('settings')));
      assert.ok(d.getElementById('settings-view').contains(d.getElementById('diagnostics')));
      assert.equal(requests.some(r => r.method !== 'GET'), false, 'navigation never mutates API state');
      w.location.hash = '#relay';
      w.dispatchEvent(new w.HashChangeEvent('hashchange'));
      assert.equal(d.getElementById('relay-view').hidden, false, 'history route restores section');
    } finally { await new Promise(resolve => setImmediate(resolve)); dom.window.close(); }
  });
  test(language + ' deep links and unknown fragments select a valid workspace', async () => {
    for (const [hash, route] of [['#relay', 'relay'], ['#search', 'search'], ['#settings', 'settings'], ['#unknown', 'proxies']]) {
      const { dom, d } = open(language, hash);
      try { assert.equal(d.getElementById(route + '-view')?.hidden, false); }
      finally { await new Promise(resolve => setImmediate(resolve)); dom.window.close(); }
    }
  });
}
