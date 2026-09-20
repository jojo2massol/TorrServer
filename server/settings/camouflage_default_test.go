package settings

import (
	"encoding/json"
	"testing"
)

// withStoredBTSets points the settings db at a scratch dir and seeds it with a
// raw stored config, the way an older TorrServer would have left one.
func withStoredBTSets(t *testing.T, stored map[string]interface{}) {
	t.Helper()
	oldPath, oldReadOnly, oldDB, oldSets := Path, ReadOnly, tdb, BTsets
	Path, ReadOnly = t.TempDir(), false
	tdb = NewJsonDB()
	BTsets = new(BTSets)
	t.Cleanup(func() {
		Path, ReadOnly, tdb, BTsets = oldPath, oldReadOnly, oldDB, oldSets
	})

	buf, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	tdb.Set("Settings", "BitTorr", buf)
}

// A config written before CamouflageClient existed has no such key, and
// encoding/json would leave the bool at false. The absence check in
// loadBTSets is the only thing that makes the setting default on for those.
func TestCamouflageClientDefaultsOnForOldConfigs(t *testing.T) {
	withStoredBTSets(t, map[string]interface{}{"CacheSize": 67108864})

	loadBTSets()

	if !BTsets.CamouflageClient {
		t.Fatal("CamouflageClient must default to true when absent from a stored config")
	}
}

// An explicit false must survive: the absence check must not clobber a user
// who deliberately turned camouflage off.
func TestCamouflageClientExplicitFalseIsKept(t *testing.T) {
	withStoredBTSets(t, map[string]interface{}{
		"CacheSize":        67108864,
		"CamouflageClient": false,
	})

	loadBTSets()

	if BTsets.CamouflageClient {
		t.Fatal("an explicitly stored false must be preserved")
	}
}

func TestCamouflageClientDefaultsOnForFreshConfig(t *testing.T) {
	// SetDefaultConfig persists through tdb, so it needs a real one.
	withStoredBTSets(t, map[string]interface{}{})

	SetDefaultConfig()

	if !BTsets.CamouflageClient {
		t.Fatal("SetDefaultConfig must enable CamouflageClient")
	}
}
