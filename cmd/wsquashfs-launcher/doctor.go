package main

// --doctor (13/10) : ce qui est installé, ce qui manque et ce que cela change
// (script bash, section 8).

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func doctorOK(msg string)   { fmt.Println("  ✓ " + msg) }
func doctorFail(msg string) { fmt.Println("  ✗ " + msg) }
func doctorNote(msg string) { fmt.Println("  – " + msg) }

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
func cmdDoctor() int {
	fatal := false
	fmt.Println("Montage des images")
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

	fmt.Println("Wine / Proton")
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
			fmt.Println("      " + filepath.Base(r) + "  " + r)
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

	fmt.Println("Rendu")
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

	fmt.Println("Manettes")
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

	fmt.Println("Dossiers")
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

	fmt.Println()
	if fatal {
		fmt.Println("✗ Aucun jeu ne peut être lancé : voir les points ✗ ci-dessus")
		return 1
	}
	fmt.Println("✓ Prêt à lancer des jeux")
	return 0
}
