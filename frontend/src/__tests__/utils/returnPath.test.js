import { describe, it, expect } from 'vitest';
import { returnPathFrom } from '../../utils/returnPath.js';

describe('TestReturnPathFrom', () => {
  it.each([
    ['null state', null, '/'],
    ['undefined state', undefined, '/'],
    ['empty object', {}, '/'],
    ['from without pathname', { from: {} }, '/'],
    ['non-string pathname', { from: { pathname: 42 } }, '/'],
    ['empty pathname', { from: { pathname: '' } }, '/'],
    ['relative pathname', { from: { pathname: 'settings' } }, '/'],
    ['protocol-relative', { from: { pathname: '//evil.example/x' } }, '/'],
    ['backslash protocol-relative', { from: { pathname: '/\\evil.example' } }, '/'],
    ['login itself', { from: { pathname: '/login' } }, '/'],
    ['login subpath', { from: { pathname: '/login/x' } }, '/'],
    ['dashboard', { from: { pathname: '/' } }, '/'],
    ['deep settings page', { from: { pathname: '/settings/wireless' } }, '/settings/wireless'],
    ['loginx is not login', { from: { pathname: '/loginx' } }, '/loginx'],
    ['keeps search and hash', { from: { pathname: '/gps', search: '?a=1', hash: '#sky' } }, '/gps?a=1#sky'],
    ['ignores non-string search', { from: { pathname: '/gps', search: 5, hash: null } }, '/gps'],
  ])('%s', (_name, state, want) => {
    expect(returnPathFrom(state)).toBe(want);
  });
});
