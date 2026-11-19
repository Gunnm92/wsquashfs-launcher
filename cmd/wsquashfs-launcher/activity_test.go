package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Extrait de /proc/bus/input/devices : DualSense en Bluetooth, manette Xbox en USB, pad virtuel de
// Steam Input (ignoré), clavier (pas de js), Steam Deck.
const inputDevicesSample = `I: Bus=0005 Vendor=054c Product=0ce6 Version=8100
N: Name="DualSense Wireless Controller"
P: Phys=aa:bb
S: Sysfs=/devices/pci0000:00/bluetooth/hci0/0005:054C:0CE6.0004/input/input21
U: Uniq=11:22
H: Handlers=event20 js0
B: PROP=0

I: Bus=0005 Vendor=054c Product=0ce6 Version=8100
N: Name="DualSense Wireless Controller Motion Sensors"
S: Sysfs=/devices/pci0000:00/bluetooth/hci0/0005:054C:0CE6.0004/input/input22
H: Handlers=event21
B: PROP=40

I: Bus=0003 Vendor=045e Product=0b12 Version=0511
N: Name="Microsoft Xbox Series S|X Controller"
S: Sysfs=/devices/pci0000:00/usb1/1-2/1-2:1.0/input/input30
H: Handlers=event25 js1

I: Bus=0003 Vendor=28de Product=11ff Version=0001
N: Name="Microsoft X-Box 360 pad 0"
S: Sysfs=/devices/virtual/input/input40
H: Handlers=event30 js2

I: Bus=0011 Vendor=0001 Product=0001 Version=ab41
N: Name="AT Translated Set 2 keyboard"
S: Sysfs=/devices/platform/i8042/serio0/input/input3
H: Handlers=sysrq kbd event3 leds

I: Bus=0003 Vendor=28de Product=1205 Version=0111
N: Name="Steam Deck"
S: Sysfs=/devices/pci0000:00/usb3/3-3/input/input12
H: Handlers=event12 js3
`

func TestConnectedControllers(t *testing.T) {
	p := filepath.Join(t.TempDir(), "devices")
	if err := os.WriteFile(p, []byte(inputDevicesSample), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INPUT_DEVICES", p)
	got := connectedControllers()
	want := []struct{ kind, bus string }{{"dualsense", "bluetooth"}, {"xbox", "usb"}, {"steam", "usb"}}
	if len(got) != len(want) {
		t.Fatalf("manettes : %+v", got)
	}
	for i, w := range want {
		if got[i].Kind != w.kind || got[i].Bus != w.bus {
			t.Fatalf("manette %d : %+v, attendu %s/%s", i, got[i], w.kind, w.bus)
		}
	}
	if got[0].Name != "DualSense Wireless Controller" {
		t.Fatalf("nom : %q", got[0].Name)
	}
}

func TestConnectedControllersWithoutFile(t *testing.T) {
	t.Setenv("INPUT_DEVICES", filepath.Join(t.TempDir(), "absent"))
	if got := connectedControllers(); got == nil || len(got) != 0 {
		t.Fatalf("liste vide attendue : %#v", got)
	}
}

func TestControllerKind(t *testing.T) {
	for _, c := range []struct{ vendor, product, kind string }{
		{"054c", "0df2", "dualsense"}, {"054C", "09cc", "playstation"}, {"045e", "028e", "xbox"},
		{"057e", "2009", "nintendo"}, {"2dc8", "6101", "gamepad"},
	} {
		if got := controllerKind(c.vendor, c.product); got != c.kind {
			t.Errorf("%s:%s → %s, attendu %s", c.vendor, c.product, got, c.kind)
		}
	}
}

// TestPlayingGamesKeepsOnlyRunning : une session dont le jeu ne tourne plus (arrêt brutal) n'est pas
// rendue ; celle d'un jeu dont le verrou est tenu l'est, avec son heure de début.
func TestPlayingGamesKeepsOnlyRunning(t *testing.T) {
	isolateState(t)
	conf.cacheDir = t.TempDir()
	launch.lockedGame = ""
	started := time.Unix(1700000000, 0)
	writeSession("Running", started)
	writeSession("Stale", started)
	_ = os.MkdirAll(filepath.Join(conf.cacheDir, "locks"), 0o755)
	f, err := os.Create(lockPath("Running"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	got := playingGames()
	if len(got) != 1 || got[0].Game != "Running" || got[0].Since != started.Unix() {
		t.Fatalf("en cours : %+v", got)
	}
	removeSession("Running")
	if got := playingGames(); len(got) != 0 {
		t.Fatalf("session retirée : %+v", got)
	}
}

func TestCachedBuildFollowsBinary(t *testing.T) {
	isolateState(t)
	exe := filepath.Join(t.TempDir(), "lanceur")
	if err := os.WriteFile(exe, []byte("version 1"), 0o755); err != nil {
		t.Fatal(err)
	}
	conf.selfPath = exe
	first := cachedBuild()
	if len(first) != 12 || cachedBuild() != first {
		t.Fatalf("version : %q", first)
	}
	if err := os.WriteFile(exe, []byte("version 2, plus longue"), 0o755); err != nil {
		t.Fatal(err)
	}
	if cachedBuild() == first {
		t.Fatal("la version n'a pas suivi le nouveau binaire")
	}
}
