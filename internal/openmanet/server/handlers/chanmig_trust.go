package handlers

import (
	"crypto/ed25519"
	"errors"
	"fmt"

	"github.com/openmanet/openmanetd/internal/chanmig/platform"
	"github.com/openmanet/openmanetd/internal/config"
)

// ChanmigTrustConfig is the config the channel-migration trust store
// reads and writes; *config.Config implements it.
type ChanmigTrustConfig interface {
	GetChanmig() config.ChanmigConfig
	PersistChanmigMeshIDIfUnset(meshID uint64) (bool, error)
}

// JoinTrust is the channel-migration trust a mesh join code carries: the
// plan-authority public keys and the chanmig mesh ID.
type JoinTrust struct {
	Keys      []ed25519.PublicKey
	MeshID    uint64
	HasMeshID bool
}

// JoinTrustResult reports what InstallJoinTrust changed.
type JoinTrustResult struct {
	// KeyFile is the configured public key file.
	KeyFile string
	// KeysWritten is true when the keys were written to KeyFile; false
	// when a key file already existed (it is never overwritten).
	KeysWritten bool
	// MeshIDSet is true when the mesh ID was persisted to the config.
	MeshIDSet bool
}

// ChanmigTrust shares and installs the channel-migration trust carried by
// mesh join codes.
type ChanmigTrust interface {
	// JoinTrust returns the trust to share; ok is false when channel
	// migration is disabled on this node.
	JoinTrust() (trust JoinTrust, ok bool, err error)
	// InstallJoinTrust adopts received trust: keys only into a missing
	// public key file, the mesh ID only when none is configured.
	InstallJoinTrust(t JoinTrust) (JoinTrustResult, error)
}

// ConfigChanmigTrust implements ChanmigTrust over the daemon config and
// the configured key file.
type ConfigChanmigTrust struct {
	Cfg ChanmigTrustConfig
}

// JoinTrust reads the trusted keys from chanmig.publicKeyFile when
// chanmig.enable is true.
func (c *ConfigChanmigTrust) JoinTrust() (JoinTrust, bool, error) {
	cc := c.Cfg.GetChanmig()
	if !cc.Enable {
		return JoinTrust{}, false, nil
	}

	keys, err := platform.LoadPublicKeys(cc.PublicKeyFile)
	if err != nil {
		return JoinTrust{}, false, fmt.Errorf("load plan-authority public keys: %w", err)
	}

	return JoinTrust{Keys: keys, MeshID: cc.MeshID, HasMeshID: true}, true, nil
}

// InstallJoinTrust writes t.Keys to chanmig.publicKeyFile when no file is
// there yet, and persists t.MeshID when the config has no mesh ID. Trust
// without keys changes nothing. Neither takes effect on a running chanmig
// node until the daemon restarts.
func (c *ConfigChanmigTrust) InstallJoinTrust(t JoinTrust) (JoinTrustResult, error) {
	if len(t.Keys) == 0 {
		return JoinTrustResult{}, nil
	}

	cc := c.Cfg.GetChanmig()
	res := JoinTrustResult{KeyFile: cc.PublicKeyFile}

	err := platform.InstallPublicKeys(cc.PublicKeyFile, t.Keys)

	switch {
	case err == nil:
		res.KeysWritten = true
	case errors.Is(err, platform.ErrKeyFileExists):
	default:
		return res, fmt.Errorf("install plan-authority public keys: %w", err)
	}

	if !t.HasMeshID {
		return res, nil
	}

	set, err := c.Cfg.PersistChanmigMeshIDIfUnset(t.MeshID)
	if err != nil {
		return res, fmt.Errorf("persist chanmig mesh id: %w", err)
	}

	res.MeshIDSet = set

	return res, nil
}
