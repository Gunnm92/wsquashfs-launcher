package main

// Image d'un jeu (gameImage, recordedImage) et index des images du dossier roms/.

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// gameImage : image du jeu d'après savesDir/.images/<jeu> (dernier lancement) ; "" si inconnue.
func gameImage(name string) string {
	recorded := recordedImage(name)
	if recorded != "" && pathExists(recorded) {
		return recorded
	}
	// Rien de mémorisé (sauvegardes reçues par --sync, jeu jamais lancé ici) ou image déplacée :
	// <jeu>.wsquashfs dans un dossier de système des roms/.
	if found := romsIndex(recorded).exact[name]; found != "" {
		return found
	}
	return recorded
}

// recordedImage : chemin de l'image mémorisé au lancement (savesDir/.images/<jeu>), "" sinon.
func recordedImage(name string) string {
	if data, err := os.ReadFile(conf.savesDir + "/.images/" + name); err == nil {
		return strings.TrimRight(string(data), "\n")
	}
	return ""
}

// imagesIndex : images des dossiers roms/ : nom exact du jeu → .wsquashfs, et nom rapproché (sans
// casse, ponctuation ni passages entre parenthèses ou crochets) → .wsquashfs ou .squashfs.
type imagesIndex struct {
	exact map[string]string
	loose map[string][]string
}

var (
	romsIndexDone map[string]imagesIndex // par liste de dossiers roms/ : un parcours par commande
)

var (
	looseBracketsRe = regexp.MustCompile(`\([^)]*\)|\[[^\]]*\]`)
	looseOtherRe    = regexp.MustCompile(`[^0-9a-z]+`)
)

// looseGameName : « After Dark (demul) » → « afterdark ».
func looseGameName(name string) string {
	return looseOtherRe.ReplaceAllString(looseBracketsRe.ReplaceAllString(strings.ToLower(name), ""), "")
}

// romsIndex : index des images des dossiers roms/ usuels (celui du chemin mémorisé, ceux de l'agent,
// puis les emplacements habituels), un dossier de système par sous-dossier ; « win » d'abord.
func romsIndex(recorded string) imagesIndex {
	var roots []string
	if recorded != "" {
		roots = append(roots, filepath.Dir(filepath.Dir(recorded))) // même roms/ que le chemin mémorisé
	}
	roots = append(append(roots, agentSrv().LoadConfig().Roms...), agentSrv().DefaultRoms())
	key := strings.Join(roots, "\x00")
	if idx, ok := romsIndexDone[key]; ok {
		return idx
	}
	idx := imagesIndex{exact: map[string]string{}, loose: map[string][]string{}}
	seen := map[string]bool{}
	for _, root := range roots {
		if seen[root] {
			continue
		}
		seen[root] = true
		systems, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		slices.SortStableFunc(systems, func(a, b os.DirEntry) int {
			if a.Name() == "win" && b.Name() != "win" {
				return -1
			}
			if b.Name() == "win" && a.Name() != "win" {
				return 1
			}
			return 0
		})
		for _, sys := range systems {
			if !sys.IsDir() {
				continue
			}
			files, err := os.ReadDir(filepath.Join(root, sys.Name()))
			if err != nil {
				continue
			}
			for _, f := range files {
				full := filepath.Join(root, sys.Name(), f.Name())
				switch {
				case strings.HasSuffix(f.Name(), ".wsquashfs"):
					n := strings.TrimSuffix(f.Name(), ".wsquashfs")
					if idx.exact[n] == "" {
						idx.exact[n] = full
					}
					idx.loose[looseGameName(n)] = append(idx.loose[looseGameName(n)], full)
				case strings.HasSuffix(f.Name(), ".squashfs"):
					n := strings.TrimSuffix(f.Name(), ".squashfs")
					idx.loose[looseGameName(n)] = append(idx.loose[looseGameName(n)], full)
				}
			}
		}
	}
	if romsIndexDone == nil {
		romsIndexDone = map[string]imagesIndex{}
	}
	romsIndexDone[key] = idx
	return idx
}

// nearImages : images au nom proche d'un jeu dont l'image n'existe plus sous ce nom (renommée,
// « (demul) » ajouté, ou remplacée par un .squashfs d'un autre émulateur).
func nearImages(name, recorded string) []string {
	return romsIndex(recorded).loose[looseGameName(name)]
}

// gameUsesSaveDir : le jeu déclare SAVEDIR ou SAVEFILES (sauvegardes hors du prefix, déjà à leur
// place : ni la copie vers la ludothèque ni la synchro ne les touchent). Image inconnue : faux.
// Pose wsquashfsFile, gameName, autorunOverride et les couches du jeu.
func gameUsesSaveDir(name string) bool {
	img := gameImage(name)
	if img == "" {
		return false
	}
	launch.wsquashfsFile = img
	launch.gameName = name
	launch.autorunOverride = strings.TrimSuffix(img, "/") + ".autorun"
	if !isFile(launch.autorunOverride) {
		launch.autorunOverride = ""
	}
	if pathExists(img) {
		loadLayers(imageAutorun(img))
	} else {
		loadLayers("")
	}
	layerLookup("SAVEDIR", "")
	savedir := layers.lookupVal
	layerLookup("SAVEFILES", "")
	return savedir+layers.lookupVal != ""
}
