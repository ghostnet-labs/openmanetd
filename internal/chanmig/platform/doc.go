// Package platform provides the production adapters that connect a
// chanmig.Node to an OpenMANET node, and the Service that runs it.
//
//   - AlfredTransport carries chanmig messages over alfred gossip under
//     data type AlfredDataType (104). alfred is a per-source key/value
//     store, not a datagram bus, so each node publishes a bounded bundle
//     of its most recent messages and receivers deliver only entries they
//     have not seen before (see transport.go for the framing).
//   - FileStore persists accepted plans to a small file (by default
//     /etc/openmanetd/chanmig/plans.bin) with atomic temp-file + rename
//     writes and a CRC guard.
//   - UCIRadio applies an assignment by rewriting the channel and htmode
//     options of the band's wifi-device section in /etc/config/wireless
//     and reloading the wireless stack, the same path the Wi-Fi settings
//     RPC uses.
//   - SystemClock reads wall time and reports the kernel NTP discipline
//     state (adjtimex) as Synced.
//   - LoadPublicKeys and LoadPrivateKey read the plan-authority Ed25519
//     keys from files provisioned out of band. Every node needs the
//     public key; only coordinators hold the private key.
//
// This package must stay free of cgo so it builds for every firmware
// target; the alfred client binding lives with the daemon wiring.
package platform
