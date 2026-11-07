// Package jamdetect flags jamming on a node's own radios.
//
// Every interval the detector reads each radio's channel survey (the
// `iw dev <if> survey dump` noise floor and channel active, busy, receive
// and transmit time counters, which both the MT7916 and the Morse Micro
// HaLow drivers report). Per radio it learns what the in-use channel
// normally looks like and marks a sample abnormal when:
//
//   - the noise floor rises well above its learned baseline, or above an
//     absolute ceiling; or
//   - busy time the radio cannot explain by its own receive and transmit
//     time rises well above its baseline, or above an absolute ceiling.
//
// Unexplained busy time is the key signal: a jammer keeps the channel's
// clear-channel assessment busy with energy no radio can decode, while a
// busy but healthy channel spends that time receiving frames.
//
// A short run of abnormal samples moves the radio to SUSPECTED, a longer
// run to JAMMED, and a run of normal samples back to CLEAR. Each
// SUSPECTED-or-worse run is one episode (Event) with a stable ID. The
// baseline is frozen while a radio reads abnormal so a parked jammer is
// never learned as normal. A channel change (channel migration moving the
// mesh away) closes any open episode and restarts learning.
//
// The Detector also publishes this node's report to the mesh and keeps
// the latest report of every other node (see Gossip), so any node can show
// the whole mesh's detections. Active returns only this node's own
// episodes; the ATAK feed places those at this node's GNSS fix.
package jamdetect
