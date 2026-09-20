package api

import (
	"encoding/json"
	"testing"

	sets "server/settings"
)

func current() *sets.BTSets {
	return &sets.BTSets{
		CacheSize:        64 << 20,
		EnableBonjour:    true,
		CamouflageClient: true,
		DisableUTP:       true,
		TorrentsSavePath: "/mnt/ssd",
		ConnectionsLimit: 25,
	}
}

// A client that does not know a field must not reset it. This is what an older
// app does on every save: it round trips only the fields it knows about.
func TestMergeBTSetsKeepsFieldsTheClientDidNotSend(t *testing.T) {
	raw := json.RawMessage(`{"CacheSize":134217728,"EnableBonjour":true,"DisableUTP":true}`)

	got, err := mergeBTSets(current(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if !got.CamouflageClient {
		t.Fatal("CamouflageClient was reset by a client that never sent it")
	}
	if got.TorrentsSavePath != "/mnt/ssd" {
		t.Fatalf("TorrentsSavePath = %q, want it untouched", got.TorrentsSavePath)
	}
	if got.ConnectionsLimit != 25 {
		t.Fatalf("ConnectionsLimit = %d, want it untouched", got.ConnectionsLimit)
	}
	if got.CacheSize != 134217728 {
		t.Fatalf("CacheSize = %d, want the value that was sent", got.CacheSize)
	}
}

// An explicit false must still win, or the setting could never be turned off.
func TestMergeBTSetsAppliesExplicitFalse(t *testing.T) {
	got, err := mergeBTSets(current(), json.RawMessage(`{"CamouflageClient":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.CamouflageClient {
		t.Fatal("an explicit false must be applied")
	}
	if !got.EnableBonjour {
		t.Fatal("unrelated fields must survive")
	}
}

func TestMergeBTSetsRejectsEmptyBody(t *testing.T) {
	if _, err := mergeBTSets(current(), nil); err == nil {
		t.Fatal("an empty sets object must be rejected, not applied as a reset")
	}
}

// The merge must not depend on there already being settings loaded.
func TestMergeBTSetsWithNilCurrent(t *testing.T) {
	got, err := mergeBTSets(nil, json.RawMessage(`{"CacheSize":1024}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.CacheSize != 1024 {
		t.Fatalf("CacheSize = %d, want 1024", got.CacheSize)
	}
}
