package deletions

import "testing"

func TestTrackerCountsOverlappingDeletions(t *testing.T) {
	var tracker Tracker
	first := tracker.Begin("a")
	second := tracker.Begin("a")
	if !tracker.Active("a") || tracker.Active("b") {
		t.Fatal("only a is being deleted")
	}
	first()
	first()
	if !tracker.Active("a") {
		t.Fatal("the second deletion of a is still running")
	}
	second()
	if tracker.Active("a") {
		t.Fatal("no deletion of a is left")
	}
	var none *Tracker
	none.Begin("a")()
	if none.Active("a") {
		t.Fatal("a nil tracker tracks nothing")
	}
}
