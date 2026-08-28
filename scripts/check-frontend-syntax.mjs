#!/usr/bin/env node

/**
 * scripts/check-frontend-syntax.mjs
 *
 * 零外部依赖前端静态语法与作用域检查器。
 * 负责校验 CSS 括号成对平衡与媒体查询作用域、JS 语法编译合法性、HTML ID 唯一性与关键结构。
 */

import { readFileSync, readdirSync, statSync, existsSync } from 'node:fs';
import { resolve, join, dirname, extname, relative } from 'node:path';
import { fileURLToPath } from 'node:url';
import vm from 'node:vm';

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);
const ROOT_DIR = resolve(__dirname, '..');

/**
 * 校验 CSS 文本的语法、注释闭合、括号成对及作用域泄露
 * @param {string} css
 * @param {string} [filename='style.css']
 * @returns {{ valid: boolean, errors: string[], rulesCount: number, mediaQueriesCount: number }}
 */
export function checkCSS(css, filename = 'style.css') {
  const errors = [];
  let line = 1;
  let col = 1;
  let inComment = false;
  let inString = null; // '"' or "'"
  let commentStart = { line: 1, col: 1 };
  let stringStart = { line: 1, col: 1 };

  const scopeStack = []; // { type: 'rule' | 'at-rule', selector: string, line: number, col: number }
  let currentSelector = '';
  let rulesCount = 0;
  let mediaQueriesCount = 0;

  // 必须位于顶层作用域、不得被误关进 @media 的关键全局类
  const REQUIRED_TOP_LEVEL_SELECTORS = [
    '.login-overlay',
    '.login-overlay.hidden',
    '.login-card',
    '.hidden',
    '.toast'
  ];

  for (let i = 0; i < css.length; i++) {
    const char = css[i];
    const nextChar = i + 1 < css.length ? css[i + 1] : '';

    if (inComment) {
      if (char === '*' && nextChar === '/') {
        inComment = false;
        i++;
        col += 2;
        continue;
      }
    } else if (inString) {
      if (char === '\\') {
        // Skip escaped char
        i++;
        col += 2;
        continue;
      } else if (char === inString) {
        inString = null;
      }
    } else {
      // Normal code outside comment & string
      if (char === '/' && nextChar === '*') {
        inComment = true;
        commentStart = { line, col };
        i++;
        col += 2;
        continue;
      } else if (char === '"' || char === "'") {
        inString = char;
        stringStart = { line, col };
      } else if (char === '{') {
        const sel = currentSelector.trim();
        const isAtRule = sel.startsWith('@');
        if (sel.startsWith('@media')) mediaQueriesCount++;
        else rulesCount++;

        // 检查关键全局类是否被误包在 @media 内部
        if (scopeStack.some(s => s.selector.startsWith('@media'))) {
          for (const reqSel of REQUIRED_TOP_LEVEL_SELECTORS) {
            if (sel === reqSel || sel.startsWith(reqSel + ' ') || sel.startsWith(reqSel + '{')) {
              errors.push(
                `${filename}:${line}:${col}: 全局基础类 "${sel}" 被错误嵌套在媒体查询 "${scopeStack[scopeStack.length - 1].selector}" 内部，必须定义在顶级作用域！`
              );
            }
          }
        }

        scopeStack.push({
          type: isAtRule ? 'at-rule' : 'rule',
          selector: sel,
          line,
          col
        });
        currentSelector = '';
      } else if (char === '}') {
        if (scopeStack.length === 0) {
          errors.push(`${filename}:${line}:${col}: 发现多余的闭合花括号 "}"，无对应的开始括号 "{"`);
        } else {
          scopeStack.pop();
        }
        currentSelector = '';
      } else if (char === ';') {
        currentSelector = '';
      } else {
        currentSelector += char;
      }
    }

    if (char === '\n') {
      line++;
      col = 1;
    } else {
      col++;
    }
  }

  if (inComment) {
    errors.push(`${filename}:${commentStart.line}:${commentStart.col}: CSS 注释 /* 未正确闭合`);
  }
  if (inString) {
    errors.push(`${filename}:${stringStart.line}:${stringStart.col}: CSS 字符串引号未正确闭合`);
  }
  if (scopeStack.length > 0) {
    for (const unclosed of scopeStack) {
      errors.push(
        `${filename}:${unclosed.line}:${unclosed.col}: 规则或媒体查询 "${unclosed.selector}" 缺少闭合花括号 "}"`
      );
    }
  }

  return {
    valid: errors.length === 0,
    errors,
    rulesCount,
    mediaQueriesCount
  };
}

import { spawnSync } from 'node:child_process';

/**
 * 校验 JavaScript / ESM 文件的语法合法性
 * @param {string} js
 * @param {string} [filename='script.js']
 * @returns {{ valid: boolean, errors: string[] }}
 */
export function checkJS(js, filename = 'script.js') {
  const errors = [];
  try {
    if (typeof vm.SourceTextModule === 'function') {
      new vm.SourceTextModule(js, { identifier: filename });
      return { valid: true, errors: [] };
    }
  } catch (err) {
    // If SourceTextModule throws syntax error directly, report it
    if (!err.message || !err.message.includes('experimental') && !err.message.includes('not enabled')) {
      errors.push(`${filename}: JS 语法错误: ${err.message}`);
      return { valid: false, errors };
    }
  }

  // Fallback syntax check with vm.compileFunction
  try {
    const stripped = js
      .replace(/import\s+.*?from\s+['"].*?['"];?/g, '// import')
      .replace(/import\s+['"].*?['"];?/g, '// import')
      .replace(/export\s+\*\s+from\s+['"].*?['"];?/g, '// export *')
      .replace(/export\s+\{[^}]*\}\s+from\s+['"].*?['"];?/g, '// export {} from')
      .replace(/export\s+\{[^}]*\};?/g, '// export {}')
      .replace(/export\s+(?:default\s+)?/g, '');
    vm.compileFunction(stripped, [], { filename });
  } catch (innerErr) {
    errors.push(`${filename}: JS 语法错误: ${innerErr.message}`);
  }

  return {
    valid: errors.length === 0,
    errors
  };
}

/**
 * 校验 HTML 文件的 DOM ID 唯一性与关键静态资源引用有效性
 * @param {string} html
 * @param {string} [baseDir]
 * @param {string} [filename='index.html']
 * @returns {{ valid: boolean, errors: string[], idsCount: number }}
 */
export function checkHTML(html, baseDir = ROOT_DIR, filename = 'index.html') {
  const errors = [];
  const idMap = new Map();
  const idRegex = /\sid=["']([^"']+)["']/g;
  let match;

  // 1. 检查 ID 唯一性
  while ((match = idRegex.exec(html)) !== null) {
    const id = match[1];
    const index = match.index;
    const lineNumber = html.substring(0, index).split('\n').length;
    if (idMap.has(id)) {
      errors.push(
        `${filename}:${lineNumber}: 发现重复的 DOM ID "${id}"，先前定义于第 ${idMap.get(id)} 行`
      );
    } else {
      idMap.set(id, lineNumber);
    }
  }

  // 2. 检查静态资源引用是否存在
  if (baseDir) {
    const staticDir = resolve(baseDir, 'internal/ui/static');
    const linkRegex = /<link\s+[^>]*href=["']\/static\/([^"'?]+)(?:\?[^"']*)?["']/g;
    while ((match = linkRegex.exec(html)) !== null) {
      const file = match[1];
      const targetPath = join(staticDir, file);
      if (!existsSync(targetPath)) {
        errors.push(`${filename}: 引用的 CSS 样式文件不存在: /static/${file} (目标路径: ${targetPath})`);
      }
    }

    const scriptRegex = /<script\s+[^>]*src=["']\/static\/([^"'?]+)(?:\?[^"']*)?["']/g;
    while ((match = scriptRegex.exec(html)) !== null) {
      const file = match[1];
      const targetPath = join(staticDir, file);
      if (!existsSync(targetPath)) {
        errors.push(`${filename}: 引用的 JS 脚本文件不存在: /static/${file} (目标路径: ${targetPath})`);
      }
    }
  }

  return {
    valid: errors.length === 0,
    errors,
    idsCount: idMap.size
  };
}

/**
 * 递归收集指定目录下的所有特定后缀文件
 */
function walkDir(dir, extensions = ['.js', '.mjs', '.css', '.html']) {
  let results = [];
  if (!existsSync(dir)) return results;
  const list = readdirSync(dir);
  for (const item of list) {
    const fullPath = join(dir, item);
    const stat = statSync(fullPath);
    if (stat && stat.isDirectory()) {
      results = results.concat(walkDir(fullPath, extensions));
    } else if (extensions.includes(extname(item))) {
      results.push(fullPath);
    }
  }
  return results;
}

/**
 * 执行全量前端静态语法与契约检查
 */
export function runAllChecks(rootDir = ROOT_DIR) {
  const uiStaticDir = resolve(rootDir, 'internal/ui/static');
  const allErrors = [];
  let checkedFiles = 0;

  // 1. 检查 CSS 文件
  const cssFiles = walkDir(uiStaticDir, ['.css']);
  for (const file of cssFiles) {
    checkedFiles++;
    const content = readFileSync(file, 'utf8');
    const res = checkCSS(content, relative(rootDir, file));
    if (!res.valid) allErrors.push(...res.errors);
  }

  // 2. 检查 JS / MJS 文件
  const jsFiles = walkDir(uiStaticDir, ['.js', '.mjs']);
  for (const file of jsFiles) {
    checkedFiles++;
    const content = readFileSync(file, 'utf8');
    const res = checkJS(content, relative(rootDir, file));
    if (!res.valid) allErrors.push(...res.errors);
  }

  // 3. 检查 HTML 文件
  const htmlPath = resolve(uiStaticDir, 'index.html');
  if (existsSync(htmlPath)) {
    checkedFiles++;
    const content = readFileSync(htmlPath, 'utf8');
    const res = checkHTML(content, rootDir, relative(rootDir, htmlPath));
    if (!res.valid) allErrors.push(...res.errors);
  }

  return {
    success: allErrors.length === 0,
    errors: allErrors,
    checkedFiles
  };
}

// CLI 执行入口
if (process.argv[1] && resolve(process.argv[1]) === resolve(__filename)) {
  const res = runAllChecks(ROOT_DIR);
  if (res.success) {
    console.log(`\x1b[32m✔ 前端静态语法与作用域检查全部通过（共扫描 ${res.checkedFiles} 个文件，0 个语法错误）\x1b[0m`);
    process.exit(0);
  } else {
    console.error(`\x1b[31m✖ 发现 ${res.errors.length} 处前端静态语法/作用域错误:\x1b[0m\n`);
    for (const err of res.errors) {
      console.error(`  - \x1b[31m${err}\x1b[0m`);
    }
    process.exit(1);
  }
}
