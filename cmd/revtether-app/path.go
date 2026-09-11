//go:build darwin

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func augmentPATH() {
	home, _ := os.UserHomeDir()
	extras := []string{
		"/opt/homebrew/bin",
		"/opt/homebrew/sbin",
		"/usr/local/bin",
		"/usr/local/sbin",
	}
	if home != "" {
		extras = append(extras,
			filepath.Join(home, "Library/Android/sdk/platform-tools"),
			filepath.Join(home, "Android/Sdk/platform-tools"),
		)
	}
	for _, key := range []string{"ANDROID_HOME", "ANDROID_SDK_ROOT"} {
		if root := os.Getenv(key); root != "" {
			extras = append(extras, filepath.Join(root, "platform-tools"))
		}
	}

	seen := map[string]bool{}
	var parts []string
	add := func(dir string) {
		if dir == "" || seen[dir] {
			return
		}
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			return
		}
		seen[dir] = true
		parts = append(parts, dir)
	}
	for _, dir := range extras {
		add(dir)
	}
	for _, dir := range strings.Split(os.Getenv("PATH"), string(os.PathListSeparator)) {
		add(dir)
	}
	if err := os.Setenv("PATH", strings.Join(parts, string(os.PathListSeparator))); err != nil {
		fmt.Fprintf(os.Stderr, "set PATH: %v\n", err)
	}
}
