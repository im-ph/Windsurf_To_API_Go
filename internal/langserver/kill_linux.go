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
func isLSProcess(pid string) bool {
	data, err := os.ReadFile("/proc/" + pid + "/cmdline")
	if err != nil {
		return false
	}
	// First arg ends at the first NUL.
	if i := indexByte(data, 0); i >= 0 {
		data = data[:i]
	}
	return strings.Contains(string(data), "language_server")
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
