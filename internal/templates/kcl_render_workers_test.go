package templates

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/internal/kclrender"
)

// The KCL module corpus renders ~340 fixtures, and kclplugin.Serialized lets
// one native evaluation run per process (the runtime's panic hook is
// process-global), so t.Parallel() subtests in ONE process queue on that lock
// and the corpus costs the sum of its renders. A render is CPU-bound native
// code, so the only way to run two at once is a second process.
//
// runKCL therefore hands each render to a small pool of long-lived worker
// processes: this same test binary, re-executed on TestKCLRenderWorker, which
// reads requests from stdin and answers on stdout. The fixtures, directives
// and assertions stay exactly as they were; only where kclrender.Run executes
// moved. Same shape as internal/tierguard's render children.

const (
	kclWorkerEnv     = "FORGE_KCL_RENDER_WORKER"
	kclWorkersEnv    = "FORGE_KCL_TEST_WORKERS"
	kclMaxWorkersCap = 4
)

type kclRenderRequest struct {
	WorkDir string
	Entry   string
	DArgs   []string
}

type kclRenderResponse struct {
	Out []byte
	Err string
}

// TestKCLRenderWorker is the worker loop. It is a no-op in an ordinary run and
// serves renders when the pool re-executes the binary with kclWorkerEnv set.
func TestKCLRenderWorker(t *testing.T) {
	if os.Getenv(kclWorkerEnv) == "" {
		t.Skip("render worker body; started by the KCL module tests, not run directly")
	}
	kclplugin.Register()
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for {
		var req kclRenderRequest
		if err := dec.Decode(&req); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			t.Fatalf("decode request: %v", err)
		}
		out, err := kclrender.Run(req.WorkDir, req.Entry, req.DArgs)
		resp := kclRenderResponse{Out: out}
		if err != nil {
			resp.Err = err.Error()
		}
		if err := enc.Encode(resp); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}
}

type kclWorker struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	enc    *json.Encoder
	dec    *json.Decoder
	stderr *bytes.Buffer
}

func startKCLWorker() (*kclWorker, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate the test binary: %w", err)
	}
	cmd := exec.Command(self, "-test.run=^TestKCLRenderWorker$", "-test.count=1", "-test.timeout=0")
	cmd.Env = append(os.Environ(), kclWorkerEnv+"=1", kclCacheEnvEntry())
	w := &kclWorker{cmd: cmd, stderr: &bytes.Buffer{}}
	cmd.Stderr = w.stderr
	if w.stdin, err = cmd.StdinPipe(); err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start render worker: %w", err)
	}
	w.enc, w.dec = json.NewEncoder(w.stdin), json.NewDecoder(stdout)
	return w, nil
}

// kclCacheEnvEntry points a worker at the parent's module cache. The directory
// is content-addressed and materialized by atomic rename, so sharing it needs
// no coordination, and no worker writes into a developer's real cache.
func kclCacheEnvEntry() string {
	return "FORGE_KCL_MODULE_CACHE=" + kclCacheDir
}

func (w *kclWorker) render(req kclRenderRequest) (kclRenderResponse, error) {
	var resp kclRenderResponse
	if err := w.enc.Encode(req); err != nil {
		return resp, fmt.Errorf("send to render worker: %w\n%s", err, w.stderr.String())
	}
	if err := w.dec.Decode(&resp); err != nil {
		return resp, fmt.Errorf("render worker died rendering %s: %w\n%s", req.Entry, err, w.stderr.String())
	}
	return resp, nil
}

func (w *kclWorker) stop() {
	_ = w.stdin.Close()
	_ = w.cmd.Wait()
}

var kclPool struct {
	once    sync.Once
	workers chan *kclWorker
	all     sync.Mutex
	started []*kclWorker
}

func kclWorkerCount() int {
	if v, err := strconv.Atoi(os.Getenv(kclWorkersEnv)); err == nil && v > 0 {
		return v
	}
	return min(runtime.NumCPU(), kclMaxWorkersCap)
}

func kclPoolInit(t *testing.T) {
	t.Helper()
	kclPool.once.Do(func() {
		n := kclWorkerCount()
		kclPool.workers = make(chan *kclWorker, n)
		for range n {
			// Started lazily on first use: a worker costs a process, and a
			// run that renders three fixtures should not pay for four.
			kclPool.workers <- nil
		}
	})
}

// runKCLInWorker renders one file in a pooled worker process. A worker that
// died is replaced, so one bad render cannot starve the rest of the corpus.
func runKCLInWorker(t *testing.T, workDir, entry string, dArgs []string) ([]byte, error) {
	t.Helper()
	kclPoolInit(t)
	w := <-kclPool.workers
	if w == nil {
		var err error
		if w, err = startKCLWorker(); err != nil {
			kclPool.workers <- nil
			t.Fatalf("%v", err)
		}
		kclPool.all.Lock()
		kclPool.started = append(kclPool.started, w)
		kclPool.all.Unlock()
	}
	resp, err := w.render(kclRenderRequest{WorkDir: workDir, Entry: entry, DArgs: dArgs})
	if err != nil {
		_ = w.cmd.Process.Kill()
		kclPool.workers <- nil
		t.Fatalf("%v", err)
	}
	kclPool.workers <- w
	if resp.Err != "" {
		return resp.Out, errors.New(resp.Err)
	}
	return resp.Out, nil
}

// stopKCLWorkers ends every worker the run started; TestMain calls it after
// the last test. A worker also exits on its own when stdin closes, so a
// crashed parent leaves nothing behind.
func stopKCLWorkers() {
	kclPool.all.Lock()
	defer kclPool.all.Unlock()
	for _, w := range kclPool.started {
		w.stop()
	}
	kclPool.started = nil
}
