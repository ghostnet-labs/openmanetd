// =============================================================================
// pcmPlayer.js — WebAudio playback of 8 kHz s16le PCM frames
// =============================================================================
//
// Plays the scanner's squelch-open audio: each frame becomes an AudioBuffer
// scheduled back to back on one AudioContext. Frames only arrive while the
// squelch is open, so a gap simply lets the schedule fall behind real time
// and the next frame restarts after a short lead. Queued audio is capped so
// a stalled tab never builds up seconds of latency.
//
// createPcmPlayer must be called from a user gesture (the listen button) so
// the AudioContext is allowed to start.

const LEAD_S = 0.12; // jitter allowance before the first frame of a burst
const MAX_QUEUE_S = 0.6; // drop frames beyond this much scheduled audio

export function createPcmPlayer({ sampleRate = 8000, AudioContextCtor } = {}) {
  const Ctor = AudioContextCtor ?? globalThis.AudioContext ?? globalThis.webkitAudioContext;
  if (!Ctor) throw new Error('WebAudio not supported in this browser');

  const ctx = new Ctor();
  let next = 0;
  let closed = false;
  let dropped = 0;

  if (ctx.state === 'suspended' && ctx.resume) ctx.resume().catch(() => {});

  // push schedules one frame. data is little-endian int16 PCM (Uint8Array).
  function push(data) {
    if (closed || !data || data.byteLength < 2) return;
    const n = data.byteLength >> 1;
    const now = ctx.currentTime;
    if (next < now) next = now + LEAD_S;
    if (next - now > MAX_QUEUE_S) {
      dropped++;
      return;
    }

    const buf = ctx.createBuffer(1, n, sampleRate);
    const out = buf.getChannelData(0);
    const view = new DataView(data.buffer, data.byteOffset, n * 2);
    for (let i = 0; i < n; i++) out[i] = view.getInt16(i * 2, true) / 32768;

    const src = ctx.createBufferSource();
    src.buffer = buf;
    src.connect(ctx.destination);
    src.start(next);
    next += n / sampleRate;
  }

  function close() {
    if (closed) return;
    closed = true;
    ctx.close?.().catch(() => {});
  }

  return { push, close, dropped: () => dropped };
}
