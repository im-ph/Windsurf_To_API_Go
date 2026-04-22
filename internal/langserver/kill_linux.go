//go:build linux

package langserver

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"

	"windsurfapi/internal/logx"
)

// killOrphanOnPort SIGKILLs any process holding a LISTEN socket on port.
// Walks /proc/net/tcp{,6} to collect candidate inodes, then scans
// /proc/*/fd for a matching `socket:[<inode>]` symlink. Intentionally
// best-effort — on failure (permission, race, proc reaped) we just return
// and let the outer port-in-use timeout do its own job.
func killOrphanOnPort(port int) {
	inodes := listenInodes(port)
	if len(inodes) == 0 {
		return
	}
	procs, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	self := os.Getpid()
	for _, ent := range procs {
		if !ent.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(ent.Name())
		if err != nil || pid <= 1 || pid == self {
			continue
		}
		fdDir := "/proc/" + ent.Name() + "/fd"
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
	inner:
		for _, fd := range fds {
			link, err := os.Readlink(fdDir + "/" + fd.Name())
			if err != nil {
				continue
			}
			// link looks like "socket:[123456]"
			for _, ino := range inodes {
				if link == "socket:["+ino+"]" {
					// Confirm the target really is a Windsurf LS before
					// SIGKILLing — in a fast fork environment PIDs cycle
					// and /proc/<pid>/fd could point at a reaped socket
					// whose PID is now owned by an unrelated process.
					// Reading /proc/<pid>/cmdline is cheap and gives us a
					// definitive "yes this is our LS" signal.
					if !isLSProcess(ent.Name()) {
						logx.Warn("killOrphanOnPort: pid=%d owns port %d but cmdline doesn't look like an LS — skipping", pid, port)
						break inner
					}
					logx.Warn("killOrphanOnPort: SIGKILL pid=%d (owns port %d)", pid, port)
					_ = syscall.Kill(pid, syscall.SIGKILL)
					break inner
				}
			}
		}
	}
}

// isLSProcess reports whether /proc/<pid>/cmdline names the Windsurf LS
// binary. cmdline uses NUL separators; we just need the first component.
// Uses exact basename comparison rather than a substring scan — otherwise
// unrelated binaries with "language_server" in their name (e.g. somebody's
// `language_server_py.sh` or `language_server_rs`) listening on the same
// port would be killed. Explicit basename match narrows the blast radius
// to the exact upstream binary we actually spawn.
func isLSProcess(pid string) bool {
	data, err := os.ReadFile("/proc/" + pid + "/cmdline")
	if err != nil {
		return false
	}
	// First arg ends at the first NUL.
	if i := indexByte(data, 0); i >= 0 {
		data = data[:i]
	}
	argv0 := string(data)
	// Strip trailing args separator if no NUL found (some kernels end with a
	// lone NUL, already stripped; others don't — belt and braces).
	if i := strings.Index(argv0, " "); i >= 0 {
		argv0 = argv0[:i]
	}
	// Compare only the basename — the full path on disk might be
	// /opt/windsurf/... or ./bin/..., we don't care where it lives.
	base := argv0
	if i := strings.LastIndexAny(base, "/\\"); i >= 0 {
		base = base[i+1:]
	}
	return base == "language_server_linux_x64"
}

func indexByte(s []byte, b byte) int {
	for i, c := range s {
		if c == b {
			return i
		}
	}
	return -1
}

// listenInodes parses /proc/net/tcp{,6} and returns inode strings for
// LISTEN-state (st=0A) sockets on the target port.
func listenInodes(port int) []string {
	var out []string
	hexPort := fmt.Sprintf("%04X", port)
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if i == 0 || line == "" {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 10 {
				continue
			}
			local := fields[1] // "0100007F:A464"
			colon := strings.Index(local, ":")
			if colon < 0 || local[colon+1:] != hexPort {
				continue
			}
			if fields[3] != "0A" { // not LISTEN
				continue
			}
			out = append(out, fields[9])
		}
	}
	return out
}
