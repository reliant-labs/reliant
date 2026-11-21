package credentials

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func TestConcurrentStoresPreserveEveryNamespace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	const writers = 24
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := Store(path, "https://cp.example", fmt.Sprintf("client-%d", i), Credential{Token: fmt.Sprintf("token-%d", i)}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	for i := 0; i < writers; i++ {
		got, err := Lookup(path, "https://cp.example", fmt.Sprintf("client-%d", i))
		if err != nil || got.Token != fmt.Sprintf("token-%d", i) {
			t.Errorf("writer %d lost: %v", i, err)
		}
	}
}

func TestRemoveRevokedNeverDeletesNewSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := Store(path, "https://cp.example", "human", Credential{Token: "B"}); err != nil {
		t.Fatal(err)
	}
	if removed, err := RemoveRevoked(path, "https://cp.example", "human", "A"); err == nil || removed {
		t.Fatalf("removed unrevoked B: %v %v", removed, err)
	}
	if got, err := Lookup(path, "https://cp.example", "human"); err != nil || got.Token != "B" {
		t.Fatal("B lost")
	}
	if removed, err := RemoveRevoked(path, "https://cp.example", "human", "B"); err != nil || !removed {
		t.Fatalf("exact revoked session retained: %v %v", removed, err)
	}
}
