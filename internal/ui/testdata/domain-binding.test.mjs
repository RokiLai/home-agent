import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', 'static');
const html = fs.readFileSync(path.join(root, 'index.html'), 'utf8');
const settings = fs.readFileSync(path.join(root, 'js/settings.js'), 'utf8');
const actions = fs.readFileSync(path.join(root, 'js/devices/actions.js'), 'utf8');
const render = fs.readFileSync(path.join(root, 'js/devices/render.js'), 'utf8');

test('server and device domain binding UI expose preflight and explicit confirmation flow', () => {
  assert.match(html, /serverDomainBindings/);
  assert.match(html, /DNS AAAA 不会自动删除/);
  assert.match(settings, /domain-bindings\/preflight/);
  assert.match(settings, /existing_record/);
  assert.match(render, /deviceDomainBindings/);
  assert.match(actions, /domain-bindings\/preflight/);
  assert.match(actions, /publisher_disabled_confirmed/);
});
