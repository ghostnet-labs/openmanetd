package device

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
)

// Sentinel errors returned by SelectCM108 and Binder.Bind. All of them are
// transient from the caller's point of view: a device may be plugged in, or
// finish enumerating its hidraw / ALSA children, at any time, so callers
// retry with backoff rather than giving up.
var (
	// ErrNoCM108 means no CM108-family USB device is present.
	ErrNoCM108 = errors.New("device: no CM108-family USB device present")
	// ErrCM108Incomplete means at least one CM108-family device is present
	// but none has both a hidraw child and an ALSA card child yet (still
	// enumerating, or the audio/HID class driver is missing).
	ErrCM108Incomplete = errors.New("device: no CM108 device with both a hidraw node and an ALSA card")
	// ErrIdentityUnknown means no candidate was confirmed as an OpenVLM and
	// at least one GPIO1 identity probe failed. Falling back to a generic
	// CM108 in that state could bind the wrong device (the unprobed one may
	// be the real OpenVLM), so selection refuses until every probe answers.
	ErrIdentityUnknown = errors.New("device: OpenVLM identity probe failed; refusing generic CM108 fallback")
)

// NoCard is the ALSACardIdx / pin value meaning "no ALSA card".
const NoCard = -1

// Selection is one CM108-family USB device whose hidraw node and ALSA card
// were both resolved from the same USB parent directory (Device.SysPath),
// plus the evidence that led to choosing it. HID (PTT) and audio/mixer
// consumers that use the same Selection are guaranteed to talk to the same
// physical device.
type Selection struct {
	// Device is the chosen descriptor; Device.IsOpenVLM reports the GPIO1
	// strap result.
	Device CM108Descriptor
	// Candidates counts CM108-family devices seen in the scan.
	Candidates int
	// Complete counts candidates that had both a hidraw and an ALSA child.
	Complete int
	// Confirmed counts complete candidates whose GPIO1 strap read high.
	Confirmed int
	// ProbeErrors counts complete candidates whose identity probe failed.
	ProbeErrors int
	// Ambiguous is true when more than one candidate ranked equally; the
	// one with the lexically lowest USB sys path was chosen so the choice
	// is stable across rescans.
	Ambiguous bool
	// PinnedCard is true when the device was chosen because it owns the
	// operator's ALSA_CARD override.
	PinnedCard bool
}

// complete reports whether d has both children needed for a pairing.
func (d *CM108Descriptor) complete() bool {
	return d.HIDPath != "" && d.ALSACardIdx >= 0
}

// SelectCM108 picks one CM108-family device from descs. Ranking:
//
//  1. When pinCard >= 0 and a complete candidate owns ALSA card pinCard,
//     that candidate (operator override wins).
//  2. GPIO1-strapped OpenVLM devices (CheckOpenVLMIdentity == true).
//  3. Generic CM108 devices, only when every identity probe answered.
//
// Only candidates that expose both a hidraw node and an ALSA card are
// eligible, so the HID and audio halves always come from one USB parent.
// Ties inside a rank are broken by the lowest SysPath. probe may be nil
// for the real HIDIOCGINPUT reader. descs is not modified.
func SelectCM108(descs []CM108Descriptor, probe HIDInputReader, pinCard int) (Selection, error) {
	sel := Selection{Candidates: len(descs)}
	if len(descs) == 0 {
		return sel, ErrNoCM108
	}

	complete := make([]CM108Descriptor, 0, len(descs))

	for i := range descs {
		if descs[i].complete() {
			complete = append(complete, descs[i])
		}
	}

	sel.Complete = len(complete)
	if sel.Complete == 0 {
		return sel, ErrCM108Incomplete
	}

	slices.SortFunc(complete, func(a, b CM108Descriptor) int { return cmp.Compare(a.SysPath, b.SysPath) })

	firstConfirmed := -1

	for i := range complete {
		ok, err := CheckOpenVLMIdentity(complete[i], probe)
		if err != nil {
			sel.ProbeErrors++

			continue
		}

		complete[i].IsOpenVLM = ok

		if ok {
			sel.Confirmed++

			if firstConfirmed < 0 {
				firstConfirmed = i
			}
		}
	}

	if pinCard >= 0 {
		for i := range complete {
			if complete[i].ALSACardIdx == pinCard {
				sel.Device = complete[i]
				sel.PinnedCard = true

				return sel, nil
			}
		}
	}

	if firstConfirmed >= 0 {
		sel.Device = complete[firstConfirmed]
		sel.Ambiguous = sel.Confirmed > 1

		return sel, nil
	}

	if sel.ProbeErrors > 0 {
		return sel, fmt.Errorf("%w (%d of %d probes failed)", ErrIdentityUnknown, sel.ProbeErrors, sel.Complete)
	}

	sel.Device = complete[0]
	sel.Ambiguous = sel.Complete > 1

	return sel, nil
}
