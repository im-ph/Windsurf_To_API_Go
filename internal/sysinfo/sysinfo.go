// Package sysinfo exposes live OS metrics (CPU, memory, swap, network, load)
// for the dashboard overview. Linux-only fast path reads /proc directly;
// non-Linux (Windows dev builds) returns a mostly-zero snapshot.
package sysinfo

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Snapshot is the value the dashboard receives every overview refresh.
// Percentages are in 0..100 (already computed — the UI just renders).
type Snapshot struct {
	OS string `json:"os"`

	CPU struct {
		Percent float64 `json:"percent"` // 0..100 aggregate across cores
		Cores   int     `json:"cores"`
	} `json:"cpu"`

	Memory struct {
		TotalBytes     uint64  `json:"totalBytes"`
		UsedBytes      uint64  `json:"usedBytes"`
		AvailableBytes uint64  `json:"availableBytes"`
		Percent        float64 `json:"percent"` // usedBytes / totalBytes
	} `json:"memory"`

	Swap struct {
		TotalBytes uint64  `json:"totalBytes"`
		UsedBytes  uint64  `json:"usedBytes"`
		Percent    float64 `json:"percent"`
	} `json:"swap"`

	// Network is the delta since the last snapshot (bytes per second).
	Network struct {
		RxBytesPerSec uint64 `json:"rxBytesPerSec"`
		TxBytesPerSec uint64 `json:"txBytesPerSec"`
		// Total since boot — handy for absolute comparisons in wide time windows.
		RxBytesTotal uint64 `json:"rxBytesTotal"`
		TxBytesTotal uint64 `json:"txBytesTotal"`
	} `json:"network"`

	Load struct {
		Min1  float64 `json:"min1"`
		Min5  float64 `json:"min5"`
		Min15 float64 `json:"min15"`
	} `json:"load"`
}

// sampler retains the previous reading so the next Get() can compute a delta.
type sampler struct {
	mu        sync.Mutex
	prevCPU   [7]uint64 // user,nice,system,idle,iowait,irq,softirq
	prevCPUAt time.Time
	prevNetRx uint64
	prevNetTx uint64
	prevNetAt time.Time
}

var s = &sampler{}

// Get returns a fresh Snapshot. Safe to call concurrently (serialised on s.mu
// so the delta counters stay consistent).
func Get() Snapshot {
	var snap Snapshot
	snap.OS = runtime.GOOS
	snap.CPU.Cores = runtime.NumCPU()
	if runtime.GOOS != "linux" {
		// /proc isn't a thing on Windows/macOS dev; leave fields zero.
		return snap
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	readCPU(&snap)
	readMem(&snap)
	readSwap(&snap)
	readNet(&snap)
	readLoad(&snap)
	return snap
}

// ─── /proc/stat: aggregate CPU jiffies ────────────────────

func readCPU(snap *Snapshot) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		// "cpu  user nice system idle iowait irq softirq steal guest guest_nice"
		fields := strings.Fields(line)[1:]
		var cur [7]uint64
		for i := 0; i < 7 && i < len(fields); i++ {
			cur[i], _ = strconv.ParseUint(fields[i], 10, 64)
		}
		now := time.Now()
		// First sample: can't compute delta; return 0 and seed.
		if s.prevCPUAt.IsZero() {
			s.prevCPU = cur
			s.prevCPUAt = now
			return
		}
		var totalCur, totalPrev uint64
		for i := range cur {
			totalCur += cur[i]
			totalPrev += s.prevCPU[i]
		}
		deltaTotal := totalCur - totalPrev
		deltaIdle := cur[3] + cur[4] - s.prevCPU[3] - s.prevCPU[4] // idle+iowait
		if deltaTotal > 0 {
			snap.CPU.Percent = float64(deltaTotal-deltaIdle) / float64(deltaTotal) * 100
		}
		s.prevCPU = cur
		s.prevCPUAt = now
		return
	}
}

// ─── /proc/meminfo ────────────────────────────────────────

func readMem(snap *Snapshot) {
	kv, err := readMeminfo()
	if err != nil {
		return
	}
	total := kv["MemTotal"]
	// MemAvailable is the modern "what apps can actually grab" number —
	// accounts for reclaimable slab + page cache. Use it as the floor for
	// "used", matching what `free -m` shows under the "available" column.
	avail := kv["MemAvailable"]
	if avail == 0 {
		avail = kv["MemFree"] + kv["Buffers"] + kv["Cached"]
	}
	snap.Memory.TotalBytes = total * 1024
	snap.Memory.AvailableBytes = avail * 1024
	if total > avail {
		snap.Memory.UsedBytes = (total - avail) * 1024
	}
	if total > 0 {
		snap.Memory.Percent = float64(total-avail) / float64(total) * 100
	}
}

func readSwap(snap *Snapshot) {
	kv, err := readMeminfo()
	if err != nil {
		return
	}
	total := kv["SwapTotal"]
	free := kv["SwapFree"]
	snap.Swap.TotalBytes = total * 1024
	if total > free {
		snap.Swap.UsedBytes = (total - free) * 1024
	}
	if total > 0 {
		snap.Swap.Percent = float64(total-free) / float64(total) * 100
	}
}

var (
	meminfoCacheMu sync.Mutex
	meminfoCache   map[string]uint64
	meminfoCachedAt time.Time
)

// readMeminfo is called twice per Snapshot (mem + swap); share one read per
// ~500ms via a tiny cache.
func readMeminfo() (map[string]uint64, error) {
	meminfoCacheMu.Lock()
	defer meminfoCacheMu.Unlock()
	if meminfoCache != nil && time.Since(meminfoCachedAt) < 500*time.Millisecond {
		return meminfoCache, nil
	}
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]uint64{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		key := line[:colon]
		rest := strings.TrimSpace(line[colon+1:])
		if sp := strings.IndexByte(rest, ' '); sp > 0 {
			rest = rest[:sp]
		}
		n, _ := strconv.ParseUint(rest, 10, 64)
		out[key] = n
	}
	meminfoCache = out
	meminfoCachedAt = time.Now()
	return out, nil
}

// ─── /proc/net/dev: sum rx+tx across non-loopback interfaces ─

func readNet(snap *Snapshot) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return
	}
	defer f.Close()
	var rxTotal, txTotal uint64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		iface := strings.TrimSpace(line[:colon])
		if iface == "lo" || strings.HasPrefix(iface, "docker") ||
			strings.HasPrefix(iface, "br-") || strings.HasPrefix(iface, "veth") {
			continue
		}
		fields := strings.Fields(line[colon+1:])
		if len(fields) < 9 {
			continue
		}
		rx, _ := strconv.ParseUint(fields[0], 10, 64)
		tx, _ := strconv.ParseUint(fields[8], 10, 64)
		rxTotal += rx
		txTotal += tx
	}
	snap.Network.RxBytesTotal = rxTotal
	snap.Network.TxBytesTotal = txTotal
	now := time.Now()
	if !s.prevNetAt.IsZero() {
		dt := now.Sub(s.prevNetAt).Seconds()
		if dt > 0 {
			if rxTotal >= s.prevNetRx {
				snap.Network.RxBytesPerSec = uint64(float64(rxTotal-s.prevNetRx) / dt)
			}
			if txTotal >= s.prevNetTx {
				snap.Network.TxBytesPerSec = uint64(float64(txTotal-s.prevNetTx) / dt)
			}
		}
	}
	s.prevNetRx = rxTotal
	s.prevNetTx = txTotal
	s.prevNetAt = now
}

// ─── /proc/loadavg ────────────────────────────────────────

func readLoad(snap *Snapshot) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return
	}
	snap.Load.Min1, _ = strconv.ParseFloat(fields[0], 64)
	snap.Load.Min5, _ = strconv.ParseFloat(fields[1], 64)
	snap.Load.Min15, _ = strconv.ParseFloat(fields[2], 64)
}
