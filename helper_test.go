package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDriveExists(t *testing.T) {
	if runtime.GOOS == "darwin" {
		if driveExists("/Volumes/NoSuchDrive-xyz/Potluck") == nil {
			t.Fatal("a missing volume must be refused")
		}
	}
	dir := t.TempDir()
	if err := driveExists(filepath.Join(dir, "Potluck")); err != nil {
		t.Fatalf("an existing folder's child should pass: %v", err)
	}
}

func TestFollowMoved(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	if followMoved(a) != a {
		t.Fatal("no pointer: same folder")
	}
	_ = os.WriteFile(filepath.Join(a, movedPointer), []byte(b+"\n"), 0o644)
	if followMoved(a) != b {
		t.Fatal("pointer not followed")
	}
}
