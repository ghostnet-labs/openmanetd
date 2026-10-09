import { Buffer } from 'node:buffer';
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { gzipSync } from 'node:zlib';
import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { compare, measure } from '../../../scripts/check-bundle-size.mjs';

const gz = (s) => gzipSync(Buffer.from(s), { level: 9 }).length;

describe('TestCheckBundleSizeMeasure', () => {
  let dir;
  beforeEach(() => {
    dir = mkdtempSync(join(tmpdir(), 'bundle-'));
    mkdirSync(join(dir, 'assets'));
  });
  afterEach(() => {
    rmSync(dir, { recursive: true, force: true });
  });

  function write(name, body) {
    writeFileSync(join(dir, name), body);
  }

  it('splits initial and lazy JS and totals CSS', () => {
    const entry = 'console.log("entry");'.repeat(20);
    const vendor = 'export const v = 1;'.repeat(30);
    const lazy = 'export default function Lazy() {}'.repeat(40);
    const css = 'body{color:red}'.repeat(10);
    write('assets/index-a.js', entry);
    write('assets/vendor-b.js', vendor);
    write('assets/Lazy-c.js', lazy);
    write('assets/index-d.css', css);
    write('assets/logo.svg', '<svg/>');
    write('index.html', '<script type="module" src="/assets/index-a.js"></script>'
      + '<link rel="modulepreload" href="/assets/vendor-b.js"><link rel="stylesheet" href="/assets/index-d.css">');

    expect(measure(dir)).toEqual({
      initialJs: gz(entry) + gz(vendor),
      totalJs: gz(entry) + gz(vendor) + gz(lazy),
      totalCss: gz(css),
    });
  });

  it('fails on an empty assets directory', () => {
    write('index.html', '');
    expect(() => measure(dir)).toThrow(/no \.js\/\.css/);
  });

  it('fails when index.html references no JS', () => {
    write('assets/index-a.js', 'x');
    write('index.html', '<html></html>');
    expect(() => measure(dir)).toThrow(/references no assets/);
  });
});

describe('TestCheckBundleSizeCompare', () => {
  const budget = { tolerancePercent: 5, baseline: { initialJs: 1000, totalJs: 2000, totalCss: 100 } };

  it('passes at the baseline and exactly at the limit', () => {
    expect(compare({ initialJs: 1000, totalJs: 2000, totalCss: 100 }, budget).ok).toBe(true);
    expect(compare({ initialJs: 1050, totalJs: 2100, totalCss: 105 }, budget).ok).toBe(true);
  });

  it('passes when the bundle shrinks', () => {
    const { ok, lines } = compare({ initialJs: 500, totalJs: 1000, totalCss: 50 }, budget);
    expect(ok).toBe(true);
    expect(lines[0]).toContain('-50.0%');
  });

  it('fails one byte over the limit and names the metric', () => {
    const { ok, lines } = compare({ initialJs: 1000, totalJs: 2101, totalCss: 100 }, budget);
    expect(ok).toBe(false);
    expect(lines.filter((l) => l.startsWith('FAIL'))).toHaveLength(1);
    expect(lines[1]).toMatch(/^FAIL totalJs/);
  });
});
