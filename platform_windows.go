//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

var (
	user32          = syscall.NewLazyDLL("user32.dll")
	procMessageBoxW = user32.NewProc("MessageBoxW")
	shell32         = syscall.NewLazyDLL("shell32.dll")
	procKnownFolder = shell32.NewProc("SHGetKnownFolderPath")
	ole32           = syscall.NewLazyDLL("ole32.dll")
	procTaskMemFree = ole32.NewProc("CoTaskMemFree")
	kernel32        = syscall.NewLazyDLL("kernel32.dll")
	procSetFileAttr = kernel32.NewProc("SetFileAttributesW")
)

func setHidden(path string) {
	if p, err := syscall.UTF16PtrFromString(path); err == nil {
		procSetFileAttr.Call(uintptr(unsafe.Pointer(p)), 0x2) // FILE_ATTRIBUTE_HIDDEN
	}
}

func messageBox(title, text string, isError bool) {
	t, _ := syscall.UTF16PtrFromString(text)
	c, _ := syscall.UTF16PtrFromString(title)
	flags := uintptr(0x40) // MB_ICONINFORMATION
	if isError {
		flags = 0x10 // MB_ICONERROR
	}
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(c)), flags|0x00010000) // MB_SETFOREGROUND
}

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}

func documentsDir() string {
	// FOLDERID_Documents {FDD39AD0-238F-46AF-ADB4-6C85480369C7}
	guid := syscall.GUID{Data1: 0xFDD39AD0, Data2: 0x238F, Data3: 0x46AF, Data4: [8]byte{0xAD, 0xB4, 0x6C, 0x85, 0x48, 0x03, 0x69, 0xC7}}
	var p *uint16
	r, _, _ := procKnownFolder.Call(uintptr(unsafe.Pointer(&guid)), 0, 0, uintptr(unsafe.Pointer(&p)))
	if r == 0 && p != nil {
		n := 0
		for *(*uint16)(unsafe.Add(unsafe.Pointer(p), n*2)) != 0 {
			n++
		}
		s := syscall.UTF16ToString(unsafe.Slice(p, n))
		procTaskMemFree.Call(uintptr(unsafe.Pointer(p)))
		if s != "" {
			return s
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Documents")
}

func openDefault(target string) error {
	cmd := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", target)
	return cmd.Start()
}

func openFolder(dir string) error {
	return exec.Command("explorer.exe", dir).Start()
}

func revealFile(file string) error {
	cmd := exec.Command("explorer.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + file + `"`}
	return cmd.Start()
}

func findBrowser() string {
	var cands []string
	for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LOCALAPPDATA"} {
		if base := os.Getenv(env); base != "" {
			cands = append(cands, filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe"))
		}
	}
	for _, c := range cands {
		if fileExists(c) {
			return c
		}
	}
	for _, key := range []string{
		`HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\msedge.exe`,
		`HKCU\SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\msedge.exe`,
		`HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\chrome.exe`,
		`HKCU\SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\chrome.exe`,
	} {
		cmd := exec.Command("reg", "query", key, "/ve")
		hideWindow(cmd)
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(out), "\n") {
			if i := strings.Index(line, "REG_SZ"); i >= 0 {
				p := strings.Trim(strings.TrimSpace(line[i+6:]), `"`)
				if fileExists(p) {
					return p
				}
			}
		}
	}
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LOCALAPPDATA"} {
		if base := os.Getenv(env); base != "" {
			p := filepath.Join(base, "Google", "Chrome", "Application", "chrome.exe")
			if fileExists(p) {
				return p
			}
		}
	}
	return ""
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
