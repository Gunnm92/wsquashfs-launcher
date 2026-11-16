package main

// Manettes (script bash, section 7) : choix entre hidraw et XInput pour les manettes Sony, pont
// DualSense Bluetooth → USB (dualsense-usb-bridge), correctif GE-Proton, réglage du registre.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
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

// findDualsenseBridge : dualsense-usb-bridge dans le PATH, sinon à côté du
// lanceur (dépôt).
func findDualsenseBridge() string {
	if p, err := exec.LookPath("dualsense-usb-bridge"); err == nil {
		return p
	}
	p := filepath.Dir(conf.selfPath) + "/tools/dualsense-usb-bridge/dualsense-usb-bridge"
	if isExecutable(p) {
		return p
	}
	return ""
}

// startDualsenseBridge : DualSense Bluetooth présentée en DualSense USB pendant
// le jeu (HIDRAW=1). Faux s'il n'y a pas de DualSense Bluetooth ou pas de pont.
func startDualsenseBridge() bool {
	bt, _ := filepath.Glob(hidrawSysfs() + "/*/device/uevent")
	isBT := false
	for _, u := range bt {
		if data, err := os.ReadFile(u); err == nil && strings.Contains("\n"+string(data), "\nHID_ID=0005:0000054C:00000CE6\n") {
			isBT = true
		}
	}
	if !isBT {
		return false
	}
	bridge := findDualsenseBridge()
	if bridge == "" {
		warn("dualsense-usb-bridge introuvable, DualSense Bluetooth en direct")
		return false
	}
	ready := fmt.Sprintf("%s/dualsense-usb-bridge.%d", envOr("TMPDIR", "/tmp"), os.Getpid())
	cmd := exec.Command(bridge, "--hide", "--parent", strconv.Itoa(os.Getpid()), "--ready-file", ready)
	if err := cmd.Start(); err != nil {
		return false
	}
	launch.dsBridgePID = cmd.Process.Pid
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	for i := 0; i < 30; i++ {
		if _, err := os.Stat(ready); err == nil {
			_ = os.Remove(ready)
			return true
		}
		select {
		case <-exited:
			_ = os.Remove(ready)
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
	stopDualsenseBridge()
	_ = os.Remove(ready)
	return false
}

// stopDualsenseBridge : arrête le pont DualSense.
func stopDualsenseBridge() {
	if launch.dsBridgePID == 0 {
		return
	}
	_ = syscall.Kill(launch.dsBridgePID, syscall.SIGTERM)
	launch.dsBridgePID = 0
}

// chooseHidrawMode : HIDRAW=1 (pont USB si DualSense Bluetooth), HIDRAW=bt
// (DualSense Bluetooth en direct), sinon XInput.
func chooseHidrawMode() {
	launch.hidrawDisable = "1"
	launch.hidrawMsg = "XInput, manette Xbox"
	mode := autorunVar("HIDRAW", "0")
	if mode != "1" && mode != "bt" {
		return
	}
	if !hidrawUsable() {
		launch.hidrawMsg = "XInput, manette Xbox (hidraw demandé : " + launch.hidrawReason + ")"
		return
	}
	launch.hidrawDisable = "0"
	launch.hidrawMsg = "DualSense/DualShock en direct (hidraw)"
	if mode == "1" && startDualsenseBridge() {
		launch.hidrawMsg = "DualSense USB (dualsense-usb-bridge, depuis la DualSense Bluetooth)"
	}
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
