package main

// Commande --list : jeux connus, jeux ayant des sauvegardes et leur état.

import (
	"os"
	"slices"
	"strings"
)

// savedGames : jeux qui ont une couche de sauvegardes.
func savedGames() []string {
	return visibleDirs(conf.savesDir)
}

// visibleDirs : sous-dossiers non cachés de d (glob */ : les noms en point sont
// ignorés, comme .work et .images).
func visibleDirs(d string) []string {
	entries, _ := os.ReadDir(d)
	var out []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	return out
}

// ── --list (13/10) ─────────────────────────────────────────────────────

// knownGames : jeux avec sauvegardes, copie de travail, ou image enregistrée.
func knownGames() []string {
	set := map[string]bool{}
	for _, g := range savedGames() {
		set[g] = true
	}
	for _, g := range visibleDirs(conf.cacheDir + "/wine") {
		set[g] = true
	}
	imgs, _ := os.ReadDir(conf.savesDir + "/.images")
	for _, e := range imgs {
		if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			set[e.Name()] = true
		}
	}
	var out []string
	for g := range set {
		out = append(out, g)
	}
	slices.Sort(out)
	return out
}

// gameHasSaves : le jeu a encore des sauvegardes de partie, au sens de --save, --sync et de la
// suppression par le manager (collectSaveFiles : Documents, Saved Games, AppData des profils, caches et
// comptes système exclus). Le reste de l'overlay (prefix, fichiers temporaires…) ne compte pas.
func gameHasSaves(name string) bool {
	return len(collectSaveFiles(syncUsersDir(name))) > 0
}

// gameState : état d'un jeu (en cours, verrouillé, monté, -).
func gameState(name string) string {
	switch {
	case gameInUse(name):
		return "en cours"
	case gameLocked(name):
		return "verrouillé"
	case isMountPoint(conf.cacheDir + "/wine/" + name):
		return "monté"
	}
	return "-"
}

// cmdList : --list.
func cmdList() {
	games := knownGames()
	if len(games) == 0 {
		uiStatusLine(stSkip, "Aucun jeu connu (aucun lancement enregistré dans "+conf.savesDir+")")
		return
	}
	var rows [][]string
	var orphanEmpty, orphanSaves []string
	for _, g := range games {
		version, size, image := "-", "aucune", ""
		img := gameImage(g) // mémorisée, sinon <jeu>.wsquashfs retrouvée dans les roms/
		if img == "" || !pathExists(img) {
			// Image renommée ou retirée : pas de ligne, un rappel en fin de liste (--clean s'il ne
			// reste que l'overlay, réinstaller le jeu s'il reste des sauvegardes)
			if gameHasSaves(g) {
				orphanSaves = append(orphanSaves, g)
			} else {
				orphanEmpty = append(orphanEmpty, g)
			}
			continue
		}
		image = img
		version = gameVersion(img)
		if version == "" {
			version = "-"
		}
		if isDirPath(conf.savesDir + "/" + g) {
			size = duHuman(conf.savesDir + "/" + g)
		}
		rows = append(rows, []string{truncate(g, uiNameMax), gameState(g), version, size, image})
	}
	// Taille totale : sur la ligne « Sauvegardes » en fin de liste.
	uiTitle("Jeux connus", plural(len(rows), "jeu", "jeux"))
	if len(rows) > 0 {
		uiTable([]string{"Jeu", "État", "Version", "Sauvegardes", "Image"}, rows, []int{3}, uiStateCell(1, 4))
	}
	uiOut("")
	uiKVLine("Sauvegardes", conf.savesDir+" ("+duHuman(conf.savesDir)+" au total)")
	if len(orphanEmpty) > 0 {
		uiNote(plural(len(orphanEmpty), "jeu", "jeux") + " sans image ni sauvegarde, non listé(s) : wsquashfs-launcher --clean les retire.")
	}
	if len(orphanSaves) > 0 {
		uiNote(plural(len(orphanSaves), "jeu", "jeux") + " sans image mais avec des sauvegardes, non listé(s) : réinstaller le jeu (ou --erase pour tout effacer) :")
		for _, g := range orphanSaves {
			line := "  " + g
			if near := nearImages(g, recordedImage(g)); len(near) > 0 {
				line += " (image proche : " + near[0] + ")"
			}
			uiNote(line)
		}
	}
}
