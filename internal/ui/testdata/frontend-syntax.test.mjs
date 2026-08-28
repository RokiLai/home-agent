import test from 'node:test';
import assert from 'node:assert/strict';
import { checkCSS, checkJS, checkHTML, runAllChecks } from '../../../scripts/check-frontend-syntax.mjs';

// ---------------------------------------------------------------------------
// 1. CSS Syntax and Scope Validation Tests
// ---------------------------------------------------------------------------

test('checkCSS passes for valid balanced CSS rules and media queries', () => {
  const validCSS = `
    .btn {
      padding: 10px;
      color: #fff;
    }
    @media (max-width: 640px) {
      .btn {
        padding: 6px;
      }
    }
    .login-overlay {
      position: fixed;
      inset: 0;
    }
  `;
  const result = checkCSS(validCSS, 'test.css');
  assert.equal(result.valid, true);
  assert.equal(result.errors.length, 0);
  assert.equal(result.mediaQueriesCount, 1);
});

test('checkCSS catches missing closing brace and identifies location', () => {
  const brokenCSS = `
    .device-card {
      padding: 12px;
      gap: 8px;

    .device-card > div {
      gap: 6px;
    }
  `;
  const result = checkCSS(brokenCSS, 'broken.css');
  assert.equal(result.valid, false);
  assert.ok(result.errors.length > 0);
  assert.match(result.errors[0], /缺少闭合花括号/);
});

test('checkCSS catches extra closing brace', () => {
  const brokenCSS = `
    .btn { padding: 10px; }
    }
  `;
  const result = checkCSS(brokenCSS, 'extra.css');
  assert.equal(result.valid, false);
  assert.match(result.errors[0], /发现多余的闭合花括号/);
});

test('checkCSS rejects top-level core classes accidentally trapped inside @media', () => {
  const trappedCSS = `
    @media (max-width: 400px) {
      .stats-grid { grid-template-columns: 1fr; }

      .login-overlay {
        position: fixed;
        inset: 0;
      }
    }
  `;
  const result = checkCSS(trappedCSS, 'trapped.css');
  assert.equal(result.valid, false);
  assert.match(result.errors[0], /全局基础类 "\.login-overlay" 被错误嵌套在媒体查询/);
});

test('checkCSS catches unclosed comments and strings', () => {
  const unclosedComment = `.btn { color: red; /* unclosed comment`;
  const resComment = checkCSS(unclosedComment, 'comment.css');
  assert.equal(resComment.valid, false);
  assert.match(resComment.errors[0], /注释 \/\* 未正确闭合/);

  const unclosedString = `.btn { content: "unclosed; }`;
  const resString = checkCSS(unclosedString, 'string.css');
  assert.equal(resString.valid, false);
  assert.match(resString.errors[0], /字符串引号未正确闭合/);
});

// ---------------------------------------------------------------------------
// 2. JS Syntax Validation Tests
// ---------------------------------------------------------------------------

test('checkJS validates ESM JavaScript syntax', () => {
  const validJS = `
    import { state } from './state.js';
    export function doSomething(val) {
      return val ? state.item : null;
    }
  `;
  const result = checkJS(validJS, 'valid.js');
  assert.equal(result.valid, true);
  assert.equal(result.errors.length, 0);
});

test('checkJS catches invalid JavaScript syntax errors', () => {
  const invalidJS = `
    import { state } from './state.js';
    function broken( {
      const x = ;
    }
  `;
  const result = checkJS(invalidJS, 'broken.js');
  assert.equal(result.valid, false);
  assert.ok(result.errors.length > 0);
  assert.match(result.errors[0], /JS 语法错误/);
});

// ---------------------------------------------------------------------------
// 3. HTML DOM ID Unique Validation Tests
// ---------------------------------------------------------------------------

test('checkHTML passes for valid HTML with unique IDs', () => {
  const validHTML = `
    <!DOCTYPE html>
    <html>
      <body>
        <div id="header">Header</div>
        <div id="content">Content</div>
        <button id="submitBtn">Submit</button>
      </body>
    </html>
  `;
  const result = checkHTML(validHTML, null, 'index.html');
  assert.equal(result.valid, true);
  assert.equal(result.errors.length, 0);
  assert.equal(result.idsCount, 3);
});

test('checkHTML catches duplicate DOM IDs and reports line numbers', () => {
  const duplicateHTML = `
    <!DOCTYPE html>
    <html>
      <body>
        <div id="loginForm">First</div>
        <div id="other">Other</div>
        <form id="loginForm">Second</form>
      </body>
    </html>
  `;
  const result = checkHTML(duplicateHTML, null, 'duplicate.html');
  assert.equal(result.valid, false);
  assert.ok(result.errors.length > 0);
  assert.match(result.errors[0], /发现重复的 DOM ID "loginForm"/);
});

// ---------------------------------------------------------------------------
// 4. Repository Full Scan Test
// ---------------------------------------------------------------------------

test('runAllChecks passes cleanly for all repository static frontend assets', () => {
  const result = runAllChecks();
  assert.equal(result.success, true, `Expected 0 syntax errors, got: ${result.errors.join('; ')}`);
  assert.ok(result.checkedFiles >= 15, `Expected at least 15 frontend files checked, got ${result.checkedFiles}`);
});
