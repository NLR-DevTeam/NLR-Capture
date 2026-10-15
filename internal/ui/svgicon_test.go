package ui

import (
	"math"
	"testing"
)

func TestIconsParse(t *testing.T) {
	all := []*Icon{icCheck, icClose, icFile, icDoc, icLink, icRefresh, icImport, icExport, icDelete, icSearch,
		icSettings, icEdit, icCopy, icPaste, icMore, icLock, icLockOpen, icInfo, icError, icWarn, icDone, icSave,
		icPlay, icOpen, icReplay, icSend, icPower, icCut, icRecord, icBlock, icFilter, icRules, icPause, icImage,
		icMedia, icWasm, icGlobe, icWS, icTunnel, icFetch, icStyle, icNIC, icProxy, icWrap, icTimer, icHelp, icCert, icMark}
	for i, ic := range all {
		if len(ic.stroke)+len(ic.fill) == 0 {
			t.Fatalf("icon %d empty", i)
		}
		for _, s := range append(append([]seg{}, ic.stroke...), ic.fill...) {
			for _, p := range s.pts {
				if math.IsNaN(float64(p.X)) || p.X < -1 || p.X > 25 || p.Y < -1 || p.Y > 25 {
					t.Fatalf("icon %d point out of range: %v", i, p)
				}
			}
		}
	}
}
