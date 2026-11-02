package shell

import (
	"sync"
	"testing"
	"time"
)

// TestLiveReadsWhatRefreshPortsWritesUnderItsLock: the process listers
// (daemon.LocalClient.ListProcesses, the daemon's exec.bg_list) read Status,
// ExitCode, EndTime and Ports through Live while refreshPorts and the
// completion and kill paths write them under outputMu. Reading them bare was a
// data race the release gate's -race suite caught (v1.7.88-rc.7: a shell_wait
// listing processes while a kill refreshed the same process's ports). Under
// -race this fails if Live stops taking the lock.
func TestLiveReadsWhatRefreshPortsWritesUnderItsLock(t *testing.T) {
	t.Parallel()
	p := &BackgroundProcess{ID: "p", Status: "running", Ports: []PortInfo{{Port: 3000}}}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wrote := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			if i == 1 {
				close(wrote)
			}
			select {
			case <-stop:
				return
			default:
			}
			end := time.Now()
			p.outputMu.Lock()
			p.Ports = []PortInfo{{Port: 3000 + i%10}}
			p.EndTime = &end
			p.outputMu.Unlock()
		}
	}()
	<-wrote // the writer is running: the reads below overlap its writes
	for range 2000 {
		live := p.Live()
		if len(live.Ports) > 0 {
			live.Ports[0].Port = 0 // a copy: the caller never aliases the process's slice
		}
	}
	close(stop)
	wg.Wait()
	if got := p.Live().Ports[0].Port; got == 0 {
		t.Fatalf("Live returned the process's own Ports slice: a caller's write reached it")
	}
}
