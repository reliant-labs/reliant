package resmetrics

import (
	"encoding/binary"
	"testing"
)

func TestDecodeTaskInfo(t *testing.T) {
	b := make([]byte, sizeofTaskInfo)
	binary.LittleEndian.PutUint64(b[0:], 400<<30) // virtual: darwin maps the shared region
	binary.LittleEndian.PutUint64(b[8:], 50<<20)  // resident
	binary.LittleEndian.PutUint32(b[80:], 999)    // pti_csw, not read
	binary.LittleEndian.PutUint32(b[84:], 12)     // threads
	rss, virt, threads, err := decodeTaskInfo(b)
	if err != nil || rss != 50<<20 || virt != 400<<30 || threads != 12 {
		t.Errorf("decodeTaskInfo = %d, %d, %d, %v", rss, virt, threads, err)
	}
	if _, _, _, err := decodeTaskInfo(b[:95]); err == nil {
		t.Error("a short proc_taskinfo was accepted")
	}
	binary.LittleEndian.PutUint32(b[84:], 0)
	if _, _, _, err := decodeTaskInfo(b); err == nil {
		t.Error("a process with no threads was accepted")
	}
}

func TestDecodeLoadavg(t *testing.T) {
	b := make([]byte, 24)
	binary.LittleEndian.PutUint32(b[0:], 3072) // 1.5 at scale 2048
	binary.LittleEndian.PutUint32(b[4:], 2048)
	binary.LittleEndian.PutUint32(b[8:], 512)
	binary.LittleEndian.PutUint32(b[12:], 0xdeadbeef) // padding
	binary.LittleEndian.PutUint64(b[16:], 2048)
	l, err := decodeLoadavg(b)
	if err != nil || l != (LoadAverage{One: 1.5, Five: 1, Fifteen: 0.25}) {
		t.Errorf("decodeLoadavg = %+v, %v", l, err)
	}
	binary.LittleEndian.PutUint64(b[16:], 0)
	if _, err := decodeLoadavg(b); err == nil {
		t.Error("a zero scale was accepted")
	}
	if _, err := decodeLoadavg(b[:20]); err == nil {
		t.Error("a short loadavg was accepted")
	}
}

func TestDecodeSwapUsage(t *testing.T) {
	b := make([]byte, 32)
	binary.LittleEndian.PutUint64(b[0:], 2<<30)  // total
	binary.LittleEndian.PutUint64(b[8:], 1<<29)  // avail
	binary.LittleEndian.PutUint64(b[16:], 3<<29) // used
	binary.LittleEndian.PutUint32(b[24:], 16384) // page size
	sw, err := decodeSwapUsage(b)
	if err != nil || sw != (Swap{Total: 2 << 30, Free: 1 << 29}) {
		t.Errorf("decodeSwapUsage = %+v, %v", sw, err)
	}
	if sw, err := decodeSwapUsage(make([]byte, 32)); err != nil || sw != (Swap{}) {
		t.Errorf("no swap file: %+v, %v; want zeros, which are measured", sw, err)
	}
	binary.LittleEndian.PutUint64(b[8:], 3<<30)
	if _, err := decodeSwapUsage(b); err == nil {
		t.Error("more swap available than exists was accepted")
	}
}

func TestDarwinCPUModes(t *testing.T) {
	m := darwinCPUModes([4]uint32{100, 50, 1000, 25}) // user, system, idle, nice
	if !near(m.User, 1.25) || !near(m.System, 0.5) || !near(m.Idle, 10) || m.IOWait != 0 || m.Steal != 0 {
		t.Errorf("darwinCPUModes = %+v, want nice counted as user", m)
	}
	big := darwinCPUModes([4]uint32{^uint32(0), 0, 0, ^uint32(0)})
	if !near(big.User, 2*float64(^uint32(0))/100) {
		t.Errorf("user plus nice overflowed: %v", big.User)
	}
}

func TestDecodeVMStatistics64(t *testing.T) {
	const page = 16384
	b := make([]byte, sizeofVMStatistics64)
	put := func(off int, pages uint32) { binary.LittleEndian.PutUint32(b[off:], pages) }
	put(0, 1000)   // free_count
	put(88, 100)   // purgeable_count
	put(92, 200)   // speculative_count, inside free_count
	put(136, 3000) // external_page_count
	put(140, 9999) // internal_page_count, not read
	total := uint64(10000 * page)
	m, err := decodeVMStatistics64(b, page, total)
	want := HostMemory{Total: total, Free: 1000 * page, Cached: 3100 * page, Used: 5900 * page, Available: 4100 * page}
	if err != nil || m != want {
		t.Errorf("decodeVMStatistics64 = %+v, %v\nwant %+v", m, err, want)
	}
	if m.Used+m.Free+m.Cached != m.Total {
		t.Error("used, free and cached do not add up to the total")
	}

	put(136, 20000) // more file pages than memory: clamped, never negative used
	m, err = decodeVMStatistics64(b, page, total)
	if err != nil || m.Used != 0 || m.Free+m.Cached != total {
		t.Errorf("clamped = %+v, %v", m, err)
	}
	if _, err := decodeVMStatistics64(b[:140], page, total); err == nil {
		t.Error("a short vm_statistics64 was accepted")
	}
	if _, err := decodeVMStatistics64(b, 0, total); err == nil {
		t.Error("a zero page size was accepted")
	}
}

func TestFiletimeSeconds(t *testing.T) {
	if got := filetimeSeconds(25_000_000, 0); !near(got, 2.5) {
		t.Errorf("filetimeSeconds(2.5s) = %v", got)
	}
	if got := filetimeSeconds(0, 1); !near(got, float64(uint64(1)<<32)/1e7) {
		t.Errorf("the high half was not shifted: %v", got)
	}
}

func TestWindowsCPUModes(t *testing.T) {
	m := windowsCPUModes(30, 40, 20) // idle, kernel (with idle), user
	if m != (CPUModes{User: 20, System: 10, Idle: 30}) {
		t.Errorf("windowsCPUModes = %+v, want idle taken out of kernel", m)
	}
	if m := windowsCPUModes(50, 40, 0); m.System != 0 {
		t.Errorf("system went negative: %+v", m)
	}
}

func TestWindowsMemory(t *testing.T) {
	m := windowsMemory(1000, 600, 250)
	if m != (HostMemory{Total: 1000, Free: 350, Cached: 250, Used: 400, Available: 600}) {
		t.Errorf("windowsMemory = %+v", m)
	}
	if m := windowsMemory(1000, 600, 900); m.Cached != 600 || m.Free != 0 || m.Used+m.Free+m.Cached != m.Total {
		t.Errorf("a cache larger than available memory was not clamped: %+v", m)
	}
	if m := windowsMemory(1000, 1200, 0); m.Used != 0 || m.Available != 1000 {
		t.Errorf("more available than total was not clamped: %+v", m)
	}
}
