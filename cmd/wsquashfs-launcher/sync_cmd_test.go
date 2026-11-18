package main

// Tests de --sync (sync_cmd.go) : la logique d'un jeu (syncGameWith) contre le serveur httptest de
// sync_http_test.go, une séquence réaliste à deux machines, l'association et la liste des machines.
// Serveur et client partagent les globales du processus : le client n'a donc pas de verrou local
// (syncLockGame remplacée), et ses dossiers (users, stateRoot) sont explicites.

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"wsquashfs-launcher/internal/ui"
)

// syncNoLocalLock : pas de verrou côté client (le serveur, dans le même processus, tient le même
// fichier de verrou : son verrou suffit à la route).
func syncNoLocalLock(t *testing.T) {
	t.Helper()
	old := syncLockGame
	syncLockGame = func(string) (*os.File, bool) { return nil, true }
	t.Cleanup(func() { syncLockGame = old })
}

// syncGlobalsSaved : restaure les globales que gameUsesSaveDir et copyGameSaves modifient.
func syncGlobalsSaved(t *testing.T) {
	t.Helper()
	isolateState(t)
}

// rigPeerID : identifiant local du serveur de test (état de synchro côté client).
func rigPeerID(r *syncRig) string {
	return syncPeerID(r.peer)
}

// rigRemote : état du jeu sur le serveur de test, tel que la liste le donne.
func rigRemote(t *testing.T, r *syncRig, name string) remoteSave {
	t.Helper()
	games, err := r.client.list()
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range games {
		if g.Name == name {
			return g
		}
	}
	return remoteSave{Name: name}
}

// syncOne : synchronise le jeu de test avec le serveur de test, depuis le dossier local donné.
func syncOne(t *testing.T, r *syncRig, local, stateRoot string, dryRun bool) (ui.Status, string) {
	t.Helper()
	return syncGameWith(r.client, "steambox", rigPeerID(r), stateRoot, syncRigGame, gameTarget(syncRigGame, local), rigRemote(t, r, syncRigGame), dryRun)
}

// TestSyncGameReceiveThenUpToDate : première synchro (pas d'état) = réception ; la suivante est à jour.
// Les deux états sont enregistrés des deux côtés.
func TestSyncGameReceiveThenUpToDate(t *testing.T) {
	r := newSyncRig(t)
	syncNoLocalLock(t)
	local := clientUsers(t)
	stateRoot := t.TempDir()
	want := saveFingerprint(r.users, collectSaveFiles(r.users))

	st, msg := syncOne(t, r, local, stateRoot, false)
	if st != ui.OK || msg != "reçu de steambox" {
		t.Fatalf("première synchro : %v %q", st, msg)
	}
	if got := saveFingerprint(local, collectSaveFiles(local)); got != want {
		t.Fatalf("empreinte locale %s, attendue %s", got, want)
	}
	if s := loadSyncState(stateRoot, rigPeerID(r), syncRigGame).Fingerprint; s != want {
		t.Errorf("état local %q, attendu %s", s, want)
	}
	if s := loadSyncState(syncStateRoot(), "client-machine-01", syncRigGame).Fingerprint; s != want {
		t.Errorf("état serveur %q, attendu %s", s, want)
	}
	st, msg = syncOne(t, r, local, stateRoot, false)
	if st != ui.OK || msg != "à jour" {
		t.Errorf("seconde synchro : %v %q, attendu à jour", st, msg)
	}
}

// TestSyncGameSend : des sauvegardes changées ici, rien de changé là-bas : envoi.
func TestSyncGameSend(t *testing.T) {
	r := newSyncRig(t)
	syncNoLocalLock(t)
	local := clientUsers(t)
	stateRoot := t.TempDir()
	if st, msg := syncOne(t, r, local, stateRoot, false); st != ui.OK {
		t.Fatalf("réception initiale : %v %q", st, msg)
	}
	syncTestPut(t, filepath.Join(local, "steamuser", "Documents", "Game", "nouvelle.sav"), "nouveau", syncTestTime.Add(time.Hour))
	mine := saveFingerprint(local, collectSaveFiles(local))

	st, msg := syncOne(t, r, local, stateRoot, false)
	if st != ui.OK || msg != "envoyé à steambox" {
		t.Fatalf("envoi : %v %q", st, msg)
	}
	if got := saveFingerprint(r.users, collectSaveFiles(r.users)); got != mine {
		t.Errorf("serveur : empreinte %s, attendue %s", got, mine)
	}
	if s := loadSyncState(stateRoot, rigPeerID(r), syncRigGame).Fingerprint; s != mine {
		t.Errorf("état local %q, attendu %s", s, mine)
	}
	if s := loadSyncState(syncStateRoot(), "client-machine-01", syncRigGame).Fingerprint; s != mine {
		t.Errorf("état serveur %q, attendu %s", s, mine)
	}
}

// TestSyncGameConflictKeepsBoth : les deux côtés ont changé, quelle que soit la taille : rien n'est
// transféré ni écrasé, la ligne annonce le conflit (sans échec). Sans manager déclaré : rien envoyé.
func TestSyncGameConflictKeepsBoth(t *testing.T) {
	r := newSyncRig(t)
	syncNoLocalLock(t)
	local := clientUsers(t)
	stateRoot := t.TempDir()
	if st, msg := syncOne(t, r, local, stateRoot, false); st != ui.OK {
		t.Fatalf("réception initiale : %v %q", st, msg)
	}
	srvFile := filepath.Join(r.users, "steamuser", "Documents", "Game", "serveur.sav")
	cliFile := filepath.Join(local, "steamuser", "Documents", "Game", "client.sav")
	syncTestPut(t, srvFile, strings.Repeat("serveur", 1000), syncTestTime.Add(3*time.Hour))
	syncTestPut(t, cliFile, "client", syncTestTime.Add(time.Hour))
	localFP := saveFingerprint(local, collectSaveFiles(local))
	srvFP := saveFingerprint(r.users, collectSaveFiles(r.users))

	st, msg := syncOne(t, r, local, stateRoot, false)
	if st != ui.Skip || msg != "conflit avec steambox (les deux ont changé) : à départager dans le manager" {
		t.Fatalf("conflit : %v %q", st, msg)
	}
	if got := saveFingerprint(local, collectSaveFiles(local)); got != localFP {
		t.Error("copie locale modifiée")
	}
	if got := saveFingerprint(r.users, collectSaveFiles(r.users)); got != srvFP {
		t.Error("copie du serveur modifiée")
	}
	if _, err := os.Stat(srvFile); err != nil {
		t.Errorf("version du serveur perdue : %v", err)
	}
	if _, err := os.Stat(cliFile); err != nil {
		t.Errorf("version d'ici perdue : %v", err)
	}
	if entries, _ := os.ReadDir(r.backups); len(entries) != 0 {
		t.Errorf("archive créée sans remplacement : %d", len(entries))
	}
}

// TestSyncGameConflictSendsToManager : avec un manager déclaré, la copie d'ici lui est envoyée ;
// rien n'est remplacé et l'état de synchro ne change pas.
func TestSyncGameConflictSendsToManager(t *testing.T) {
	r := newSyncRig(t)
	syncNoLocalLock(t)
	syncGlobalsSaved(t)
	local := clientUsers(t)
	stateRoot := t.TempDir()
	if st, msg := syncOne(t, r, local, stateRoot, false); st != ui.OK {
		t.Fatalf("réception initiale : %v %q", st, msg)
	}
	syncTestPut(t, filepath.Join(r.users, "steamuser", "Documents", "Game", "serveur.sav"), "serveur", syncTestTime.Add(3*time.Hour))
	syncTestPut(t, filepath.Join(local, "steamuser", "Documents", "Game", "client.sav"), "client", syncTestTime.Add(time.Hour))
	before := loadSyncState(stateRoot, rigPeerID(r), syncRigGame).Fingerprint
	m, peer := newAutoManager(t)
	cfg := loadSyncConfig()
	cfg.ID = "client-machine-01"
	cfg.Peers = append(cfg.Peers, peer)
	if err := saveSyncConfig(cfg); err != nil {
		t.Fatal(err)
	}

	st, msg := syncOne(t, r, local, stateRoot, false)
	if st != ui.Skip || !strings.HasSuffix(msg, " ; copie d'ici envoyée au manager") {
		t.Fatalf("conflit : %v %q", st, msg)
	}
	if m.puts != 1 || !tarHas(t, m.body, "client.sav") {
		t.Errorf("manager : %d envoi(s), archive sans client.sav", m.puts)
	}
	if _, err := os.Stat(filepath.Join(local, "steamuser", "Documents", "Game", "client.sav")); err != nil {
		t.Errorf("version d'ici perdue : %v", err)
	}
	if s := loadSyncState(stateRoot, rigPeerID(r), syncRigGame).Fingerprint; s != before {
		t.Errorf("état de synchro modifié par le conflit : %q, attendu %q", s, before)
	}
}

// TestSyncGameDryRunChangesNothing : la simulation annonce le sens, sans verrou ni écriture
// (le verrou local est refusé pour le prouver).
func TestSyncGameDryRunChangesNothing(t *testing.T) {
	r := newSyncRig(t)
	syncNoLocalLock(t)
	local := clientUsers(t)
	stateRoot := t.TempDir()
	if st, msg := syncOne(t, r, local, stateRoot, false); st != ui.OK {
		t.Fatalf("réception initiale : %v %q", st, msg)
	}
	syncTestPut(t, filepath.Join(local, "steamuser", "Documents", "Game", "nouvelle.sav"), "nouveau", syncTestTime.Add(time.Hour))

	old := syncLockGame
	syncLockGame = func(string) (*os.File, bool) { return nil, false }
	defer func() { syncLockGame = old }()
	beforeLocal, beforeServer := syncTestTree(t, local), syncTestTree(t, r.users)
	beforeState := syncTestTree(t, syncStateRoot())
	beforeClientState := syncTestTree(t, stateRoot)

	st, msg := syncOne(t, r, local, stateRoot, true)
	if st != ui.Info || msg != "envoi" {
		t.Errorf("simulation : %v %q, attendu envoi", st, msg)
	}
	if !reflect.DeepEqual(beforeLocal, syncTestTree(t, local)) || !reflect.DeepEqual(beforeServer, syncTestTree(t, r.users)) {
		t.Error("simulation : sauvegardes modifiées")
	}
	if !reflect.DeepEqual(beforeState, syncTestTree(t, syncStateRoot())) || !reflect.DeepEqual(beforeClientState, syncTestTree(t, stateRoot)) {
		t.Error("simulation : états de synchro modifiés")
	}
}

// TestSyncGameLockedOnServer : le jeu tourne (verrou tenu) sur la machine distante : ignoré, sans erreur.
func TestSyncGameLockedOnServer(t *testing.T) {
	r := newSyncRig(t)
	syncNoLocalLock(t)
	local := clientUsers(t)
	stateRoot := t.TempDir()
	if st, msg := syncOne(t, r, local, stateRoot, false); st != ui.OK {
		t.Fatalf("réception initiale : %v %q", st, msg)
	}
	syncTestPut(t, filepath.Join(local, "steamuser", "Documents", "Game", "nouvelle.sav"), "nouveau", syncTestTime.Add(time.Hour))

	if err := os.MkdirAll(filepath.Join(conf.cacheDir, "locks"), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(lockPath(syncRigGame), os.O_CREATE|os.O_RDONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	st, msg := syncOne(t, r, local, stateRoot, false)
	if st != ui.Skip || msg != "en cours sur steambox, ignoré" {
		t.Errorf("jeu verrouillé là-bas : %v %q", st, msg)
	}
}

// TestSyncGameLockedHere : le jeu tourne ici (verrou local refusé) : ignoré, rien n'est échangé.
func TestSyncGameLockedHere(t *testing.T) {
	r := newSyncRig(t)
	old := syncLockGame
	syncLockGame = func(string) (*os.File, bool) { return nil, false }
	defer func() { syncLockGame = old }()
	before := syncTestTree(t, r.users)
	st, msg := syncOne(t, r, clientUsers(t), t.TempDir(), false)
	if st != ui.Skip || msg != "en cours ici, ignoré" {
		t.Errorf("jeu en cours ici : %v %q", st, msg)
	}
	if !reflect.DeepEqual(before, syncTestTree(t, r.users)) {
		t.Error("jeu en cours ici : sauvegardes du serveur modifiées")
	}
}

// TestSyncGameSaveDirIgnored : un jeu à SAVEDIR n'est pas synchronisé (ses sauvegardes sont hors du
// prefix) ; sans image connue, il ne l'est pas.
func TestSyncGameSaveDirIgnored(t *testing.T) {
	r := newSyncRig(t)
	syncNoLocalLock(t)
	syncGlobalsSaved(t)
	img := filepath.Join(t.TempDir(), syncRigGame+".wsquashfs")
	writeGameImage(t, syncRigGame, img, "SAVEDIR=drive_c/game/saves\n")

	if !gameUsesSaveDir(syncRigGame) {
		t.Fatal("SAVEDIR non détecté")
	}
	st, msg := syncOne(t, r, clientUsers(t), t.TempDir(), false)
	if st != ui.Skip || !strings.Contains(msg, "SAVEDIR/SAVEFILES") {
		t.Errorf("jeu à SAVEDIR : %v %q", st, msg)
	}
}

// TestGameUsesSaveDirNeedsImageRecord : image inconnue ou sans SAVEDIR/SAVEFILES : faux.
func TestGameUsesSaveDirNeedsImageRecord(t *testing.T) {
	isolateState(t)
	conf.savesDir = t.TempDir()
	syncGlobalsSaved(t)
	if gameUsesSaveDir("Inconnu") {
		t.Error("jeu sans image connue : vrai")
	}
	img := filepath.Join(t.TempDir(), "Plain.wsquashfs")
	writeGameImage(t, "Plain", img, "CMD=Config.exe\n")
	if gameUsesSaveDir("Plain") {
		t.Error("jeu sans SAVEDIR ni SAVEFILES : vrai")
	}
	writeGameImage(t, "Files", img, "SAVEFILES=drive_c/game/save.dat\n")
	if !gameUsesSaveDir("Files") {
		t.Error("SAVEFILES non détecté")
	}
}

// writeGameImage : enregistrement de l'image d'un jeu (savesDir/.images) et son autorun de surcharge.
func writeGameImage(t *testing.T, name, img, autorun string) {
	t.Helper()
	if err := os.MkdirAll(conf.savesDir+"/.images", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conf.savesDir+"/.images/"+name, []byte(img+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if autorun != "" {
		if err := os.WriteFile(img+".autorun", []byte(autorun), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestSyncTwoMachinesSequence : A joue et synchronise (B reçoit), B joue, A synchronise (A reçoit),
// puis une nouvelle synchro ne change plus rien.
func TestSyncTwoMachinesSequence(t *testing.T) {
	r := newSyncRig(t)
	syncNoLocalLock(t)
	// B part sans sauvegarde de ce jeu.
	if err := os.RemoveAll(filepath.Join(conf.savesDir, syncRigGame)); err != nil {
		t.Fatal(err)
	}
	a := clientUsers(t)
	syncTestPopulate(t, a, "steamuser")
	stateA := t.TempDir()
	save := filepath.Join("steamuser", "Documents", "Game", "save1.sav")

	// A joue : B reçoit.
	syncTestPut(t, filepath.Join(a, save), "partie A", syncTestTime.Add(time.Hour))
	if st, msg := syncOne(t, r, a, stateA, false); st != ui.OK || msg != "envoyé à steambox" {
		t.Fatalf("A envoie : %v %q", st, msg)
	}
	if data, _ := os.ReadFile(filepath.Join(r.users, save)); string(data) != "partie A" {
		t.Fatalf("B n'a pas reçu la partie de A : %q", data)
	}
	// B joue (sur B, après la synchro) : A reçoit.
	syncTestPut(t, filepath.Join(r.users, save), "partie B", syncTestTime.Add(2*time.Hour))
	if st, msg := syncOne(t, r, a, stateA, false); st != ui.OK || msg != "reçu de steambox" {
		t.Fatalf("A reçoit : %v %q", st, msg)
	}
	if data, _ := os.ReadFile(filepath.Join(a, save)); string(data) != "partie B" {
		t.Fatalf("A n'a pas reçu la partie de B : %q", data)
	}
	// Rien à faire ensuite.
	if st, msg := syncOne(t, r, a, stateA, false); st != ui.OK || msg != "à jour" {
		t.Errorf("seconde synchro : %v %q, attendu à jour", st, msg)
	}
}

// TestSyncArchiveNamesStash : mode copy, le serveur n'a que la mise de côté (copyUsersStash) : la
// version à mettre de côté s'appelle comme le dossier réel, pas « users ».
func TestSyncArchiveNamesStash(t *testing.T) {
	r := newSyncRig(t)
	if err := os.RemoveAll(filepath.Join(conf.savesDir, syncRigGame)); err != nil {
		t.Fatal(err)
	}
	stash := copyUsersStash(syncRigGame)
	syncTestPopulate(t, stash, "steamuser")
	local := clientUsers(t)
	syncTestPut(t, filepath.Join(local, "steamuser", "Documents", "Game", "client.sav"), "client", syncTestTime.Add(time.Hour))

	_, archived, err := r.client.send(gameTarget(syncRigGame, local))
	if err != nil {
		t.Fatalf("envoi vers une mise de côté : %v", err)
	}
	if archived == "" {
		t.Fatal("mise de côté du serveur non archivée")
	}
	out, err := exec.Command("tar", "-tf", archived).CombinedOutput()
	if err != nil {
		t.Fatalf("archive illisible : %v %s", err, out)
	}
	if !strings.Contains(string(out), syncRigGame+"/steamuser/Documents/Game/save1.sav") || strings.Contains(string(out), "users/") {
		t.Errorf("membres de l'archive : %s", out)
	}
}

// TestCopyGameSavesNewerSource : copyGameSaves (refactor sur collectSaveFiles) copie ce qui est absent
// ou plus récent à la nanoseconde près, et se tait sinon.
func TestCopyGameSavesNewerSource(t *testing.T) {
	isolateState(t)
	conf.savesDir = t.TempDir()
	syncGlobalsSaved(t)
	dest := t.TempDir()
	t.Setenv("WSQUASHFS_BATOCERA_SAVES", dest)
	const game = "Game"
	img := filepath.Join(t.TempDir(), game+".wsquashfs")
	writeGameImage(t, game, img, "")

	src := filepath.Join(conf.savesDir, game, "drive_c", "users", "steamuser", "Documents", "Game", "a.sav")
	base := syncTestTime
	syncTestPut(t, src, "partie", base)
	syncTestPut(t, filepath.Join(conf.savesDir, game, "drive_c", "users", "steamuser", "Temp", "x"), "temp", base)
	target := filepath.Join(dest, game, "drive_c", "users", "steamuser", "Documents", "Game", "a.sav")

	out, _ := capteUI(t)
	run := func() string {
		out.Reset()
		copyGameSaves(game, ui.NewGames([]string{game}))
		return out.String()
	}
	if got := run(); !strings.Contains(got, "1 fichier(s) copié(s)") {
		t.Fatalf("première copie : %q", got)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("fichier non copié : %v", err)
	}
	if got := run(); !strings.Contains(got, "à jour (") {
		t.Errorf("seconde copie : %q, attendu à jour", got)
	}
	// Source plus récente d'une demi-seconde : même seconde, mais copiée (la comparaison est à la nanoseconde).
	if err := os.Chtimes(src, base.Add(500*time.Millisecond), base.Add(500*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if got := run(); !strings.Contains(got, "1 fichier(s) copié(s)") {
		t.Errorf("source plus récente à la nanoseconde : %q", got)
	}
}

// TestSyncPeersAddReplaceRemove : une machine remplacée par son empreinte, nom en double distingué,
// retrait sans tenir compte de la casse.
func TestSyncPeersAddReplaceRemove(t *testing.T) {
	fpA, fpB := strings.Repeat("A", 64), strings.Repeat("B", 64)
	cfg := syncConfig{ID: "client-machine-01"}
	cfg = addSyncPeer(cfg, syncPeer{Name: "steambox", URL: "https://10.0.0.2:8790", Token: "t1", Fingerprint: fpA})
	cfg = addSyncPeer(cfg, syncPeer{Name: "steambox", URL: "https://10.0.0.2:8791", Token: "t2", Fingerprint: fpA})
	if len(cfg.Peers) != 1 || cfg.Peers[0].URL != "https://10.0.0.2:8791" {
		t.Fatalf("même empreinte : %+v, attendue une seule machine remplacée", cfg.Peers)
	}
	cfg = addSyncPeer(cfg, syncPeer{Name: "STEAMBOX", URL: "https://10.0.0.3:8790", Fingerprint: fpB})
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
	if id := syncPeerID(syncPeer{Fingerprint: fpA}); id != strings.Repeat("a", 32) || !validSyncID(id) {
		t.Errorf("identifiant local : %q", id)
	}
}

// TestSyncPairAssociates : --sync wsq:// échange le code contre un jeton durable : la machine est
// enregistrée (nom reçu, adresse, empreinte) avec ce jeton, jamais avec le secret du code. Une
// deuxième association la remplace ; un code déjà utilisé est refusé (errPairCode).
func TestSyncPairAssociates(t *testing.T) {
	r := newSyncRig(t)
	out, _ := capteUI(t)
	const secret = "secret-sync-pair-01"
	if err := beginPairing(secret, time.Minute); err != nil {
		t.Fatal(err)
	}
	code := "wsq://" + secret + "@" + strings.TrimPrefix(r.srv.URL, "https://") + "#" + r.peer.Fingerprint
	if rc := syncPair(loadSyncConfig(), code); rc != 0 {
		t.Fatalf("association : code %d", rc)
	}
	cfg := loadSyncConfig()
	if len(cfg.Peers) != 1 {
		t.Fatalf("machines : %+v", cfg.Peers)
	}
	p := cfg.Peers[0]
	if p.URL != r.srv.URL || p.Fingerprint != r.peer.Fingerprint || p.Name == "" {
		t.Errorf("machine enregistrée : %+v", p)
	}
	if p.Token == "" || p.Token == secret {
		t.Errorf("jeton enregistré : %q (le secret du code ne doit pas l'être)", p.Token)
	}
	if !strings.Contains(out.String(), "enregistrée") || !strings.Contains(out.String(), "wsquashfs-launcher --sync") {
		t.Errorf("sortie : %q", out.String())
	}
	// Le jeton enregistré ouvre les routes de synchro.
	if _, err := newSyncClient(p, cfg.ID).list(); err != nil {
		t.Errorf("jeton durable refusé : %v", err)
	}
	// Deuxième association, avec un code neuf : elle remplace la première.
	const secret2 = "secret-sync-pair-02"
	if err := beginPairing(secret2, time.Minute); err != nil {
		t.Fatal(err)
	}
	syncPair(cfg, "wsq://"+secret2+"@"+strings.TrimPrefix(r.srv.URL, "https://")+"#"+r.peer.Fingerprint)
	if n := len(loadSyncConfig().Peers); n != 1 {
		t.Errorf("deuxième association : %d machines", n)
	}
	// L'ancien code, déjà utilisé, est refusé.
	peer, err := parsePairCode(code)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newSyncClient(peer, cfg.ID).pair(); !errors.Is(err, errPairCode) {
		t.Errorf("code réutilisé : erreur %v, attendu errPairCode", err)
	}
}

// TestSyncRunNoPeersAndUnreachable : sans machine, l'aide d'association (code 0) ; une machine éteinte
// est signalée et n'échoue pas la commande.
func TestSyncRunNoPeersAndUnreachable(t *testing.T) {
	out, _ := capteUI(t)
	isolateState(t)
	conf.savesDir, conf.cacheDir = t.TempDir(), t.TempDir()

	if rc := syncRun(syncConfig{}, "", false); rc != 0 {
		t.Errorf("sans machine : code %d", rc)
	}
	if !strings.Contains(out.String(), "Aucune machine associée") || !strings.Contains(out.String(), "wsquashfs-launcher --pair") {
		t.Errorf("sans machine : sortie %q", out.String())
	}
	out.Reset()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	cfg := syncConfig{ID: "client-machine-01", Peers: []syncPeer{{Name: "eteinte", URL: "https://" + addr, Token: "t", Fingerprint: strings.Repeat("0", 64)}}}
	if rc := syncRun(cfg, "", true); rc != 0 {
		t.Errorf("machine éteinte : code %d, attendu 0", rc)
	}
	if !strings.Contains(out.String(), "injoignable (éteinte ou hors du réseau local)") || !strings.Contains(out.String(), "Versions écartées en cas de conflit") {
		t.Errorf("machine éteinte : sortie %q", out.String())
	}
}

// TestSyncCommandAction : --sync termine le parsing (action « sync », reste en arguments), ne lance
// pas de jeu, et l'agent l'appelle comme action de maintenance (--sync, jeu facultatif).
func TestSyncCommandAction(t *testing.T) {
	for _, args := range [][]string{{"--sync"}, {"--sync", "Jeu"}, {"--sync", "wsq://tok@h:1#AB"}, {"--sync", "forget", "x"}} {
		o, err := parseArgs(args)
		if err != nil || o.action != "sync" || len(o.cmdArgs) != len(args)-1 {
			t.Errorf("%q : action %v, arguments %v, erreur %v", args, o.action, o.cmdArgs, err)
		}
		if launchesGame(args) {
			t.Errorf("%q compté comme un lancement de jeu", args)
		}
	}
	if spec, ok := maintenanceActions["sync"]; !ok || spec.game != gameOptional || spec.args[0] != "--sync" {
		t.Errorf("« sync » de l'agent : %+v, attendu --sync avec jeu facultatif", spec)
	}
}

// TestSyncGameNothingAnywhere : aucune sauvegarde ici ni là-bas : ligne ignorée, rien n'est écrit.
func TestSyncGameNothingAnywhere(t *testing.T) {
	r := newSyncRig(t)
	syncNoLocalLock(t)
	if err := os.RemoveAll(filepath.Join(conf.savesDir, syncRigGame)); err != nil {
		t.Fatal(err)
	}
	st, msg := syncOne(t, r, clientUsers(t), t.TempDir(), false)
	if st != ui.Skip || msg != "aucune sauvegarde ici ni sur steambox" {
		t.Errorf("rien de part et d'autre : %v %q", st, msg)
	}
}
