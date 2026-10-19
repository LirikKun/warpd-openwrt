// Acknowledge the external Relay service, not unused generator code.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const Module = require('node:module');
const path = require('node:path');
const helper = new Module(path.join(__dirname, 'ui_credits.helpers.cjs'), module);
helper.filename = path.join(__dirname, 'ui_credits.helpers.cjs');
helper.paths = module.paths;
const existing = fs.readFileSync(path.join(__dirname, 'ui_interaction.test.cjs'), 'utf8');
helper._compile(existing.slice(0, existing.indexOf('\ntest(')) + '\nmodule.exports={mount};', helper.filename);
const { mount } = helper.exports;
for (const language of ['ru', 'en']) {
  test(language + ' credits identify only the external Relay service and survive polling', async () => {
    const a = await mount({ language });
    try {
      const credit = a.d.getElementById('credits');
      assert.ok(credit, 'dedicated acknowledgement outside overwritten runtime footer');
      assert.match(credit.textContent, language === 'ru' ? /Relay-сервис/ : /Relay service/);
      const links = [...credit.querySelectorAll('a')];
      assert.equal(links.length, 1, 'do not imply use of the generator project');
      assert.equal(links[0].getAttribute('href'), 'https://github.com/nellimonix');
      assert.equal(links[0].textContent, 'llimonix');
      assert.equal(credit.textContent.includes('warp-config-generator-vercel'), false);
      assert.equal(a.d.getElementById('footer').contains(credit), false);
      await a.w.refresh();
      assert.equal(a.d.getElementById('credits'), credit);
      assert.match(credit.textContent, language === 'ru' ? /Relay-сервис/ : /Relay service/);
      assert.equal(a.calls.filter(c => c.method !== 'GET').length, 0);
    } finally { a.close(); }
  });
}

test('current documentation credits the service, not unused generator code or a bundled component', () => {
  for (const name of ['README.md', 'README.ru.md', 'docs/tui-policy-console.md']) {
    const text = fs.readFileSync(path.join(__dirname, name), 'utf8');
    assert.ok(text.includes('https://github.com/nellimonix'), name);
    assert.equal(text.includes('warp-config-generator-vercel'), false, name);
    assert.match(text, /Relay[- ](?:сервис|service)/, name);
  }
  const components = fs.readFileSync(path.join(__dirname, 'THIRD_PARTY.md'), 'utf8');
  assert.equal(components.includes('llimonix'), false, 'external-service thanks belong in the guide, not bundled components');
  assert.equal(components.includes('warp-config-generator-vercel'), false);
  assert.ok(components.includes('WarpScout 0.16.0'));
  assert.ok(components.includes('Mihomo 1.19.31'));
});
