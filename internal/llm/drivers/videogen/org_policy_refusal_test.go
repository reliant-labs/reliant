// Copyright (c) 2025 Reliant Labs
package videogen

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/orgpolicy"
)

// The gateway's org policy refusal (403 + x-forge-error-reason,
// control-plane docs/design/org-policies.md §5.1) is a verdict: Submit makes
// one attempt and reports KindPolicyRefused carrying the refusal, and a Poll
// refused (a spend cap reached mid-render) ends the wait at once, keeping
// the job so the render can be fetched once the policy allows it.

func refuse(reason string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Forge-Error-Reason", reason)
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"the organization's AI spend cap is reached","code":"` + reason + `"}}`))
	}
}

func TestManagedSubmit_PolicyRefusalIsAVerdictNotRetried(t *testing.T) {
	attempts := 0
	client := newManagedForTest(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		refuse(orgpolicy.ReasonOrgSpendCapReached)(w, r)
	})
	client.config.RetryBaseDelay = time.Millisecond

	_, err := client.Submit(context.Background(), Request{Prompt: "x"})

	var classified *Error
	if !errors.As(err, &classified) || classified.Kind != KindPolicyRefused {
		t.Fatalf("err = %v, want KindPolicyRefused", err)
	}
	refusal, ok := orgpolicy.RefusalOf(err)
	if !ok || refusal.Reason != orgpolicy.ReasonOrgSpendCapReached {
		t.Fatalf("refusal = %+v, %v", refusal, ok)
	}
	if attempts != 1 {
		t.Errorf("a policy refusal was retried %d times", attempts-1)
	}
}

func TestManagedPoll_PolicyRefusalEndsTheWaitAndKeepsTheJob(t *testing.T) {
	polls := 0
	client := newManagedForTest(t, func(w http.ResponseWriter, r *http.Request) {
		polls++
		refuse(orgpolicy.ReasonMemberSpendCapReached)(w, r)
	})
	job := Job{Provider: managedDriverID, ID: testVideoID}

	_, err := Wait(context.Background(), client, job, WaitOptions{Interval: time.Millisecond, Deadline: time.Second})

	var classified *Error
	if !errors.As(err, &classified) || classified.Kind != KindPolicyRefused {
		t.Fatalf("err = %v, want KindPolicyRefused", err)
	}
	if classified.Job.ID != testVideoID {
		t.Errorf("job = %+v, want the render kept for a later fetch", classified.Job)
	}
	if !orgpolicy.IsRefusal(err) {
		t.Error("the refusal is recoverable from the error")
	}
	if polls != 1 {
		t.Errorf("polled %d times; a policy refusal is not a transient poll failure", polls)
	}
}

func TestManagedSubmit_A403WithoutAPolicyReasonIsNotARefusal(t *testing.T) {
	client := newManagedForTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"forbidden"}}`))
	})
	_, err := client.Submit(context.Background(), Request{Prompt: "x"})
	if orgpolicy.IsRefusal(err) {
		t.Fatalf("a 403 naming no policy reason is not the policy's: %v", err)
	}
}
