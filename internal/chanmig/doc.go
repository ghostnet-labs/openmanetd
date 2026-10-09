// Package chanmig implements authenticated mesh channel migration and
// absent-node rediscovery for OpenMANET nodes.
//
// A channel plan assigns a channel and width to each radio band (HaLow,
// 2.4 GHz, 5 GHz). Plans are versioned, signed with Ed25519 by a holder of
// the mesh plan-authority key, and carry an activation time. Nodes flood
// plans as adverts, acknowledge pending plans, switch band by band at the
// activation time, verify that peers appear on each changed band, and fall
// back to the previous assignment when they do not. A node that missed one
// or more plans, or cold-booted, rediscovers the mesh with a bounded search
// over its last-known plans, the provisioned rendezvous channels and the
// provisioned allowed-channel list, and learns the current plan from the
// first authenticated advert it hears.
//
// Competing plans from separated partitions resolve deterministically: the
// higher version wins, then the higher signer ID, then the larger canonical
// encoding.
//
// The package is transport- and radio-agnostic. Callers inject a Clock, a
// Transport, a RadioSetter and a Store; no radio is actuated and no RPC is
// exposed here. The design is recorded in the ghostnet-labs docs repository
// at project/software/channel-migration.md.
package chanmig
