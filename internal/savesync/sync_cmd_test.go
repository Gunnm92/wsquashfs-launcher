package savesync

import (
	"strings"
	"testing"
)

// TestSyncPeersAddReplaceRemove : une machine remplacée par son empreinte, nom en double distingué,
// retrait sans tenir compte de la casse.
func TestSyncPeersAddReplaceRemove(t *testing.T) {
	fpA, fpB := strings.Repeat("A", 64), strings.Repeat("B", 64)
	cfg := Config{ID: "client-machine-01"}
	cfg = AddPeer(cfg, Peer{Name: "steambox", URL: "https://10.0.0.2:8790", Token: "t1", Fingerprint: fpA})
	cfg = AddPeer(cfg, Peer{Name: "steambox", URL: "https://10.0.0.2:8791", Token: "t2", Fingerprint: fpA})
	if len(cfg.Peers) != 1 || cfg.Peers[0].URL != "https://10.0.0.2:8791" {
		t.Fatalf("même empreinte : %+v, attendue une seule machine remplacée", cfg.Peers)
	}
	cfg = AddPeer(cfg, Peer{Name: "STEAMBOX", URL: "https://10.0.0.3:8790", Fingerprint: fpB})
	if len(cfg.Peers) != 2 || cfg.Peers[1].Name != "STEAMBOX (https://10.0.0.3:8790)" {
		t.Fatalf("nom en double : %+v", cfg.Peers)
	}
	cfg, ok := removeSyncPeer(cfg, "steambox")
	if !ok || len(cfg.Peers) != 1 {
		t.Fatalf("retrait sans tenir compte de la casse : ok %v, %+v", ok, cfg.Peers)
	}
	if _, ok := removeSyncPeer(cfg, "inconnue"); ok {
		t.Error("machine inconnue retirée")
	}
	if got := syncPeerNames(cfg); got != cfg.Peers[0].Name {
		t.Errorf("noms : %q", got)
	}
	if id := PeerID(Peer{Fingerprint: fpA}); id != strings.Repeat("a", 32) || !ValidID(id) {
		t.Errorf("identifiant local : %q", id)
	}
}
