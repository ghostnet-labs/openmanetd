// =============================================================================
// pcmPlayer.test.js — WebAudio scheduling of scanner PCM frames (GHO-53)
// =============================================================================

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { createPcmPlayer } from '../../services/pcmPlayer.js';

function createMockAudioContext() {
  const started = [];
  const Ctor = vi.fn(function MockAudioContext() {
    this.state = 'suspended';
    this.currentTime = 10;
    this.destination = {};
    this.resume = vi.fn(() => Promise.resolve());
    this.close = vi.fn(() => Promise.resolve());
    this.createBuffer = vi.fn((channels, length, rate) => {
      const data = new Float32Array(length);
      return { channels, length, rate, getChannelData: () => data };
    });
    this.createBufferSource = vi.fn(() => {
      const src = { connect: vi.fn(), start: vi.fn((t) => started.push({ t, buffer: src.buffer })) };
      return src;
    });
  });
  return { Ctor, started };
}

// frame returns n little-endian int16 samples of value v.
function frame(n, v) {
  const b = new Uint8Array(n * 2);
  const view = new DataView(b.buffer);
  for (let i = 0; i < n; i++) view.setInt16(i * 2, v, true);
  return b;
}

describe('TestPcmPlayer', () => {
  let mock;

  beforeEach(() => {
    mock = createMockAudioContext();
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it('resumes a suspended context and decodes s16le to float', () => {
    const p = createPcmPlayer({ AudioContextCtor: mock.Ctor });
    const ctx = mock.Ctor.mock.instances[0];
    expect(ctx.resume).toHaveBeenCalled();

    p.push(frame(160, -16384));
    expect(ctx.createBuffer).toHaveBeenCalledWith(1, 160, 8000);
    expect(mock.started).toHaveLength(1);
    expect(mock.started[0].buffer.getChannelData(0)[0]).toBe(-0.5);
  });

  it('schedules frames back to back after a short lead', () => {
    const p = createPcmPlayer({ AudioContextCtor: mock.Ctor });
    p.push(frame(160, 1));
    p.push(frame(160, 1));
    expect(mock.started[0].t).toBeCloseTo(10.12, 6);
    expect(mock.started[1].t).toBeCloseTo(10.14, 6);
  });

  it('restarts after a squelch gap and caps queued audio', () => {
    const p = createPcmPlayer({ AudioContextCtor: mock.Ctor });
    const ctx = mock.Ctor.mock.instances[0];
    p.push(frame(160, 1));
    ctx.currentTime = 20; // gap: the schedule fell behind
    p.push(frame(160, 1));
    expect(mock.started[1].t).toBeCloseTo(20.12, 6);

    for (let i = 0; i < 40; i++) p.push(frame(160, 1));
    expect(p.dropped()).toBeGreaterThan(0);
    const last = mock.started[mock.started.length - 1].t;
    expect(last - ctx.currentTime).toBeLessThanOrEqual(0.6 + 1e-9);
  });

  it('ignores empty frames and pushes after close', () => {
    const p = createPcmPlayer({ AudioContextCtor: mock.Ctor });
    const ctx = mock.Ctor.mock.instances[0];
    p.push(new Uint8Array(0));
    p.push(null);
    p.close();
    p.close();
    p.push(frame(160, 1));
    expect(mock.started).toHaveLength(0);
    expect(ctx.close).toHaveBeenCalledTimes(1);
  });

  it('honours a byte offset into a larger buffer', () => {
    const p = createPcmPlayer({ AudioContextCtor: mock.Ctor });
    const big = new Uint8Array(6);
    new DataView(big.buffer).setInt16(2, 16384, true);
    p.push(big.subarray(2, 4));
    expect(mock.started[0].buffer.getChannelData(0)[0]).toBe(0.5);
  });

  it('throws without WebAudio', () => {
    vi.stubGlobal('AudioContext', undefined);
    vi.stubGlobal('webkitAudioContext', undefined);
    expect(() => createPcmPlayer()).toThrow(/WebAudio/);
  });
});
