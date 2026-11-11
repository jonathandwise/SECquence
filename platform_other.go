//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Non-Windows stubs so the app can be developed and tested on Linux/macOS.

func messageBox(title, text string, isError bool) {
	fmt.Fprintf(os.Stderr, "[%s] %s\n", title, text)
}

func hideWindow(cmd *exec.Cmd) {}

func documentsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Documents")
}

func openDefault(target string) error { return exec.Command("xdg-open", target).Start() }
func openFolder(dir string) error     { return exec.Command("xdg-open", dir).Start() }
func revealFile(file string) error    { return openFolder(filepath.Dir(file)) }

func findBrowser() string {
	if p := os.Getenv("SECQ_BROWSER"); p != "" {
		return p
	}
	for _, n := range []string{"microsoft-edge", "google-chrome", "chromium", "chromium-browser"} {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	return ""
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func setHidden(path string) {}
