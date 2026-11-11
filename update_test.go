package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewerVersion(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"2.5.0", "2.4.0", true}, {"v2.10.0", "2.9.9", true}, {"2.4.0", "2.4.0", false}, {"2.4", "2.4.1", false}, {"3.0.0", "2.99.99", true}, {"2.4.0", "2.5.0", false}} {
		if got := newerVersion(c.a, c.b); got != c.want {
			t.Errorf("newerVersion(%s, %s) = %v", c.a, c.b, got)
		}
	}
}

func TestUpdate(t *testing.T) {
	payload, sum := []byte("the new program"), ""
	tag := "v99.0.0"
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/someone/secquence/releases/latest":
			fmt.Fprintf(w, `{"tag_name":%q,"body":"Notes","html_url":"https://example.com/r","assets":[{"name":"SECquence.exe","browser_download_url":"%s/dl/exe"},{"name":"SECquence.exe.sha256","browser_download_url":"%s/dl/sum"}]}`, tag, srv.URL, srv.URL)
		case "/dl/exe":
			w.Write(payload)
		case "/dl/sum":
			fmt.Fprintln(w, sum)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("SECQ_GITHUB", srv.URL)
	prev := UpdateRepo
	UpdateRepo = "someone/secquence"
	defer func() { UpdateRepo = prev }()

	exe := filepath.Join(t.TempDir(), "SECquence.exe")
	_ = os.WriteFile(exe, []byte("the old program"), 0o755)
	u := &Updater{exe: exe}
	rel, err := u.Check(context.Background(), true)
	if err != nil || !rel.Newer || rel.Latest != "99.0.0" || rel.Notes != "Notes" {
		t.Fatalf("check: %+v %v", rel, err)
	}

	// A download that does not match its checksum is refused and nothing changes.
	sum = strings.Repeat("0", 64)
	if _, err := u.Apply(context.Background()); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("bad checksum accepted: %v", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "the old program" {
		t.Fatal("program changed after a failed update")
	}
	if _, err := os.Stat(exe + ".new"); err == nil {
		t.Error("failed download left behind")
	}

	h := sha256.Sum256(payload)
	sum = hex.EncodeToString(h[:]) + "  SECquence.exe"
	if _, err := u.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "the new program" {
		t.Error("program not replaced")
	}
	if b, _ := os.ReadFile(exe + ".old"); string(b) != "the old program" {
		t.Error("previous version not kept as .old")
	}

	// Same or older release: nothing to do.
	tag = "v" + AppVersion
	if rel, _ := u.Check(context.Background(), true); rel.Newer {
		t.Error("same version offered as an update")
	}
	if _, err := u.Apply(context.Background()); err == nil {
		t.Error("applied an update that is not newer")
	}
}
