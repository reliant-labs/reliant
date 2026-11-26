package kubedial

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
)

// DialPod goes straight to the named pod's port: no Service, no endpoint
// lookup, which is what a loopback debug listener needs.
func TestDialPodGoesStraightToThePod(t *testing.T) {
	pods := &fakePods{serve: func(string, string, int) (io.ReadWriteCloser, error) {
		c, s := pipeStream()
		go func() { _, _ = io.Copy(s, s) }()
		return c, nil
	}}
	c := NewWithClientset(fake.NewClientset(), pods)
	conn, err := c.DialPod(context.Background(), "ns", "api-7c9", 6060)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if got := pods.got(); len(got) != 1 || got[0] != "ns/api-7c9:6060" {
		t.Fatalf("forwarded to %v, want ns/api-7c9:6060", got)
	}
	if conn.RemoteAddr().String() != "ns/api-7c9:6060" {
		t.Errorf("remote %s", conn.RemoteAddr())
	}
	if _, err := conn.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "ok" {
		t.Fatalf("echo %q %v", buf, err)
	}
}

func TestDialPodErrors(t *testing.T) {
	refused := &fakePods{serve: func(string, string, int) (io.ReadWriteCloser, error) {
		return nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "api-1", errors.New("no"))
	}}
	c := NewWithClientset(fake.NewClientset(), refused)
	if _, err := c.DialPod(context.Background(), "", "api-1", 6060); err == nil {
		t.Error("a dial with no namespace succeeded")
	}
	if _, err := c.DialPod(context.Background(), "ns", "api-1", 0); err == nil {
		t.Error("a dial with no port succeeded")
	}
	_, err := c.DialPod(context.Background(), "ns", "api-1", 6060)
	if err == nil || !strings.Contains(err.Error(), "pods/portforward") || !strings.Contains(err.Error(), "ns/api-1:6060") {
		t.Errorf("forbidden dial: %v, want the pod and the rule it needs", err)
	}
}
