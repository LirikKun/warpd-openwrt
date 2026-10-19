// Public acknowledgements must remain visible after the dynamic footer updates.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { JSDOM } = require(process.env.JSDOM_PATH || 'jsdom');
test('Relay acknowledgement links preserve author and project attribution', () => {
  const dom = new JSDOM(fs.readFileSync(__dirname + '/webui.html', 'utf8'));
  try {
    const credit = dom.window.document.getElementById('credits');
    assert.ok(credit, 'dedicated credits outside overwritten runtime footer');
    assert.equal(credit.querySelector('[href="https://github.com/nellimonix"]').textContent, 'llimonix');
    assert.ok(credit.querySelector('[href="https://github.com/nellimonix/warp-config-generator-vercel"]'));
    assert.ok(!dom.window.document.getElementById('footer').contains(credit));
  } finally { dom.window.close(); }
});
