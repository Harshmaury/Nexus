// @nexus-project: nexus
// @nexus-path: pkg/osutil/file.go
// Package osutil provides shared OS-level utilities for Nexus.
//
// MoveFile is the single authoritative implementation of the
// rename-with-copy-fallback pattern used by both the intelligence
// router and the daemon server.  Previously each package had its own
// copy; a bug fix in one would silently leave the other broken.
//
// Lives in pkg/ so both internal/intelligence and internal/daemon can
// import it without creating an import cycle.
package osutil

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const copyBufSize = 32 * 1024 // 32 KB — matches prior implementations

// MoveFile moves src to dst.
//
// Strategy:
//  1. Ensure the destination directory exists (MkdirAll).
//  2. Attempt os.Rename — succeeds instantly when src and dst are on the
//     same filesystem (the common case).
//  3. If Rename fails (cross-device), fall back to copy-then-delete.
//
// The copy uses a fixed 32 KB buffer and io.Writer to avoid loading the
// entire file into memory.  The source file is removed only after a
// successful copy so a partial write never leaves both files corrupt.
func MoveFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("osutil.MoveFile: create destination dir: %w", err)
	}
	if err := os.Rename(src, dst); err == nil {
		return nil // same-filesystem fast path
	}
	if err := copyFile(src, dst); err != nil {
		return fmt.Errorf("osutil.MoveFile: copy %s → %s: %w", src, dst, err)
	}
	if err := os.Remove(src); err != nil {
		return fmt.Errorf("osutil.MoveFile: remove source after copy: %w", err)
	}
	return nil
}

// copyFile copies src to dst using a fixed buffer.
// dst is created (or truncated) before writing begins.
// If the write fails mid-stream, dst may be partially written —
// the caller (MoveFile) is responsible for cleanup.
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create destination: %w", err)
	}
	defer dstFile.Close()

	buf := make([]byte, copyBufSize)
	if _, err := io.CopyBuffer(dstFile, srcFile, buf); err != nil {
		return fmt.Errorf("copy data: %w", err)
	}
	return nil
}
