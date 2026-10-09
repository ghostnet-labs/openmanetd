# radiocap fixtures

These `iw list` / `iw phy` texts are **synthetic**. They were assembled from the
documented `iw` output format (`iw/info.c`) and the interface-combination
tables the upstream drivers register; they were **not captured from OpenMANET
hardware**. Replace them with real captures (`iw list > file`) once a qualified
board is on the bench.

| File | Models |
|------|--------|
| `mt7916_dbdc.txt` | MT7916 on `mt7915e`: DBDC exposed as two wiphys (phy0 2.4 GHz, phy1 5 GHz), each `#{ AP, mesh point } <= 16, total <= 19, #channels <= 1` as in `mt7915/init.c`. |
| `morse_halow.txt` | Morse Micro HaLow phy. The Morse driver maps S1G channels onto 5 GHz channel numbers for mac80211, so `iw` reports 5 GHz frequencies; callers mark the PHY HaLow from the driver type (`PHY.SetHaLow`). The combination line is illustrative, not taken from the vendor driver. |
| `ath9k_dualband_single_phy.txt` | One wiphy that lists two bands and two combinations (one `#channels <= 2`). Two band names on one wiphy are not two radios. |
