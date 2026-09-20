package utils

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"server/log"
	"server/settings"
)

// qbtFallbackVersion is used when nothing better is known. Verified against
// api.github.com on 2026-09-20.
const qbtFallbackVersion = "5.2.3"

// qbtLegacyVersion is the identity TorrServer hardcoded before camouflage was
// configurable. Kept as the "camouflage off" behaviour so turning the setting
// off restores stock behaviour exactly.
const qbtLegacyVersion = "4.3.9"

const (
	qbtCacheFile = "qbtversion.json"
	// qBittorrent ships a handful of releases a year. A week keeps GitHub
	// contact to ~52 requests a year while never being badly stale.
	qbtCacheTTL     = 7 * 24 * time.Hour
	qbtFetchTimeout = 5 * time.Second
)

// qbtAPIURL is a var so tests can point it at an httptest server.
// /releases/latest never returns drafts or prereleases, so no filtering is
// needed.
var qbtAPIURL = "https://api.github.com/repos/qbittorrent/qBittorrent/releases/latest"

// QBTIdent is the qBittorrent identity presented to peers and trackers.
type QBTIdent struct {
	Version   string // "5.2.3"
	PeerID    string // "-qB5230-", the BEP 20 peer id prefix
	UserAgent string // "qBittorrent/5.2.3"
}

var (
	qbtFallbackIdent, _ = identFor(qbtFallbackVersion)
	qbtLegacyIdent, _   = identFor(qbtLegacyVersion)

	qbtMu      sync.Mutex
	qbtCurrent *QBTIdent // nil until first resolved
	qbtFetched bool       // at most one network attempt per process
)

// QBittorrentIdent returns the qBittorrent identity to present.
//
// With camouflage off it is the fixed legacy identity and no network or disk
// access ever happens. With camouflage on it is the newest release known from
// the on-disk cache, falling back to qbtFallbackVersion; a single background
// refresh starts when the cache is missing, corrupt or older than qbtCacheTTL.
// A refreshed version takes effect the next time the BT client is configured -
// on a settings save or a restart - never mid-session.
//
// This function never blocks on the network and never fails.
func QBittorrentIdent(camouflage bool) QBTIdent {
	if !camouflage {
		return qbtLegacyIdent
	}
	qbtMu.Lock()
	defer qbtMu.Unlock()
	if qbtCurrent == nil {
		version, fresh := loadQBTCache()
		id, ok := identFor(version)
		if !ok {
			id, fresh = qbtFallbackIdent, false
		}
		qbtCurrent = &id
		log.TLogln("Client camouflage:", id.UserAgent, id.PeerID)
		if !fresh && !qbtFetched {
			// Guarded because SetSettings restarts the BT client on every
			// settings save, and unauthenticated GitHub allows 60 req/h/IP.
			qbtFetched = true
			go refreshQBT()
		}
	}
	return *qbtCurrent
}

// identFor builds the identity for a dotted version like "5.2.3" or "4.1.9.1".
// Reports false unless it is 3 or 4 numeric components, each in 0..35.
func identFor(version string) (QBTIdent, bool) {
	parts := strings.Split(version, ".")
	if len(parts) < 3 || len(parts) > 4 {
		return QBTIdent{}, false
	}
	var n [4]int // libtorrent's 4th component ("tag") defaults to 0
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 || v > 35 {
			return QBTIdent{}, false
		}
		n[i] = v
	}
	// libtorrent generate_fingerprint("qB", major, minor, revision, tag).
	peerID := string([]byte{
		'-', 'q', 'B',
		versionToChar(n[0]), versionToChar(n[1]), versionToChar(n[2]), versionToChar(n[3]),
		'-',
	})
	return QBTIdent{
		Version:   version,
		PeerID:    peerID,
		UserAgent: "qBittorrent/" + version,
	}, true
}

// versionToChar mirrors libtorrent version_to_char (RC_2_0
// src/fingerprint.cpp): 0..9 map to '0'..'9', 10 and above to 'A' and up.
func versionToChar(v int) byte {
	if v < 10 {
		return byte('0' + v)
	}
	return byte('A' + v - 10)
}

// parseReleaseTag turns a GitHub tag like "release-5.2.3" into "5.2.3".
func parseReleaseTag(tag string) (string, bool) {
	return strings.CutPrefix(strings.TrimSpace(tag), "release-")
}

type qbtCacheJSON struct {
	Tag     string    `json:"tag"`
	Fetched time.Time `json:"fetched"`
}

func qbtCachePath() string { return filepath.Join(settings.Path, qbtCacheFile) }

// loadQBTCache returns the cached dotted version and whether it is younger
// than qbtCacheTTL. Any error yields ("", false).
func loadQBTCache() (string, bool) {
	buf, err := os.ReadFile(qbtCachePath())
	if err != nil {
		return "", false
	}
	var c qbtCacheJSON
	if json.Unmarshal(buf, &c) != nil {
		return "", false
	}
	version, ok := parseReleaseTag(c.Tag)
	if !ok {
		return "", false
	}
	return version, time.Since(c.Fetched) < qbtCacheTTL
}

func saveQBTCache(tag string) {
	if settings.ReadOnly || settings.Path == "" {
		return
	}
	if buf, err := json.Marshal(qbtCacheJSON{Tag: tag, Fetched: time.Now()}); err == nil {
		_ = os.WriteFile(qbtCachePath(), buf, 0o644)
	}
}

func fetchQBTTag() (string, error) {
	client := &http.Client{Timeout: qbtFetchTimeout}
	resp, err := client.Get(qbtAPIURL)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", err
	}
	return rel.TagName, nil
}

// refreshQBT fetches the latest release and applies it. Synchronous: run in a
// goroutine from QBittorrentIdent, called directly by tests. On any failure the
// current identity is left untouched.
func refreshQBT() {
	tag, err := fetchQBTTag()
	if err != nil {
		log.TLogln("qBittorrent version fetch failed, keeping current identity:", err.Error())
		return
	}
	version, ok := parseReleaseTag(tag)
	if !ok {
		log.TLogln("qBittorrent version fetch: unexpected tag", tag)
		return
	}
	id, ok := identFor(version)
	if !ok {
		log.TLogln("qBittorrent version fetch: unparsable version", version)
		return
	}
	saveQBTCache(tag)
	qbtMu.Lock()
	qbtCurrent = &id
	qbtMu.Unlock()
	log.TLogln("Client camouflage updated:", id.UserAgent, id.PeerID)
}
