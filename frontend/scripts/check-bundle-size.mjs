// =============================================================================
// check-bundle-size.mjs — gzip budget for the shipped JS and CSS
// =============================================================================
// The SPA is embedded in the firmware image and fetched over the mesh, often
// on a 1–2 Mbit/s HaLow link, so bundle growth is a field cost. This guard
// gzips every .js/.css under <dist>/assets and compares three totals against
// bundle-budget.json:
//
//   initialJs  — JS index.html loads up front (entry + modulepreloads)
//   totalJs    — every JS chunk, lazy ones included
//   totalCss   — every stylesheet
//
// A total may grow up to `tolerancePercent` over its recorded baseline;
// beyond that the check fails. After a deliberate size change, record the
// new baseline with --update and commit bundle-budget.json with the reason.
//
// usage: node scripts/check-bundle-size.mjs [dist-dir] [--update]
//        dist-dir defaults to ../static (what make frontend produces).

import { readdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { gzipSync } from 'node:zlib';

const HERE = dirname(fileURLToPath(import.meta.url));
export const BUDGET_FILE = join(HERE, 'bundle-budget.json');
export const METRICS = ['initialJs', 'totalJs', 'totalCss'];

const ASSET_REF = /(?:src|href)="\/?(assets\/[^"]+\.(?:js|css))"/g;

function gzipSize(path) {
  return gzipSync(readFileSync(path), { level: 9 }).length;
}

// measure returns gzip byte totals for the build in distDir.
export function measure(distDir) {
  const assetsDir = join(distDir, 'assets');
  const files = readdirSync(assetsDir).filter((f) => f.endsWith('.js') || f.endsWith('.css'));
  if (files.length === 0) throw new Error(`no .js/.css files in ${assetsDir}`);

  const sizes = new Map(files.map((f) => [f, gzipSize(join(assetsDir, f))]));
  const html = readFileSync(join(distDir, 'index.html'), 'utf8');
  const initial = new Set([...html.matchAll(ASSET_REF)].map((m) => m[1].slice('assets/'.length)));
  if (![...initial].some((f) => f.endsWith('.js'))) throw new Error(`index.html in ${distDir} references no assets/*.js`);

  let initialJs = 0;
  let totalJs = 0;
  let totalCss = 0;
  for (const [file, size] of sizes) {
    if (file.endsWith('.css')) {
      totalCss += size;
      continue;
    }
    totalJs += size;
    if (initial.has(file)) initialJs += size;
  }
  return { initialJs, totalJs, totalCss };
}

// compare returns one line per metric and whether every metric is in budget.
export function compare(measured, budget) {
  const tolerance = budget.tolerancePercent;
  const lines = [];
  let ok = true;
  for (const metric of METRICS) {
    const base = budget.baseline[metric];
    const limit = Math.floor(base * (1 + tolerance / 100));
    const now = measured[metric];
    const delta = ((now - base) / base) * 100;
    const pass = now <= limit;
    ok &&= pass;
    lines.push(`${pass ? 'ok  ' : 'FAIL'} ${metric.padEnd(9)} ${kb(now)} gzip (baseline ${kb(base)}, limit ${kb(limit)}, ${delta >= 0 ? '+' : ''}${delta.toFixed(1)}%)`);
  }
  return { ok, lines };
}

function kb(bytes) {
  return `${(bytes / 1024).toFixed(1)} KiB`;
}

function main(argv) {
  const update = argv.includes('--update');
  const dir = resolve(argv.find((a) => !a.startsWith('--')) || join(HERE, '..', '..', 'static'));
  const measured = measure(dir);
  const budget = JSON.parse(readFileSync(BUDGET_FILE, 'utf8'));

  if (update) {
    budget.baseline = measured;
    writeFileSync(BUDGET_FILE, `${JSON.stringify(budget, null, 2)}\n`);
    console.log(`bundle-budget.json baseline updated from ${dir}`);
    return 0;
  }

  const { ok, lines } = compare(measured, budget);
  console.log(`bundle size (${dir}, tolerance +${budget.tolerancePercent}%):`);
  for (const line of lines) console.log(`  ${line}`);
  if (!ok) {
    console.error('bundle size over budget: trim the change, or run with --update and justify the new baseline in the commit');
    return 1;
  }
  return 0;
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  process.exit(main(process.argv.slice(2)));
}
