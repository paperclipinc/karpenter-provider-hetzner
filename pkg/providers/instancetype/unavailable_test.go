package instancetype

import (
	"testing"
	"time"
)

func TestUnavailableCache(t *testing.T) {
	now := time.Now()
	c := newUnavailableCache(10 * time.Minute)
	c.nowFn = func() time.Time { return now }

	if c.isUnavailable("cx22", "nbg1") {
		t.Fatal("nothing marked yet")
	}
	c.markUnavailable("cx22", "nbg1")
	if !c.isUnavailable("cx22", "nbg1") {
		t.Fatal("should be unavailable after marking")
	}
	if c.isUnavailable("cx22", "fsn1") {
		t.Fatal("other location must be unaffected")
	}

	now = now.Add(11 * time.Minute) // expire
	if c.isUnavailable("cx22", "nbg1") {
		t.Fatal("entry should have expired")
	}
}
