package search

import "testing"

func TestPostprocessInterleavesAndDedupes(t *testing.T) {
	a := &Aggregator{maxResults: 10}
	in := []MediaResult{
		{ID: "a1", Engine: "bing"},
		{ID: "a2", Engine: "bing"},
		{ID: "a3", Engine: "bing"},
		{ID: "a2", Engine: "bing"}, // duplicate ID -> dropped
		{ID: "u1", Engine: "unsplash"},
		{ID: "f1", Engine: "flickr"},
	}
	out := a.postprocess(in)

	// Dedup: 5 unique.
	if len(out) != 5 {
		t.Fatalf("expected 5 results after dedup, got %d", len(out))
	}
	// Round-robin: first three should be one-per-engine (bing, unsplash, flickr),
	// not three bings in a row.
	if out[0].Engine != "bing" || out[1].Engine != "unsplash" || out[2].Engine != "flickr" {
		t.Errorf("not interleaved: got %s,%s,%s", out[0].Engine, out[1].Engine, out[2].Engine)
	}
	// Remaining bing results come after the first round.
	if out[3].Engine != "bing" || out[4].Engine != "bing" {
		t.Errorf("expected trailing bing results, got %s,%s", out[3].Engine, out[4].Engine)
	}
}

func TestPostprocessCaps(t *testing.T) {
	a := &Aggregator{maxResults: 3}
	in := []MediaResult{
		{ID: "1", Engine: "x"}, {ID: "2", Engine: "x"},
		{ID: "3", Engine: "y"}, {ID: "4", Engine: "y"},
	}
	if got := a.postprocess(in); len(got) != 3 {
		t.Fatalf("expected cap at 3, got %d", len(got))
	}
}
