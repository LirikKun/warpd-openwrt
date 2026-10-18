// Regression: credential endpoint metadata is not runtime health.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { execFileSync } = require('node:child_process');
const { JSDOM } = require(process.env.JSDOM_PATH || 'jsdom');
test('EN/RU credential presentation never declares the implemented runtime absent or permanent unapplied state', () => {
  for (const language of ['en', 'ru']) {
    const dir = process.env.WARPD_UI_FIXTURES;
    execFileSync(process.env.GO_BINARY, ['test','webui.go','webui_i18n_test.go','-run','^TestWebUILocalizationFixtures$','-count=1'], {cwd:__dirname,env:process.env});
    const html=fs.readFileSync(path.join(dir,language+'.html'),'utf8');
    assert.doesNotMatch(html, /Relay runtime is unavailable|Среда Relay недоступна|среда Relay недоступна|tunnel will remain unavailable|туннель останется недоступен/);
    assert.match(html, language==='en' ? /Tunnel and exit readiness: see network and policy status/ : /Готовность туннеля и выхода — в статусах сети и политики/);
    assert.match(html, language==='en' ? /This action does not apply tunnel settings/ : /Это действие не применяет настройки туннеля/);
    // Parsing the rendered production document catches accidental malformed markup.
    const dom=new JSDOM(html); assert.equal(dom.window.document.documentElement.lang,language);dom.window.close();
  }
});

test('EN/RU access monitoring does not deny the independent proxy runtime readiness', () => {
  for (const language of ['en', 'ru']) {
    const html=fs.readFileSync(path.join(process.env.WARPD_UI_FIXTURES,language+'.html'),'utf8');
    assert.doesNotMatch(html, /Tunnel and foreign IP not verified|Туннель и иностранный IP не подтверждены/);
    assert.match(html, language==='en' ? /Monitoring does not verify tunnel readiness/ : /Мониторинг не подтверждает готовность туннеля/);
  }
});
