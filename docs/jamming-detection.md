# Jamming detection

Every node (handheld or mast) watches its own radios for jamming (GHO-93).
It is on by default, read-only, and needs no extra hardware.

## How it works

Every 5 s the daemon reads each radio's channel survey with
`iw dev <if> survey dump`. Both the MT7916 Wi-Fi driver and the Morse Micro
HaLow driver report the in-use channel's noise floor and four counters:
active, busy, receive and transmit time.

For each radio it learns, over the first minute on a channel, what normal
looks like:

- the **noise floor** (dBm), and
- **unexplained busy time**: the share of time the channel was busy that
  the radio did not spend receiving or sending frames.

Unexplained busy time is the main signal. A jammer keeps the channel busy
with energy no radio can decode. A crowded but healthy channel is busy
too, but the radio spends that time receiving real frames, so it does not
trip detection.

A sample is abnormal when the noise floor is 10 dB or more above normal, or
unexplained busy time is 30 points or more above normal (and at least 40 %).
Two absolute ceilings (noise at or above -70 dBm, unexplained busy at or
above 85 %) catch a jammer that was already on when the node booted.

| State | When |
|---|---|
| Learning | First 12 samples (one minute) on a channel |
| Clear | Channel reads normal |
| Suspected | 2 abnormal samples in a row (10 s) |
| Jammed | 4 abnormal samples in a row (20 s). This is the alert |
| Clear again | 6 normal samples in a row (30 s) |

The learned baseline is frozen while a radio reads abnormal, so a jammer
that parks on a channel is never learned as normal. A channel change (for
example channel migration moving the mesh) ends the episode and restarts
learning on the new channel.

## Where it shows up

- **Dashboard**: a red `JAMMING · HALOW 915.5 MHz` alert (yellow for
  suspected), including other nodes' detections by hostname, and a
  *Jamming · Radios* panel with each radio's noise and busy readings.
- **ATAK**: the ATAK feed (GHO-94) puts a marker for this node's jammed
  radios at the node's GNSS fix.
- **Mesh**: every node publishes its report over alfred (data type 105)
  every 10 s, so any node shows the whole mesh. A node that stops
  publishing drops off after 2 minutes.
- **Logs**: `JAMMING DETECTED` at warn level, `jamming suspected` and
  `jamming cleared` at info, with radio, frequency, noise and reasons.
- **API**: `JamDetectService.GetJamStatus` and `StreamJamEvents`
  (session required).
- **Instrumentation**: the `jamdetect` snapshot section (see
  `instrumentation-snapshot.md`).

## Tuning

All thresholds are in the `jamdetect` section of
`/etc/openmanetd/config.yml`; see `example_config.yml`. Set
`jamdetect.enable: false` to turn detection off, or `jamdetect.gossip: false`
to keep detections on the node.

## Limits

- It detects jamming on the channels the radios are using. It does not scan
  other channels and cannot locate a jammer beyond "near the nodes that hear
  it". A listen-only SDR (GHO-53) is the planned add for direction finding.
- Detection depends on the driver's survey counters. A radio whose driver
  does not support `iw survey dump` is not monitored
  (`survey_errors_total` rises in the snapshot).
- The default thresholds are a starting point and have not been tuned
  against a real jammer yet.
