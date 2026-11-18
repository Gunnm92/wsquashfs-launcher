package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// shortNameRig : dossier d'installation temporaire avec un lanceur installé ; HOME
// et PATH isolés (aucune vraie commande ws, aucun vrai ~/.bashrc touché).
func shortNameRig(t *testing.T) (dir, target string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("PATH", t.TempDir())
	dir = t.TempDir()
	target = filepath.Join(dir, "wsquashfs-launcher")
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, target
}

// TestInstallShortNameLien : ws pointe vers le lanceur (lien relatif, il suit le
// dossier) ; une seconde pose ne change rien.
func TestInstallShortNameLien(t *testing.T) {
	dir, target := shortNameRig(t)
	created, err := installShortName(dir, target)
	if err != nil || !created {
		t.Fatalf("première pose : créé=%v, err=%v", created, err)
	}
	if lt, _ := os.Readlink(filepath.Join(dir, "ws")); lt != "wsquashfs-launcher" {
		t.Errorf("cible du lien : %q, attendu wsquashfs-launcher", lt)
	}
	if !isOwnShortName(filepath.Join(dir, "ws"), target) {
		t.Error("le lien posé n'est pas reconnu comme le nôtre")
	}
	created, err = installShortName(dir, target)
	if err != nil || created {
		t.Errorf("seconde pose : créé=%v, err=%v (attendu rien à faire)", created, err)
	}
}

// TestInstallShortNameNeEcrasePasFichier : un ws étranger (fichier ordinaire) reste
// intact, la pose est refusée avec shortNameTaken.
func TestInstallShortNameNeEcrasePasFichier(t *testing.T) {
	dir, target := shortNameRig(t)
	link := filepath.Join(dir, "ws")
	if err := os.WriteFile(link, []byte("autre commande"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := installShortName(dir, target)
	var taken shortNameTaken
	if !errors.As(err, &taken) {
		t.Fatalf("erreur %v, attendu shortNameTaken", err)
	}
	if data, _ := os.ReadFile(link); string(data) != "autre commande" {
		t.Errorf("ws étranger modifié : %q", data)
	}
}

// TestInstallShortNameNeEcrasePasLienEtranger : un lien vers une autre commande
// n'est pas remplacé.
func TestInstallShortNameNeEcrasePasLienEtranger(t *testing.T) {
	dir, target := shortNameRig(t)
	other := filepath.Join(t.TempDir(), "autre-ws")
	if err := os.WriteFile(other, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "ws")
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	if _, err := installShortName(dir, target); err == nil {
		t.Fatal("pose acceptée sur un lien étranger")
	}
	if lt, _ := os.Readlink(link); lt != other {
		t.Errorf("lien étranger modifié : %q", lt)
	}
	if isOwnShortName(link, target) {
		t.Error("lien étranger reconnu comme le nôtre")
	}
}

// TestInstallShortNamePrisDansPath : une autre commande ws dans le PATH (hors
// installDir) bloque la pose ; le message nomme la commande prise.
func TestInstallShortNamePrisDansPath(t *testing.T) {
	dir, target := shortNameRig(t)
	pathDir := t.TempDir()
	foreign := filepath.Join(pathDir, "ws")
	if err := os.WriteFile(foreign, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir)
	_, err := installShortName(dir, target)
	var taken shortNameTaken
	if !errors.As(err, &taken) || taken.by != foreign {
		t.Fatalf("erreur %v, attendu ws pris par %s", err, foreign)
	}
	if _, err := os.Lstat(filepath.Join(dir, "ws")); err == nil {
		t.Error("lien posé malgré le ws du PATH")
	}
}

// TestInstallShortNamePathVersNotre : un ws du PATH qui mène déjà à notre
// lanceur ne bloque pas la pose.
func TestInstallShortNamePathVersNotre(t *testing.T) {
	dir, target := shortNameRig(t)
	pathDir := t.TempDir()
	if err := os.Symlink(target, filepath.Join(pathDir, "ws")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir)
	created, err := installShortName(dir, target)
	if err != nil || !created {
		t.Errorf("créé=%v, err=%v : attendu une pose", created, err)
	}
}

// TestUninstallShortName : uninstall retire ws s'il pointe vers notre lanceur,
// et laisse un ws étranger en place.
func TestUninstallShortName(t *testing.T) {
	saved := []*string{&installDir, &wsqData, &wineInstallDir, &umuInstallDir, &dxvkDir, &vkd3dDir,
		&d7vkDir, &nvapiDir, &fsr4Dir, &protonInstallDir, &desktopFile}
	vals := make([]string, len(saved))
	for i, p := range saved {
		vals[i] = *p
	}
	savedDry := dryRun
	t.Cleanup(func() {
		for i, p := range saved {
			*p = vals[i]
		}
		dryRun = savedDry
	})

	t.Run("lien à nous", func(t *testing.T) {
		dir, target := shortNameRig(t)
		setupInit()
		installDir, dryRun = dir, false
		if err := os.Symlink("wsquashfs-launcher", filepath.Join(dir, "ws")); err != nil {
			t.Fatal(err)
		}
		uninstall()
		if pathExists(filepath.Join(dir, "ws")) {
			t.Error("ws à nous non retiré")
		}
		if pathExists(target) {
			t.Error("lanceur non retiré")
		}
	})

	t.Run("ws étranger", func(t *testing.T) {
		dir, target := shortNameRig(t)
		setupInit()
		installDir, dryRun = dir, false
		link := filepath.Join(dir, "ws")
		if err := os.WriteFile(link, []byte("autre commande"), 0o755); err != nil {
			t.Fatal(err)
		}
		uninstall()
		if data, _ := os.ReadFile(link); string(data) != "autre commande" {
			t.Errorf("ws étranger retiré ou modifié : %q", data)
		}
		if pathExists(target) {
			t.Error("lanceur non retiré")
		}
	})
}
