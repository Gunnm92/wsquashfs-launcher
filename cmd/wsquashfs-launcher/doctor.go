package main

// --doctor (13/10) : ce qui est installé, ce qui manque et ce que cela change
// (script bash, section 8).

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// Rapport de --doctor : construit une fois, rendu en texte ou en JSON (agent).
type doctorItem struct {
	Status  string   `json:"status"` // ok | missing | optional
	Text    string   `json:"text"`
	Details []string `json:"details,omitempty"`
}

type doctorSection struct {
	Title string       `json:"title"`
	Items []doctorItem `json:"items"`
}

type runnerEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

var (
	docSections []doctorSection
	docRunners  []runnerEntry
)

// docSection : nouvelle section (titre affiché sans retrait).
func docSection(title string) {
	docSections = append(docSections, doctorSection{Title: title})
}

func docAdd(status, msg string) {
	if len(docSections) == 0 {
		docSection("")
	}
	last := &docSections[len(docSections)-1]
	last.Items = append(last.Items, doctorItem{Status: status, Text: msg})
}

func doctorOK(msg string)   { docAdd("ok", msg) }
func doctorFail(msg string) { docAdd("missing", msg) }
func doctorNote(msg string) { docAdd("optional", msg) }

// docRunner : runner installé, listé sous le dernier point (texte et JSON).
func docRunner(name, path string) {
	docRunners = append(docRunners, runnerEntry{Name: name, Path: path})
	last := &docSections[len(docSections)-1]
	item := &last.Items[len(last.Items)-1]
	item.Details = append(item.Details, name+"  "+path)
}

// doctorTool : commande présente (ok) ou absente (fail ou note selon level).
func doctorTool(name, role, level string) {
	if p, err := exec.LookPath(name); err == nil {
		doctorOK(name + " (" + p + ") : " + role)
		return
	}
	if level == "fail" {
		doctorFail(name + " absent : " + role)
	} else {
		doctorNote(name + " absent : " + role)
	}
}

// writable : accès en écriture (test -w).
func writable(p string) bool {
	return syscall.Access(p, 2) == nil
}

// cmdDoctor : --doctor. Code 1 si aucun jeu ne peut être lancé.
func runDoctorChecks() bool {
	fatal := false
	docSection("Montage des images")
	if st, err := os.Stat("/dev/fuse"); err == nil && st.Mode()&os.ModeCharDevice != 0 && writable("/dev/fuse") {
		doctorOK("/dev/fuse accessible")
	} else {
		doctorFail("/dev/fuse inaccessible : mode copy seulement (conteneur : --device /dev/fuse)")
	}
	doctorTool("squashfuse", "montage de l'image", "fail")
	if reason := kernelOverlayUnavailable(); reason != "" {
		doctorNote("overlay noyau indisponible : " + reason + " (fuse-overlayfs à la place)")
	} else if probe := kernelOverlayProbe(); probe == "" {
		doctorOK("overlay noyau (namespace utilisateur) : une couche FUSE au lieu de deux")
	} else {
		doctorNote("overlay noyau indisponible : " + probe + " (fuse-overlayfs à la place)")
	}
	doctorTool("fuse-overlayfs", "mode overlay, sauvegardes séparées (sinon mode copy)", "fail")
	doctorTool("unsquashfs", "mode copy, --info, --install", "fail")
	doctorTool("mksquashfs", "--pack", "note")
	doctorTool("getfattr", "--clean (doublons des sauvegardes)", "note")
	doctorTool("flock", "verrou par jeu (double lancement)", "note")
	_, sqErr := exec.LookPath("squashfuse")
	_, unErr := exec.LookPath("unsquashfs")
	if sqErr != nil && unErr != nil {
		fatal = true
	}

	docSection("Wine / Proton")
	bin := findWineBinary()
	if bin != "" {
		doctorOK("Wine par défaut : " + runnerName(bin) + " " + runnerVersion(bin) + " (" + bin + ")")
		if wineSupportsWin32(bin) {
			doctorOK("prefixes 32 bits (win32) pris en charge")
		} else if alt := findWin32Wine(); alt != "" {
			doctorOK("prefixes 32 bits : " + runnerName(alt) + " (Wine par défaut WoW64 seul)")
		} else {
			doctorFail("prefixes 32 bits impossibles : Wine WoW64 seul, aucun Wine classique installé")
		}
	} else {
		doctorFail("aucun Wine : seuls les jeux lancés par Proton (umu) fonctionnent")
	}
	runners := listWineRunners()
	if len(runners) > 0 {
		doctorOK("runners installés (--wine) :")
		for _, r := range runners {
			docRunner(filepath.Base(r), r)
		}
	} else {
		doctorNote("aucun runner installé dans /opt, /usr/wine, ~/.local/share/wsquashfs/wine ni Steam/Heroic")
	}
	umu := findUmuRun()
	if umu != "" {
		doctorOK("umu-run (" + umu + ") : prefixes Proton et jeux seuls lancés par Proton")
	} else {
		doctorNote("umu-run absent : prefixes Proton lancés directement par Wine (dégradé)")
	}
	if bin == "" && !(umu != "" && findProtonRunner("") != "") {
		fatal = true
	}

	docSection("Rendu")
	if d := findDxvkDir(); d != "" {
		doctorOK("DXVK (" + d + ")")
	} else {
		doctorFail("DXVK absent : Direct3D 8-11 en OpenGL (llvmpipe possible)")
	}
	if d := findVkd3dDir(); d != "" {
		doctorOK("VKD3D-Proton (" + d + ")")
	} else {
		doctorFail("VKD3D-Proton absent : Direct3D 12 indisponible")
	}
	if d := findD7vk(); d != "" {
		doctorOK("d7vk (" + d + ")")
	} else {
		doctorNote("d7vk absent : DirectDraw en OpenGL")
	}
	detectGpus()
	switch {
	case hasNvidia == "1" && hasAMD == "1":
		doctorOK("GPU : NVIDIA et AMD détectés (réglages NVIDIA et AMD actifs)")
	case hasNvidia == "1":
		doctorOK("GPU : NVIDIA détecté")
	case hasAMD == "1":
		doctorOK("GPU : AMD détecté (aucun réglage NVIDIA)")
	default:
		doctorNote("GPU : aucun fournisseur reconnu dans " + envOr("WSQUASHFS_DRM_SYSFS", "/sys/class/drm"))
	}
	if hasNvidia == "1" {
		if d := findNvapiDir(); d != "" {
			doctorOK("dxvk-nvapi (" + d + ") : NVAPI active en auto")
			if isFile(d + "/layer/VkLayer_DXVK_NVAPI_reflex.json") {
				doctorOK("couche Reflex présente : active en auto avec NVAPI")
			} else {
				doctorNote("couche Reflex absente : VKREFLEX sans effet")
			}
		} else {
			doctorNote("dxvk-nvapi absent : NVAPI sans effet (DLSS indisponible)")
			doctorNote("couche Reflex absente : VKREFLEX sans effet")
		}
	}
	if hasAMD == "1" {
		icds, _ := filepath.Glob(envOr("WSQUASHFS_VK_ICD_DIR", "/usr/share/vulkan/icd.d") + "/radeon_icd*.json")
		if len(icds) > 0 {
			doctorOK("RADV (pilote Vulkan Mesa) présent")
		} else {
			doctorNote("RADV absent : vulkan-radeon (Arch) ou mesa-vulkan-drivers (Debian)")
		}
		if d := findFsr4Dll(); d != "" {
			doctorOK("FSR4 : " + d + " (FSR4=1 pour l'activer, défaut désactivé)")
		} else {
			doctorNote("FSR4 : amdxcffx64.dll absente (install.sh peut la copier depuis proton-cachyos)")
		}
	}

	docSection("Manettes")
	if hidrawUsable() {
		doctorOK("manette Sony lisible par hidraw (HIDRAW=1 possible)")
	} else {
		doctorNote("hidraw : " + hidrawReason + " (HIDRAW=1 retombe en XInput)")
	}
	if b := findDualsenseBridge(); b != "" {
		doctorOK("dualsense-usb-bridge (" + b + ")")
	} else {
		doctorNote("dualsense-usb-bridge absent : DualSense Bluetooth en direct avec HIDRAW=1")
	}

	docSection("Dossiers")
	for _, d := range []string{savesDir, cacheDir} {
		if !pathExists(d) {
			doctorNote(d + " : pas encore créé")
		} else if writable(d) {
			doctorOK(d + " (" + duHuman(d) + ")")
		} else {
			doctorFail(d + " : pas d'accès en écriture")
		}
	}
	if f := machineLayerFile(); isFile(f) {
		doctorOK("réglages machine : " + f + " (" + overrideKeys(f) + ")")
	}

	return fatal
}

// cmdDoctor : --doctor, texte. Code 1 si aucun jeu ne peut être lancé.
func cmdDoctor() int {
	fatal := runDoctorChecks()
	printDoctorText(fatal)
	if fatal {
		return 1
	}
	return 0
}

// printDoctorText : rendu texte du rapport (sortie humaine).
func printDoctorText(fatal bool) {
	for _, sec := range docSections {
		if sec.Title != "" {
			fmt.Println(sec.Title)
		}
		for _, it := range sec.Items {
			mark := map[string]string{"ok": "✓", "missing": "✗", "optional": "–"}[it.Status]
			fmt.Println("  " + mark + " " + it.Text)
			for _, d := range it.Details {
				fmt.Println("      " + d)
			}
		}
	}
	fmt.Println()
	if fatal {
		fmt.Println("✗ Aucun jeu ne peut être lancé : voir les points ✗ ci-dessus")
		return
	}
	fmt.Println("✓ Prêt à lancer des jeux")
}

// doctorJSON : rapport structuré (--doctor --json, route /v1/doctor de l'agent).
type doctorJSON struct {
	Code     int             `json:"code"`
	Sections []doctorSection `json:"sections"`
	Runners  []runnerEntry   `json:"runners"`
	Verdict  string          `json:"verdict"`
}

func printDoctorJSON(fatal bool) {
	out := doctorJSON{Sections: docSections, Runners: docRunners, Verdict: "✓ Prêt à lancer des jeux"}
	if fatal {
		out.Code = 1
		out.Verdict = "✗ Aucun jeu ne peut être lancé : voir les points ✗ ci-dessus"
	}
	data, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(data))
}

// cmdDoctorMain : --doctor [--json]. Le JSON est le rapport structuré, lu par
// l'agent et par wsquashfs-manager ; le texte est la sortie humaine.
func cmdDoctorMain(args []string) int {
	for _, a := range args {
		if a == "--json" {
			fatal := runDoctorChecks()
			printDoctorJSON(fatal)
			if fatal {
				return 1
			}
			return 0
		}
	}
	return cmdDoctor()
}
