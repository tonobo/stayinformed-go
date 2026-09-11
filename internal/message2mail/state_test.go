package message2mail

import (
	"path/filepath"
	"testing"
)

func TestFileStateStoreRoundTrip(t *testing.T) {
	store := FileStateStore{Path: filepath.Join(t.TempDir(), "nested", "state.json")}
	state := State{Initialized: true, Deliveries: map[string]Delivery{"fingerprint": {NewsID: "news-1", Status: StatusDelivered}}}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Initialized || loaded.Version != stateVersion || loaded.Deliveries["fingerprint"].NewsID != "news-1" {
		t.Fatalf("unexpected state: %+v", loaded)
	}
}
