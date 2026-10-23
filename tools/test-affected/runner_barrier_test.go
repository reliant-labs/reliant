package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"testing"
)

func TestRunnerAllPrerequisitesAreBarriers(t *testing.T) {
	pl := failSafe("", "", "fixture")
	pl.Lockfiles = []LockfileCheck{{Dir: "frontends/web", Lanes: []lane{laneWeb}}}
	enabled := laneSet("go,web,electron,forge-generate,docs-generate")
	var out bytes.Buffer
	r := &runner{root: t.TempDir(), out: &out, errOut: &out, jobs: 1}
	var names, exclusive []string
	for _, s := range r.steps(pl, enabled) {
		if s.exclusive {
			exclusive = append(exclusive, s.name)
		} else {
			names = append(names, s.name)
		}
	}
	names = append(names, exclusive...)
	for stop := 0; stop < 3; stop++ {
		var calls []string
		r.execute = func(_ context.Context, s step, _, _ io.Writer) error {
			calls = append(calls, s.name)
			if s.name == names[stop] {
				return errors.New("fixture prerequisite failure")
			}
			return nil
		}
		if code := r.run(context.Background(), pl, enabled); code != 1 {
			t.Fatalf("stop=%d exit=%d", stop, code)
		}
		if !reflect.DeepEqual(calls, names[:stop+1]) {
			t.Fatalf("stop=%d ran %v, want %v", stop, calls, names[:stop+1])
		}
	}
	var calls []string
	r.execute = func(_ context.Context, s step, _, _ io.Writer) error { calls = append(calls, s.name); return nil }
	if code := r.run(context.Background(), pl, enabled); code != 0 || !reflect.DeepEqual(calls, names) {
		t.Fatalf("serial run: exit=%d calls=%v want=%v", code, calls, names)
	}
	r.dry = true
	r.execute = func(context.Context, step, io.Writer, io.Writer) error {
		t.Fatal("dry run executed a step")
		return nil
	}
	if code := r.run(context.Background(), pl, enabled); code != 0 {
		t.Fatal(code)
	}
}
