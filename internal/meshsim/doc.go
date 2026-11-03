// Package meshsim runs several virtual OpenMANET nodes on one Linux host so
// openmanetd's batman-adv code can be tested against a real mesh without
// radios.
//
// Each node is a network namespace holding a real batman-adv mesh interface
// (bat0), a dummy interface whose MAC is the node's stable originator
// address, and veth pairs standing in for radio links. Tests drive the mesh
// (link up/down, node join/leave, gateway mode) and then call openmanetd's
// own batctl-backed providers from inside a node's namespace, so the code
// under test parses live kernel output rather than fixtures.
//
// Everything except this file is behind the meshsim build tag because the
// harness needs root and the batman_adv kernel module:
//
//	go test -c -tags meshsim -o meshsim.test ./internal/meshsim
//	sudo MESHSIM_REQUIRE=1 ./meshsim.test -test.v
//
// Without root or batman-adv the tests skip, unless MESHSIM_REQUIRE=1 is set,
// in which case they fail so CI cannot silently pass without running them.
package meshsim
