package main

// Manettes (script bash, section 7) : choix entre hidraw et XInput pour les manettes Sony,
// correctif GE-Proton, réglage du registre. Paramétrage du préfixe seulement : la DualSense
// elle-même (USB, haptiques) vient de Sunshine (sunshine-ds / ds5-usbip).

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ── 7. Manettes ────────────────────────────────────────────────────────

var reSonyHid = regexp.MustCompile(`(?m)^HID_ID=[0-9A-Fa-f]*:0000054C:`)

// hidrawSysfs : /sys/class/hidraw, surchargeable (tests).
func hidrawSysfs() string {
	return envOr("HIDRAW_SYSFS", "/sys/class/hidraw")
}

// hidrawUsable : manettes Sony lisibles par hidraw. Faux avec la raison dans
// hidrawReason si une manette est illisible ou absente.
func hidrawUsable() bool {
	uevents, _ := filepath.Glob(hidrawSysfs() + "/*/device/uevent")
	found := false
	for _, u := range uevents {
		data, err := os.ReadFile(u)
		if err != nil || !reSonyHid.Match(data) {
			continue
		}
		found = true
		n := filepath.Base(filepath.Dir(filepath.Dir(u)))
		if f, err := os.Open("/dev/" + n); err != nil {
			launch.hidrawReason = "/dev/" + n + " illisible"
			return false
		} else {
			f.Close()
		}
	}
	if !found {
		launch.hidrawReason = "aucune manette Sony"
		return false
	}
	return true
}

// chooseHidrawMode : HIDRAW=1, manettes Sony en direct par hidraw (le jeu gère
// la DualSense lui-même : gâchettes, haptiques) ; sinon XInput.
func chooseHidrawMode() {
	launch.hidrawDisable = "1"
	launch.hidrawMsg = "XInput, manette Xbox"
	mode := autorunVar("HIDRAW", "0")
	if mode != "1" {
		return
	}
	if !hidrawUsable() {
		launch.hidrawMsg = "XInput, manette Xbox (hidraw demandé : " + launch.hidrawReason + ")"
		return
	}
	launch.hidrawDisable = "0"
	launch.hidrawMsg = "DualSense/DualShock en direct (hidraw)"
}

// applyGeProtonSonyFix : GE-Proton ignore les manettes Sony en SDL ; hidraw
// coupé, sa conversion XInput les présente en XInput (PROTON_SONY_HIDRAW_XINPUT).
func applyGeProtonSonyFix() {
	protonRoot := ""
	if strings.HasSuffix(launch.wineBinary, "/files/bin/wine") {
		protonRoot = strings.TrimSuffix(launch.wineBinary, "/files/bin/wine")
	}
	if launch.hidrawDisable != "1" || protonRoot == "" {
		return
	}
	lib, err := os.ReadFile(protonRoot + "/files/lib/wine/x86_64-unix/winebus.so")
	if err != nil || !strings.Contains(string(lib), "hidraw handles native reports") || !hidrawUsable() {
		return
	}
	launch.hidrawDisable = "0"
	os.Setenv("PROTON_SONY_HIDRAW_XINPUT", "1")
	launch.hidrawMsg = "XInput, manette Xbox (manette Sony par hidraw, convertie par GE-Proton)"
}

// writeHidrawSetting : DisableHidraw inscrit dans le registre s'il diffère.
func writeHidrawSetting() {
	want := `"DisableHidraw"=dword:0000000` + launch.hidrawDisable
	for _, f := range []string{launch.winePoint + "/system.reg", launch.winePoint + "/pfx/system.reg"} {
		if data, err := os.ReadFile(f); err == nil && strings.Contains(string(data), want) {
			return
		}
	}
	_ = runWineTool("reg", "add", `HKLM\System\CurrentControlSet\Services\winebus`,
		"/v", "DisableHidraw", "/t", "REG_DWORD", "/d", launch.hidrawDisable, "/f")
	_ = runWineserver(launch.winePoint, "-k")
	_ = runWineserver(launch.winePoint, "-w")
}

// configureControllers : mode des manettes, correctif GE-Proton, registre.
func configureControllers() {
	chooseHidrawMode()
	applyGeProtonSonyFix()
	writeHidrawSetting()
}
