package jamdetect

import (
	"bufio"
	"bytes"
	"strconv"
	"strings"
)

// Survey is one channel entry of `iw dev <if> survey dump`. A counter is
// valid only when its Has flag is set; drivers omit the ones they do not
// keep.
type Survey struct {
	FreqMHz    float64
	ActiveMs   uint64
	BusyMs     uint64
	RxMs       uint64
	TxMs       uint64
	NoiseDBm   int
	InUse      bool
	HasNoise   bool
	HasActive  bool
	HasBusy    bool
	HasRx      bool
	HasTx      bool
	hasFreqVal bool
}

// ParseSurvey parses `iw dev <if> survey dump` output. Entries without a
// frequency are dropped. Unknown lines are ignored so newer iw releases
// that add fields keep parsing.
func ParseSurvey(out []byte) []Survey {
	surveys := make([]Survey, 0, 4)

	var cur *Survey

	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "Survey data from") {
			surveys = append(surveys, Survey{})
			cur = &surveys[len(surveys)-1]

			continue
		}

		if cur == nil {
			continue
		}

		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}

		parseSurveyField(cur, strings.TrimSpace(key), strings.TrimSpace(val))
	}

	kept := surveys[:0]

	for i := range surveys {
		if surveys[i].hasFreqVal {
			kept = append(kept, surveys[i])
		}
	}

	return kept
}

func parseSurveyField(s *Survey, key, val string) {
	switch key {
	case "frequency":
		f, rest := firstField(val)

		mhz, err := strconv.ParseFloat(f, 64)
		if err != nil {
			return
		}

		s.FreqMHz = mhz
		s.hasFreqVal = true
		s.InUse = strings.Contains(rest, "[in use]")
	case "noise":
		f, _ := firstField(val)

		n, err := strconv.Atoi(f)
		if err != nil {
			return
		}

		s.NoiseDBm = n
		s.HasNoise = true
	case "channel active time":
		s.ActiveMs, s.HasActive = parseMs(val)
	case "channel busy time":
		s.BusyMs, s.HasBusy = parseMs(val)
	case "channel receive time":
		s.RxMs, s.HasRx = parseMs(val)
	case "channel transmit time":
		s.TxMs, s.HasTx = parseMs(val)
	}
}

func firstField(val string) (first, rest string) {
	first, rest, _ = strings.Cut(val, " ")

	return first, rest
}

func parseMs(val string) (uint64, bool) {
	f, _ := firstField(val)

	n, err := strconv.ParseUint(f, 10, 64)
	if err != nil {
		return 0, false
	}

	return n, true
}

// InUse returns the entry for the channel the radio is on, or false when
// the driver marked none.
func InUse(surveys []Survey) (Survey, bool) {
	for i := range surveys {
		if surveys[i].InUse {
			return surveys[i], true
		}
	}

	return Survey{}, false
}
