package controller

import (
	"testing"
	"time"
)

func TestDone(t *testing.T) {
	r := Done()
	if r.Requeue {
		t.Errorf("Done() should not set Requeue, got %+v", r)
	}
	if r.RequeueAfter != 0 {
		t.Errorf("Done() should not set RequeueAfter, got %v", r.RequeueAfter)
	}
}

func TestRequeueWithPositiveDuration(t *testing.T) {
	r := Requeue(5 * time.Second)
	if r.Requeue {
		t.Errorf("Requeue(5s) should NOT set Requeue=true (RequeueAfter is set instead), got %+v", r)
	}
	if r.RequeueAfter != 5*time.Second {
		t.Errorf("Requeue(5s).RequeueAfter = %v, want 5s", r.RequeueAfter)
	}
}

func TestRequeueWithZeroDuration(t *testing.T) {
	r := Requeue(0)
	if !r.Requeue {
		t.Errorf("Requeue(0) should set Requeue=true, got %+v", r)
	}
}

func TestRequeueWithNegativeDuration(t *testing.T) {
	r := Requeue(-1 * time.Second)
	if !r.Requeue {
		t.Errorf("Requeue(-1s) should set Requeue=true, got %+v", r)
	}
}

func TestStop(t *testing.T) {
	r := Stop()
	if r.Requeue {
		t.Errorf("Stop() should not set Requeue, got %+v", r)
	}
	if r.RequeueAfter != 0 {
		t.Errorf("Stop() should not set RequeueAfter, got %v", r.RequeueAfter)
	}
}
