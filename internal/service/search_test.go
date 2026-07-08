package service

import (
	"testing"
)

func TestRRFScore_RankBased(t *testing.T) {
	k := 60.0

	topInBoth := rrfScore(k, 1, 1)
	midInBoth := rrfScore(k, 5, 5)
	highInOne := rrfScore(k, 1, 100)
	goodInBoth := rrfScore(k, 50, 1)
	absent := rrfScore(k, 1, 0)

	if topInBoth <= midInBoth {
		t.Errorf("RRF #1 in both (%f) should outrank #5 in both (%f)", topInBoth, midInBoth)
	}

	if highInOne >= midInBoth {
		t.Errorf("RRF #1+#100 (%f) should NOT outrank #5+#5 (%f) — being good in both beats great in one", highInOne, midInBoth)
	}

	if goodInBoth <= absent {
		t.Errorf("RRF (#50,#1) (%f) should outrank (#1,absent) (%f)", goodInBoth, absent)
	}
}
