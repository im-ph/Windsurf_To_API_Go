// Package atomicfile centralises the "write a JSON blob to disk safely"
// pattern used by every persistent state file (accounts.json, proxy.json,
// runtime-config.json, model-access.json, stats.json, cache entries …).
//
// It fixes two classes of bug the older per-package implementations had:
//
//   - **Concurrent tmp collision.** Each caller used a fixed `<path>.tmp`
//     filename. Two goroutines racing on Save would both write the same
//     `.tmp`, interleaving bytes, and the final Rename would land a
//     corrupt JSON on disk — on next startup the whole file was
//     unparseable and the relevant pool was reset to empty.
//
//   - **World-readable mode.** Some callers used 0o644, which on a shared
//     host leaked secrets (e.g. proxy-config.json holds proxy passwords,
//     runtime-config.json's identity prompts can reveal deployment
//     details). 0o600 locks the file down to the service user.
//
// Per-call unique tmp names are generated with `crypto/rand` so even racing
// processes (same UID) don't collide.
package atomicfile

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// Write atomically replaces path with data using a unique tmp file and
// rename. Permissions on the new file are 0o600. Intermediate directories
// are NOT created — caller is responsible for ensuring parent exists.
func Write(path string, data []byte) error {
	var token [6]byte
	if _, err := rand.Read(token[:]); err != nil {
		return fmt.Errorf("atomicfile: rand: %w", err)
	}
	tmp := fmt.Sprintf("%s.%s.tmp", path, hex.EncodeToString(token[:]))
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		// Best-effort cleanup so we don't leave orphan .tmp files behind
		// on failure. If the rename itself raced, the winner's file is
		// valid and we just remove our abandoned draft.
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// WriteInDir is a convenience for "write this file into this directory";
// the directory is created if missing (mode 0o700). Used by the cache
// entry writer.
func WriteInDir(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return Write(filepath.Join(dir, name), data)
}
