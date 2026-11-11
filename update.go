package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Updates come from the GitHub repository the program was built from. A release
// there carries SECquence.exe and SECquence.exe.sha256; when its version is newer
// than this copy, the app offers to download it, swap itself and restart.

// UpdateRepo is "owner/name" on GitHub. It is set when a release is built
// (-ldflags "-X main.UpdateRepo=..."); a copy built without it never checks.
var UpdateRepo = ""

const (
	updateAsset = "SECquence.exe"
	updateSum   = "SECquence.exe.sha256"
)

type Release struct {
	Current    string `json:"current"`
	Latest     string `json:"latest"`
	Newer      bool   `json:"newer"`
	Notes      string `json:"notes"`
	Page       string `json:"page"`
	Repo       string `json:"repo"`
	Configured bool   `json:"configured"`
	exeURL     string
	sumURL     string
}

type Updater struct {
	mu      sync.Mutex
	last    *Release
	checked time.Time
	exe     string // the program file to replace; empty means the running one
}

func githubAPI() string {
	if v := os.Getenv("SECQ_GITHUB"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://api.github.com"
}

// newerVersion reports whether a is a later version than b ("2.10.0" > "2.9.1").
func newerVersion(a, b string) bool {
	pa, pb := strings.Split(strings.TrimPrefix(a, "v"), "."), strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x > y
		}
	}
	return false
}

func githubGet(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", AppName+"/"+AppVersion)
	req.Header.Set("Accept", "application/vnd.github+json")
	return http.DefaultClient.Do(req)
}

// Check asks GitHub for the latest release. Results are kept for three hours unless
// fresh is set.
func (u *Updater) Check(ctx context.Context, fresh bool) (*Release, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	rel := &Release{Current: AppVersion, Repo: UpdateRepo, Configured: UpdateRepo != ""}
	if UpdateRepo == "" {
		return rel, nil
	}
	if !fresh && u.last != nil && time.Since(u.checked) < 3*time.Hour {
		return u.last, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resp, err := githubGet(ctx, githubAPI()+"/repos/"+UpdateRepo+"/releases/latest")
	if err != nil {
		return nil, errors.New("couldn't reach GitHub to check for updates")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 { // no releases published yet
		u.last, u.checked = rel, time.Now()
		return rel, nil
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub answered %s when checking for updates", resp.Status)
	}
	var gh struct {
		Tag    string `json:"tag_name"`
		Body   string `json:"body"`
		Page   string `json:"html_url"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&gh); err != nil {
		return nil, errors.New("couldn't read GitHub's reply about updates")
	}
	rel.Latest, rel.Notes, rel.Page = strings.TrimPrefix(gh.Tag, "v"), strings.TrimSpace(gh.Body), gh.Page
	for _, a := range gh.Assets {
		switch a.Name {
		case updateAsset:
			rel.exeURL = a.URL
		case updateSum:
			rel.sumURL = a.URL
		}
	}
	rel.Newer = rel.exeURL != "" && rel.sumURL != "" && newerVersion(rel.Latest, AppVersion)
	u.last, u.checked = rel, time.Now()
	return rel, nil
}

func download(ctx context.Context, url string, w io.Writer, limit int64) error {
	resp, err := githubGet(ctx, url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("download failed: %s", resp.Status)
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, limit+1))
	if err == nil && n > limit {
		err = errors.New("download is larger than expected")
	}
	return err
}

// Apply downloads the release, checks it against its published checksum and puts it
// in place of the running program. It returns the path to start. The old program
// file is kept beside it as "<name>.old" until the new copy starts.
func (u *Updater) Apply(ctx context.Context) (string, error) {
	rel, err := u.Check(ctx, true)
	if err != nil {
		return "", err
	}
	if !rel.Newer {
		return "", errors.New("this is already the latest version")
	}
	exe := u.exe
	if exe == "" {
		if exe, err = os.Executable(); err == nil {
			exe, err = filepath.EvalSymlinks(exe)
		}
		if err != nil {
			return "", errors.New("couldn't work out where the program file is")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	var sum strings.Builder
	if err := download(ctx, rel.sumURL, &sum, 4096); err != nil {
		return "", fmt.Errorf("couldn't download the update's checksum: %v", err)
	}
	want := strings.ToLower(strings.TrimSpace(sum.String()))
	if i := strings.IndexAny(want, " \t"); i > 0 {
		want = want[:i]
	}
	fresh, old := exe+".new", exe+".old"
	f, err := os.OpenFile(fresh, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return "", fmt.Errorf("can't write to %s. Download the new version from %s instead", filepath.Dir(exe), rel.Page)
	}
	h := sha256.New()
	err = download(ctx, rel.exeURL, io.MultiWriter(f, h), 200<<20)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && hex.EncodeToString(h.Sum(nil)) != want {
		err = errors.New("the downloaded file did not match its checksum, so it was not installed")
	}
	if err == nil && runtime.GOOS == "windows" && u.exe == "" {
		head := make([]byte, 2)
		if g, oerr := os.Open(fresh); oerr == nil {
			_, _ = io.ReadFull(g, head)
			g.Close()
		}
		if string(head) != "MZ" {
			err = errors.New("the downloaded file is not a Windows program")
		}
	}
	if err != nil {
		_ = os.Remove(fresh)
		return "", fmt.Errorf("update failed: %v", err)
	}
	// Windows lets a running program file be renamed, though not overwritten.
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		_ = os.Remove(fresh)
		return "", fmt.Errorf("couldn't replace the program file: %v", err)
	}
	if err := os.Rename(fresh, exe); err != nil {
		_ = os.Rename(old, exe)
		return "", fmt.Errorf("couldn't put the new version in place: %v", err)
	}
	logf("updated %s to %s", AppVersion, rel.Latest)
	return exe, nil
}

// cleanUpdate removes the previous version left behind by an update. The old copy
// may still be shutting down, so it tries for a little while.
func cleanUpdate() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return
	}
	go func() {
		for i := 0; i < 10; i++ {
			if _, err := os.Stat(exe + ".old"); err != nil || os.Remove(exe+".old") == nil {
				return
			}
			time.Sleep(2 * time.Second)
		}
	}()
}

// startProgram launches a program on its own, not tied to this process.
func startProgram(path string) error {
	cmd := exec.Command(path)
	cmd.Dir = filepath.Dir(path)
	return cmd.Start()
}
