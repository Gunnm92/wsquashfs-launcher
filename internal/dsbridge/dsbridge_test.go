package dsbridge

import (
	"bytes"
	"encoding/hex"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// checkEvent : l'événement uhid vaut 4376 octets et, sans ses zéros de fin, le vecteur.
func checkEvent(t *testing.T, name string, got []byte, ref string) {
	t.Helper()
	if len(got) != uhidEventSize {
		t.Fatalf("%s : %d octets, attendu %d", name, len(got), uhidEventSize)
	}
	if h := hex.EncodeToString(bytes.TrimRight(got, "\x00")); h != ref {
		t.Errorf("%s :\n got %s\nwant %s", name, h, ref)
	}
}

func TestEntreeBTversUSB(t *testing.T) {
	got, ok := btInputToUSB(unhex(t, ref_bt_in))
	if !ok || hex.EncodeToString(got) != ref_usb_in || len(got) != 64 {
		t.Fatalf("got %x ok=%v", got, ok)
	}
	if _, ok := btInputToUSB(unhex(t, ref_bt_in)[:64]); ok {
		t.Error("lecture trop courte acceptée")
	}
	bad := unhex(t, ref_bt_in)
	bad[0] = 0x01
	if _, ok := btInputToUSB(bad); ok {
		t.Error("rapport non 0x31 accepté")
	}
}

func TestSortieUSBversBT(t *testing.T) {
	usb := unhex(t, ref_usb_out)
	for seq, ref := range map[uint8]string{0: ref_bt_out_0, 1: ref_bt_out_1, 15: ref_bt_out_15} {
		got, ok := usbOutputToBT(seq, usb)
		if !ok || len(got) != 78 || hex.EncodeToString(got) != ref {
			t.Errorf("seq %d : %x", seq, got)
		}
	}
	if _, ok := usbOutputToBT(0, usb[:47]); ok {
		t.Error("sortie trop courte acceptée")
	}
	bad := append([]byte{0x05}, usb[1:]...)
	if _, ok := usbOutputToBT(0, bad); ok {
		t.Error("rapport non 0x02 accepté")
	}
	// Le CRC (valeur du script Python : 426e521d) couvre 0xA2 + 74 octets.
	got, _ := usbOutputToBT(0, usb)
	if c := crc32.ChecksumIEEE(append([]byte{0xa2}, got[:74]...)); c != 0x1d526e42 {
		t.Errorf("crc %08x", c)
	}
}

func TestRemplacementMAC(t *testing.T) {
	in := unhex(t, ref_feat09_in)
	reply := featureReply(0x09, in, 20)
	if len(reply) != 20 {
		t.Fatalf("longueur %d", len(reply))
	}
	replaceMAC(reply, [6]byte{0x02, 0, 0, 0, 0, 0x51})
	if hex.EncodeToString(reply) != ref_feat09_out {
		t.Errorf("got %x", reply)
	}
	if n := len(featureReply(0x05, make([]byte, 64), 64)); n != 41 {
		t.Errorf("0x05 : %d octets", n)
	}
	if n := len(featureReply(0x20, make([]byte, 64), 64)); n != 64 {
		t.Errorf("0x20 : %d octets", n)
	}
	short := []byte{9, 1, 2, 3}
	replaceMAC(short, [6]byte{1, 2, 3, 4, 5, 6}) // < 7 octets : intact
	if !bytes.Equal(short, []byte{9, 1, 2, 3}) {
		t.Errorf("rapport court modifié : %x", short)
	}
}

func TestStructuresUhid(t *testing.T) {
	checkEvent(t, "create0", uhidEvent(uhidCreate2, createPayload(0, [6]byte{2, 0, 0, 0, 0, 0x50})), ref_create_0)
	checkEvent(t, "create1", uhidEvent(uhidCreate2, createPayload(1, [6]byte{2, 0, 0, 0, 0, 0x51})), ref_create_1)
	checkEvent(t, "input2", uhidEvent(uhidInput2, inputPayload(unhex(t, ref_usb_in))), ref_input2)
	checkEvent(t, "destroy", uhidEvent(uhidDestroy, nil), ref_destroy)
	checkEvent(t, "getreply", uhidEvent(uhidGetReportReply, getReportReplyPayload(0x01020304, 0, []byte("abcde"))), ref_getreply_ok)
	checkEvent(t, "getreply err", uhidEvent(uhidGetReportReply, getReportReplyPayload(7, 5, nil)), ref_getreply_err)
	checkEvent(t, "setreply", uhidEvent(uhidSetReportReply, setReportReplyPayload(0xAABBCCDD)), ref_setreply)
}

func TestDescripteurEtNumerosIoctl(t *testing.T) {
	if len(usbDescriptor) != refDescLen {
		t.Errorf("descripteur : %d octets", len(usbDescriptor))
	}
	if c := crc32.ChecksumIEEE(usbDescriptor); c != refDescCRC {
		t.Errorf("descripteur : crc %d", c)
	}
	for size, want := range refIoctl {
		if got := hidiocgfeature(size); got != want {
			t.Errorf("HIDIOCGFEATURE(%d) = %d, attendu %d", size, got, want)
		}
	}
}

func TestArguments(t *testing.T) {
	o, _, err := ParseArgs([]string{"--hide", "--parent", "42", "--ready-file=/tmp/x"})
	if err != nil || !o.Hide || o.Parent != 42 || o.ReadyFile != "/tmp/x" {
		t.Errorf("%+v %v", o, err)
	}
	if _, h, _ := ParseArgs([]string{"-h"}); !h {
		t.Error("-h ignoré")
	}
	for _, bad := range [][]string{{"--bogus"}, {"--parent", "abc"}, {"--parent"}, {"--hide=1"}} {
		if _, _, err := ParseArgs(bad); err == nil {
			t.Errorf("%v accepté", bad)
		}
	}
}

func TestDetectionSysfs(t *testing.T) {
	dir := t.TempDir()
	for n, id := range map[string]string{
		"hidraw1": "HID_ID=0005:0000054C:00000CE6",
		"hidraw0": "HID_ID=0003:0000054C:00000CE6", // USB : ignorée
		"hidraw2": "DRIVER=x\nHID_ID=0005:0000054C:00000CE6\nHID_NAME=y",
		"hidraw3": "HID_ID=0005:0000054C:00000DF2", // DualSense Edge : ignorée
	} {
		p := filepath.Join(dir, n, "device")
		os.MkdirAll(p, 0o755)
		os.WriteFile(filepath.Join(p, "uevent"), []byte(id+"\n"), 0o644)
	}
	s := &server{o: Options{Sysfs: dir, Dev: "/dev"}}
	got := strings.Join(s.btSources(), " ")
	if got != "/dev/hidraw1 /dev/hidraw2" {
		t.Errorf("got %q", got)
	}
}

func TestAucuneManette(t *testing.T) {
	var log bytes.Buffer
	err := Serve(Options{Sysfs: t.TempDir(), Log: &log})
	if err != ErrNoController || err.Error() != "aucune DualSense Bluetooth" {
		t.Errorf("err = %v", err)
	}
}

// pair : faux uhid (socketpair en paquets) et faux hidraw (socketpair).
func pair(t *testing.T) (b *bridge, kernel, device int, log *bytes.Buffer) {
	t.Helper()
	u, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	h, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	log = &bytes.Buffer{}
	b = &bridge{s: &server{o: Options{Log: log}}, uhid: u[0], source: h[0], node: "/dev/hidrawX",
		mac: [6]byte{2, 0, 0, 0, 0, 0x50}}
	t.Cleanup(func() {
		for _, fd := range []int{u[0], u[1], h[1]} {
			unix.Close(fd)
		}
		if b.source >= 0 {
			unix.Close(b.source)
		}
	})
	return b, u[1], h[1], log
}

func recv(t *testing.T, fd int) []byte {
	t.Helper()
	buf := make([]byte, 8192)
	n, _, err := unix.Recvfrom(fd, buf, unix.MSG_DONTWAIT)
	if err != nil {
		t.Fatalf("rien reçu : %v", err)
	}
	return buf[:n]
}

func TestBoucleEntreeSortieFeature(t *testing.T) {
	b, kernel, device, _ := pair(t)

	// Entrée : hidraw → uhid INPUT2.
	unix.Write(device, unhex(t, ref_bt_in))
	b.onSource()
	checkEvent(t, "input2", recv(t, kernel), ref_input2)

	// Sortie : UHID_OUTPUT → hidraw, avec séquence croissante.
	out := make([]byte, uhidEventSize)
	out[0] = uhidOutput
	copy(out[4:], unhex(t, ref_usb_out))
	out[4+uhidDataMax] = 48
	for i, want := range []string{ref_bt_out_0, ref_bt_out_1} {
		unix.Write(kernel, out)
		if err := b.onUhid(); err != nil {
			t.Fatal(err)
		}
		if got := hex.EncodeToString(recv(t, device)); got != want {
			t.Errorf("sortie %d : %s", i, got)
		}
	}

	// SET_REPORT acquitté.
	set := make([]byte, uhidEventSize)
	set[0] = uhidSetReport
	copy(set[4:], []byte{0xDD, 0xCC, 0xBB, 0xAA})
	unix.Write(kernel, set)
	if err := b.onUhid(); err != nil {
		t.Fatal(err)
	}
	checkEvent(t, "setreply", recv(t, kernel), ref_setreply)

	// GET_REPORT d'un rapport inconnu : erreur EIO (5), pas de données.
	get := make([]byte, uhidEventSize)
	get[0] = uhidGetReport
	get[4] = 7
	get[8] = 0x77
	unix.Write(kernel, get)
	if err := b.onUhid(); err != nil {
		t.Fatal(err)
	}
	checkEvent(t, "getreply err", recv(t, kernel), ref_getreply_err)
}

func TestPerteDeLaSource(t *testing.T) {
	b, _, _, log := pair(t)
	unix.Close(b.source)
	// Un tube en écriture seule : toute lecture échoue (EBADF), comme une manette partie.
	var p [2]int
	if err := unix.Pipe2(p[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	unix.Close(p[0])
	b.source = p[1]
	b.onSource()
	if b.source != -1 || b.node != "" {
		t.Error("source non détachée")
	}
	if !strings.Contains(log.String(), "/dev/hidrawX perdue, DualSense USB n°1 en attente") {
		t.Errorf("journal : %q", log.String())
	}
}

func TestAnalyseGetReport(t *testing.T) {
	ev := make([]byte, uhidEventSize)
	ev[0] = uhidGetReport
	copy(ev[4:], []byte{0x04, 0x03, 0x02, 0x01, 0x20, 0x03})
	if id, n := parseGetReport(ev); id != 0x01020304 || n != 0x20 {
		t.Errorf("%x %x", id, n)
	}
}
