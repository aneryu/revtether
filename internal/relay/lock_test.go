//go:build unix

package relay

import (
	"testing"
)

func TestTryLockExclusive(t *testing.T) {
	dir := t.TempDir()
	a, err := tryLockDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tryLockDir(dir); err == nil {
		t.Fatal("second lock should fail")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := tryLockDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = b.Close()
}

func TestOwnerLabel(t *testing.T) {
	if got := ownerLabel(kindApp); got != "Reverse Tether.app" {
		t.Fatalf("app label = %q", got)
	}
	if got := ownerLabel(kindCLI); got != "revtether" {
		t.Fatalf("cli label = %q", got)
	}
}
