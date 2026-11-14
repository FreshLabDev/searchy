package main

import "testing"

func TestSummaryCountsFailuresAndUsesNearestRankP95(t *testing.T) {
	rows := make([]caseReport, 21)
	for i := range 20 {
		rows[i].Candidate = &runResult{Count: 24, ElapsedMillis: int64(i + 1), Degraded: i == 0}
	}
	got := summarize(rows, false)
	if got.Cases != 20 || got.Failed != 1 || got.Degraded != 1 || got.Empty != 0 ||
		got.MedianMillis != 10.5 || got.P95Millis != 19 || got.MaxMillis != 20 {
		t.Fatalf("unexpected summary: %+v", got)
	}
}
