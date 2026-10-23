package kclplugin

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"kcl-lang.io/lib/go/native"
)

// methodArgs is the decoded argument list of one kcl_plugin.forge.* call.
type methodArgs struct {
	args   []any
	kwargs map[string]any
}

// callArg returns the keyword argument key if present, else the positional
// argument at index, else nil.
func (a *methodArgs) callArg(index int, key string) any {
	if v, ok := a.kwargs[key]; ok {
		return v
	}
	if index < len(a.args) {
		return a.args[index]
	}
	return nil
}

// strArg returns positional argument i as a string. Panics if absent; the
// proxy recovers panics into a KCL-visible error.
func (a *methodArgs) strArg(i int) string { return fmt.Sprint(a.args[i]) }

// intArg returns positional argument i as an int64, panicking if it is not
// an integer.
func (a *methodArgs) intArg(i int) int64 {
	v, err := strconv.ParseInt(fmt.Sprint(a.args[i]), 10, 64)
	if err != nil {
		panic(err)
	}
	return v
}

type methodSpec struct {
	Body func(args *methodArgs) (any, error)
}

// methods is the kcl_plugin.forge namespace, keyed by method name. It is
// filled by Register before the proxy can be reached.
var methods map[string]methodSpec

// invoke runs one plugin call and returns the JSON the KCL runtime expects:
// the marshalled result, or {"__kcl_PanicInfo__": msg} on any failure
// (including a panic in the method body).
func invoke(method, argsJSON, kwargsJSON string) (out string) {
	defer func() {
		if r := recover(); r != nil {
			out = panicJSON(fmt.Sprint(r))
		}
	}()
	name, ok := strings.CutPrefix(method, "kcl_plugin.forge.")
	if !ok {
		return panicJSON(fmt.Sprintf("invalid method: %s, not found", method))
	}
	spec, ok := methods[name]
	if !ok {
		return panicJSON(fmt.Sprintf("invalid method: %s, not found", method))
	}
	args := &methodArgs{kwargs: map[string]any{}}
	if argsJSON != "" {
		if err := json.Unmarshal([]byte(argsJSON), &args.args); err != nil {
			return panicJSON(err.Error())
		}
	}
	if kwargsJSON != "" {
		if err := json.Unmarshal([]byte(kwargsJSON), &args.kwargs); err != nil {
			return panicJSON(err.Error())
		}
	}
	result, err := spec.Body(args)
	if err != nil {
		return panicJSON(err.Error())
	}
	data, err := json.Marshal(result)
	if err != nil {
		return panicJSON(err.Error())
	}
	return string(data)
}

func panicJSON(msg string) string {
	d, _ := json.Marshal(map[string]string{"__kcl_PanicInfo__": msg})
	return string(d)
}

// resultRingSize is how many returned C strings stay alive at once. The KCL
// runtime reads the returned pointer after the callback returns and never
// frees it, so Go must keep it valid; upstream (kcl-lang.io/lib/go/plugin
// utils_c_string.go) recycles a 100-slot ring on the same reasoning. A slot
// is only reused after 100 further calls, far longer than the runtime holds
// a result. Evaluations are serialized (see Serialized), so 100 is ample.
const resultRingSize = 100

var ring struct {
	sync.Mutex
	next int
	bufs [resultRingSize][]byte
	pins [resultRingSize]*runtime.Pinner
}

// cString copies s into pinned, NUL-terminated Go memory that outlives the
// callback return (until its ring slot is recycled).
func cString(s string) uintptr {
	ring.Lock()
	defer ring.Unlock()
	i := ring.next % resultRingSize
	ring.next++
	if ring.pins[i] != nil {
		ring.pins[i].Unpin()
	}
	b := append([]byte(s), 0)
	p := new(runtime.Pinner)
	p.Pin(&b[0])
	ring.bufs[i], ring.pins[i] = b, p
	return uintptr(unsafe.Pointer(&b[0]))
}

// goString reads a NUL-terminated C string at address p.
func goString(p uintptr) string {
	if p == 0 {
		return ""
	}
	n := 0
	for *(*byte)(unsafe.Add(unsafe.Pointer(nil), p+uintptr(n))) != 0 {
		n++
	}
	return string(unsafe.Slice((*byte)(unsafe.Add(unsafe.Pointer(nil), p)), n))
}

// proxy is the C-ABI entry the KCL runtime calls for every plugin call:
// char* proxy(char* method, char* args_json, char* kwargs_json).
func proxy(method, argsJSON, kwargsJSON uintptr) uintptr {
	return cString(invoke(goString(method), goString(argsJSON), goString(kwargsJSON)))
}

var (
	installOnce sync.Once
	// installErr is why the native KCL runtime could not be started, or nil.
	// Set once by install and never cleared: see install.
	installErr error
)

// install creates the ONE purego callback (purego caps callbacks at 2000
// per process and never frees them) and hands it to the native KCL client.
// The client is a process-wide sync.Once singleton: whichever caller
// initializes it first fixes its plugin agent for the life of the process.
//
// kcl-lang.io/lib PANICS when it cannot extract or load libkcl (a read-only
// cache dir, an antivirus-quarantined kcl.dll, a KCL_LIB_HOME pointing
// nowhere) — from inside its own sync.Once, which a panic still marks done.
// Unrecovered, that crashed forge from whatever goroutine first touched KCL
// (`forge doctor`'s deploy probe did). Recovered, the library's Once is spent
// and its client is nil, so the failure is PERMANENT for the process: install
// records it, and Ready reports it to every later caller instead of letting
// them reach the nil client.
func install() {
	installOnce.Do(func() {
		defer func() {
			if r := recover(); r != nil {
				installErr = fmt.Errorf("load the KCL runtime: %v", r)
			}
		}()
		if err := unscopeFileBuiltins(); err != nil {
			installErr = err
			return
		}
		native.NewNativeServiceClientWithPluginAgent(uint64(purego.NewCallback(proxy)))
	})
}

// Ready installs the bridge if needed and reports whether the native KCL
// runtime is usable. A non-nil error is permanent for this process.
func Ready() error {
	install()
	return installErr
}
