// =============================================================================
// returnPath.test.js — post-login return path validation (open redirects)
// =============================================================================

import { describe, it, expect } from 'vitest';
import { safeReturnPath, loginPathFor } from '../../utils/returnPath.js';

describe('TestSafeReturnPath', () => {
  it.each([
    ['/settings/wireless', '/settings/wireless'],
    ['/comms', '/comms'],
    ['/settings/logs?level=warn', '/settings/logs?level=warn'],
    ['/topology#node-3', '/topology#node-3'],
    ['/settings/../comms', '/comms'],
    ['/', '/'],
  ])('keeps same-origin SPA path %s', (raw, want) => {
    expect(safeReturnPath(raw)).toBe(want);
  });

  it.each([
    [null],
    [undefined],
    [''],
    [42],
    [{}],
  ])('falls back to / for non-string or empty input %s', (raw) => {
    expect(safeReturnPath(raw)).toBe('/');
  });

  it.each([
    ['//evil.example/'],
    ['//evil.example/settings'],
    ['/\\evil.example'],
    ['\\\\evil.example'],
    ['/\t/evil.example'],
    ['/\n/evil.example'],
    ['/settings\u0000'],
    ['https://evil.example/'],
    ['http:/evil.example'],
    ['javascript:alert(1)'],
    ['data:text/html,hi'],
    ['evil.example'],
    ['settings'],
    [' /settings'],
  ])('rejects open-redirect attempt %j', (raw) => {
    expect(safeReturnPath(raw)).toBe('/');
  });

  it.each([
    ['/login'],
    ['/login?next=/login'],
    ['/LOGIN'],
    ['/cgi-bin/luci/'],
    ['/luci-static/resources/luci.js'],
    ['/ubus'],
    ['/api/system/info'],
    ['/rpc/openmanet.gnss.v1.GNSSService/GetGNSSConfig'],
    ['/auth/logout'],
    ['/ws'],
  ])('rejects non-SPA path %s', (raw) => {
    expect(safeReturnPath(raw)).toBe('/');
  });

  it('keeps routes that merely start with a reserved word', () => {
    expect(safeReturnPath('/loginhelp')).toBe('/loginhelp');
    expect(safeReturnPath('/apiary')).toBe('/apiary');
  });

  it('rejects paths longer than 2048 characters', () => {
    expect(safeReturnPath('/' + 'a'.repeat(2048))).toBe('/');
    expect(safeReturnPath('/' + 'a'.repeat(2047))).toBe('/' + 'a'.repeat(2047));
  });

  it('does not decode an encoded protocol-relative path into one', () => {
    expect(safeReturnPath('/%2F%2Fevil.example')).toBe('/%2F%2Fevil.example');
  });
});

describe('TestLoginPathFor', () => {
  it('omits next for the dashboard', () => {
    expect(loginPathFor('/')).toBe('/login');
  });

  it('encodes a deep link into next', () => {
    expect(loginPathFor('/settings/wireless?tab=mesh')).toBe(
      '/login?next=%2Fsettings%2Fwireless%3Ftab%3Dmesh',
    );
  });

  it('drops an unsafe current path', () => {
    expect(loginPathFor('//evil.example')).toBe('/login');
    expect(loginPathFor('/login')).toBe('/login');
  });
});
