package cli

// One `forge generate` used to evaluate each environment's KCL up to five
// times. The frontend inventory renders every env; so do the CI frontend
// discovery and the CI hosted-env discovery; the k3d ports fragment renders
// dev; and the closing loadability check renders every env again. Each is a
// `forge kcl eval` child, bracketed by two `git status` calls for the purity
// guard (kcl_render_purity.go). On a 22-entity project with three envs that
// was 16 evaluations, a third of the run's wall time; nothing between most of
// them had changed a byte the evaluation reads.
//
// So a generate run memoizes renderKCLPure, keyed by the inputs. A render is a
// function of the env, the render context and options this process binds, and
// the files it evaluates. The forge KCL module comes from the binary, so it is
// fixed for the run. The files generate itself rewrites mid-run, and that an
// evaluation reads, are the deploy tree (config_gen.k, the per-env config,
// workloads.k, crd_gen.k, k3d-ports.yaml) and forge.yaml (which the frontend
// outputs binding is derived from). The key hashes their CONTENT, so a step
// that rewrites one of them with different bytes gets a fresh evaluation, and
// one that rewrites the same bytes does not.
//
// A memo hit also skips the purity guard's two `git status` calls: the render
// that produced the value already ran the guard and reported anything it
// restored.
//
// The memo is live only while a generate pipeline runs (beginKCLRenderMemo).
// `forge env up` renders the same env twice on purpose, with the port
// allocator in two states this key does not see, and never shares it.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

type kclRenderMemoEntry struct {
	entities *KCLEntities
}

var kclRenderMemo struct {
	mu      sync.Mutex
	active  int
	entries map[string]kclRenderMemoEntry
}

// beginKCLRenderMemo turns the generate-run render memo on and returns the
// function that turns it off. The entries are dropped when the last run ends.
func beginKCLRenderMemo() (end func()) {
	kclRenderMemo.mu.Lock()
	kclRenderMemo.active++
	if kclRenderMemo.entries == nil {
		kclRenderMemo.entries = map[string]kclRenderMemoEntry{}
	}
	kclRenderMemo.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			kclRenderMemo.mu.Lock()
			defer kclRenderMemo.mu.Unlock()
			kclRenderMemo.active--
			if kclRenderMemo.active == 0 {
				kclRenderMemo.entries = nil
			}
		})
	}
}

// kclRenderMemoKey is the memo key for one render, or ok=false when no memo
// is live or the inputs cannot be read (then the render simply runs).
func kclRenderMemoKey(projectDir, env string) (key string, ok bool) {
	kclRenderMemo.mu.Lock()
	live := kclRenderMemo.active > 0
	kclRenderMemo.mu.Unlock()
	if !live {
		return "", false
	}
	root, err := filepath.Abs(projectDir)
	if err != nil {
		return "", false
	}
	inputs, err := kclRenderInputsDigest(root)
	if err != nil {
		return "", false
	}
	c := rememberedKCLContext(root, env)
	return fmt.Sprintf("%s\x00%s\x00%+v\x00%q\x00%s", root, env, c, activeRenderOptionDArgs(), inputs), true
}

func kclRenderMemoGet(key string) (kclRenderMemoEntry, bool) {
	kclRenderMemo.mu.Lock()
	defer kclRenderMemo.mu.Unlock()
	e, ok := kclRenderMemo.entries[key]
	return e, ok
}

func kclRenderMemoPut(key string, e kclRenderMemoEntry) {
	kclRenderMemo.mu.Lock()
	defer kclRenderMemo.mu.Unlock()
	if kclRenderMemo.entries != nil {
		kclRenderMemo.entries[key] = e
	}
}

// kclRenderInputsDigest hashes the content of every file under deploy/ and of
// forge.yaml: the inputs of an evaluation that generate itself rewrites.
func kclRenderInputsDigest(root string) (string, error) {
	h := sha256.New()
	var files []string
	deploy := filepath.Join(root, "deploy")
	err := filepath.WalkDir(deploy, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && path == deploy {
				return filepath.SkipDir
			}
			return err
		}
		if d.Type().IsRegular() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	files = append(files, filepath.Join(root, defaultProjectConfigFile))
	for _, path := range files {
		rel, _ := filepath.Rel(root, path)
		f, err := os.Open(path)
		if os.IsNotExist(err) {
			fmt.Fprintf(h, "%s\x00absent\x00", rel)
			continue
		}
		if err != nil {
			return "", err
		}
		info, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", rel, info.Size())
		_, err = io.Copy(h, f)
		_ = f.Close()
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
