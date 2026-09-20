package utils

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"server/settings"
)

// setupQBTTest points the resolver at a local server and a scratch config dir,
// and restores both afterwards.
func setupQBTTest(t *testing.T, url string) {
	t.Helper()
	oldURL, oldPath := qbtAPIURL, settings.Path
	qbtAPIURL, settings.Path = url, t.TempDir()
	resetQBTState()
	t.Cleanup(func() {
		qbtAPIURL, settings.Path = oldURL, oldPath
		resetQBTState()
	})
}

func resetQBTState() {
	qbtMu.Lock()
	qbtCurrent, qbtFetched = nil, false
	qbtMu.Unlock()
}

// qbtServer returns a server replying with body, and a counter of how many
// requests actually reached it.
func qbtServer(t *testing.T, body string, status int) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func writeQBTCache(t *testing.T, tag string, age time.Duration) {
	t.Helper()
	buf, err := json.Marshal(qbtCacheJSON{Tag: tag, Fetched: time.Now().Add(-age)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(qbtCachePath(), buf, 0o644); err != nil {
		t.Fatal(err)
	}
}

func waitForQBTVersion(t *testing.T, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if got := QBittorrentIdent(true).Version; got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for version %q, got %q", want, QBittorrentIdent(true).Version)
}

func TestIdentFor(t *testing.T) {
	for _, c := range []struct {
		version string
		peerID  string
		ok      bool
	}{
		{"5.2.3", "-qB5230-", true},
		{"4.3.9", "-qB4390-", true},  // pins the legacy identity
		{"4.1.9.1", "-qB4191-", true}, // four components
		{"5.10.3", "-qB5A30-", true},  // component >= 10
		{"12.0.0", "-qBC000-", true},
		{"5.2", "", false},
		{"5.2.3.4.5", "", false},
		{"5.x.3", "", false},
		{"5.2.-1", "", false},
		{"5.2.36", "", false}, // past 'Z'
		{"", "", false},
	} {
		id, ok := identFor(c.version)
		if ok != c.ok {
			t.Fatalf("identFor(%q) ok = %v, want %v", c.version, ok, c.ok)
		}
		if !c.ok {
			continue
		}
		if id.PeerID != c.peerID {
			t.Fatalf("identFor(%q) peer id = %q, want %q", c.version, id.PeerID, c.peerID)
		}
		if want := "qBittorrent/" + c.version; id.UserAgent != want {
			t.Fatalf("identFor(%q) user agent = %q, want %q", c.version, id.UserAgent, want)
		}
		if len(id.PeerID) != 8 {
			t.Fatalf("identFor(%q) peer id prefix is %d bytes, want 8", c.version, len(id.PeerID))
		}
	}
}

func TestParseReleaseTag(t *testing.T) {
	for _, c := range []struct {
		tag  string
		want string
		ok   bool
	}{
		{"release-5.2.3", "5.2.3", true},
		{" release-5.2.3\n", "5.2.3", true},
		{"v5.2.3", "", false},
		{"5.2.3", "", false},
	} {
		got, ok := parseReleaseTag(c.tag)
		if ok != c.ok || (ok && got != c.want) {
			t.Fatalf("parseReleaseTag(%q) = %q,%v want %q,%v", c.tag, got, ok, c.want, c.ok)
		}
	}
}

func TestRefreshQBTSuccess(t *testing.T) {
	srv, _ := qbtServer(t, `{"tag_name":"release-5.2.3"}`, http.StatusOK)
	setupQBTTest(t, srv.URL)

	refreshQBT()
	if got := QBittorrentIdent(true).PeerID; got != "-qB5230-" {
		t.Fatalf("peer id = %q, want %q", got, "-qB5230-")
	}
	buf, err := os.ReadFile(qbtCachePath())
	if err != nil {
		t.Fatalf("cache not written: %v", err)
	}
	var c qbtCacheJSON
	if err := json.Unmarshal(buf, &c); err != nil || c.Tag != "release-5.2.3" {
		t.Fatalf("cache = %#v, err %v", c, err)
	}
}

func TestQBTFallback(t *testing.T) {
	for _, c := range []struct {
		name   string
		body   string
		status int
	}{
		{"http error", "", http.StatusInternalServerError},
		{"malformed json", "not json", http.StatusOK},
		{"unexpected tag", `{"tag_name":"v5.2.3"}`, http.StatusOK},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := qbtServer(t, c.body, c.status)
			setupQBTTest(t, srv.URL)

			refreshQBT()
			if got := QBittorrentIdent(true).Version; got != qbtFallbackVersion {
				t.Fatalf("version = %q, want fallback %q", got, qbtFallbackVersion)
			}
		})
	}
}

// A fresh cache must be used without any network access at all. Deterministic:
// a fresh cache means the refresh goroutine is never started.
func TestQBTCacheHitAvoidsNetwork(t *testing.T) {
	srv, hits := qbtServer(t, `{"tag_name":"release-5.2.3"}`, http.StatusOK)
	setupQBTTest(t, srv.URL)
	writeQBTCache(t, "release-6.1.2", time.Hour)

	if got := QBittorrentIdent(true).PeerID; got != "-qB6120-" {
		t.Fatalf("peer id = %q, want cached %q", got, "-qB6120-")
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("network hits = %d, want 0", n)
	}
}

// An expired cache must still be preferred over the fallback for the immediate
// answer, and refreshed in the background.
func TestQBTCacheExpiryRefetches(t *testing.T) {
	srv, hits := qbtServer(t, `{"tag_name":"release-5.2.3"}`, http.StatusOK)
	setupQBTTest(t, srv.URL)
	writeQBTCache(t, "release-4.6.7", 8*24*time.Hour)

	if got := QBittorrentIdent(true).Version; got != "4.6.7" {
		t.Fatalf("first call = %q, want the stale cached value %q", got, "4.6.7")
	}
	waitForQBTVersion(t, "5.2.3", 2*time.Second)
	if n := hits.Load(); n != 1 {
		t.Fatalf("network hits = %d, want 1", n)
	}
}

func TestQBTCorruptCacheFile(t *testing.T) {
	srv, _ := qbtServer(t, `{"tag_name":"release-5.2.3"}`, http.StatusOK)
	setupQBTTest(t, srv.URL)
	if err := os.WriteFile(qbtCachePath(), []byte("{{{"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := QBittorrentIdent(true).Version; got != qbtFallbackVersion {
		t.Fatalf("first call = %q, want fallback %q", got, qbtFallbackVersion)
	}
	waitForQBTVersion(t, "5.2.3", 2*time.Second)
}

// The off path is the privacy escape hatch: it must touch neither the network
// nor the disk.
func TestQBTCamouflageOffNoNetwork(t *testing.T) {
	srv, hits := qbtServer(t, `{"tag_name":"release-5.2.3"}`, http.StatusOK)
	setupQBTTest(t, srv.URL)

	id := QBittorrentIdent(false)
	if id.Version != qbtLegacyVersion || id.PeerID != "-qB4390-" {
		t.Fatalf("got %#v, want the legacy 4.3.9 identity", id)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("network hits = %d, want 0", n)
	}
	if _, err := os.Stat(qbtCachePath()); !os.IsNotExist(err) {
		t.Fatalf("cache file was written with camouflage off")
	}
	qbtMu.Lock()
	defer qbtMu.Unlock()
	if qbtCurrent != nil {
		t.Fatal("camouflage off must not resolve an identity")
	}
}

// Rate-limit guard: SetSettings restarts the BT client on every settings save,
// and unauthenticated GitHub allows 60 requests an hour.
func TestQBTOnlyOneFetchAttempt(t *testing.T) {
	srv, hits := qbtServer(t, "", http.StatusInternalServerError)
	setupQBTTest(t, srv.URL)

	for i := 0; i < 3; i++ {
		QBittorrentIdent(true)
	}
	time.Sleep(200 * time.Millisecond)
	if n := hits.Load(); n != 1 {
		t.Fatalf("network hits = %d, want exactly 1", n)
	}
}
