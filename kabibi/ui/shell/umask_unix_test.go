// Copyright 2026 the u-root Authors. All rights reserved
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build unix

package shell

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestShellUmaskAffectsChmod(t *testing.T) {
	oldMask := syscall.Umask(0)
	syscall.Umask(oldMask)
	t.Cleanup(func() { syscall.Umask(oldMask) })

	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	if err := os.WriteFile(path, nil, 0o444); err != nil {
		t.Fatal(err)
	}
	session, err := NewExecutionSession(dir)
	if err != nil {
		t.Fatal(err)
	}

	script := "umask 0000; chmod +w file"
	_, parsed := session.ClassifyInput(script)
	record, err := session.RunCommand(context.Background(), script, parsed, SourceUserShell, nil)
	if err != nil || record.ExitCode != 0 {
		t.Fatalf("shell umask/chmod failed (code=%d, err=%v): %q", record.ExitCode, err, record.Output)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o666 {
		t.Fatalf("mode after omitted-who chmod under umask 0000 = %04o, want 0666", got)
	}
}
