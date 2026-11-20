package dsbridge

import (
	"os"
	"unicode"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/unix"
)

// bridge : une DualSense USB virtuelle (un périphérique uhid), alimentée par
// une DualSense Bluetooth (un hidraw) quand il y en a une. Utilisé par la
// seule goroutine de Serve.
type bridge struct {
	s      *server
	index  int
	uhid   int    // /dev/uhid, ouvert pour toute la vie du pont
	source int    // hidraw Bluetooth, -1 si en attente
	node   string // chemin du hidraw ("" si en attente)
	hidden *hiddenNode
	outSeq uint8
	mac    [6]byte
}

// hiddenNode : droits d'origine d'un hidraw masqué (--hide).
type hiddenNode struct {
	node string
	rdev uint64
	mode os.FileMode
}

// newBridge : ouvre /dev/uhid et crée la DualSense USB virtuelle.
func (s *server) newBridge(index int) (*bridge, error) {
	fd, err := unix.Open(s.o.Uhid, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: s.o.Uhid, Err: err}
	}
	b := &bridge{s: s, index: index, uhid: fd, source: -1,
		mac: [6]byte{0x02, 0, 0, 0, 0, byte(0x50 + index)}}
	if err := b.writeUhid(uhidEvent(uhidCreate2, createPayload(index, b.mac))); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return b, nil
}

// strerror : message d'errno comme le strerror de Python ("Permission denied").
func strerror(err error) string {
	msg := err.Error()
	r, n := utf8.DecodeRuneInString(msg)
	return string(unicode.ToUpper(r)) + msg[n:]
}

// attach : ouvre le hidraw et le masque si hide. Faux si illisible.
func (b *bridge) attach(node string, hide bool) bool {
	fd, err := unix.Open(node, unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		b.s.logf("%s illisible (%s)", node, strerror(err))
		return false
	}
	b.source, b.node = fd, node
	b.s.logf("%s → DualSense USB n°%d", node, b.index+1)
	if hide {
		var st unix.Stat_t
		if err := unix.Stat(node, &st); err == nil && setMode(node, 0o600) {
			b.hidden = &hiddenNode{node, uint64(st.Rdev), os.FileMode(st.Mode & 0o777)}
		} else {
			b.s.logf("impossible de masquer %s (ni root ni sudo -n) : le jeu verra aussi la manette Bluetooth", node)
		}
	}
	return true
}

// restore : rend au hidraw ses droits d'origine (si c'est toujours le même nœud).
func (b *bridge) restore() {
	if b.hidden == nil {
		return
	}
	h := b.hidden
	b.hidden = nil
	var st unix.Stat_t
	if unix.Stat(h.node, &st) == nil && uint64(st.Rdev) == h.rdev { // sinon : manette débranchée
		setMode(h.node, h.mode)
	}
}

// detach : libère le hidraw ; la DualSense USB reste, en attente.
func (b *bridge) detach() {
	b.restore()
	if b.source >= 0 {
		unix.Close(b.source)
		b.s.logf("%s perdue, DualSense USB n°%d en attente", b.node, b.index+1)
	}
	b.source, b.node = -1, ""
}

// close : détache, détruit le périphérique uhid et ferme /dev/uhid.
func (b *bridge) close() {
	b.detach()
	_ = b.writeUhid(uhidEvent(uhidDestroy, nil))
	unix.Close(b.uhid)
}

func (b *bridge) writeUhid(ev []byte) error {
	for {
		_, err := unix.Write(b.uhid, ev)
		if err != unix.EINTR {
			return err
		}
	}
}

// onSource : rapport d'entrée Bluetooth lu → rapport USB. Une erreur de
// lecture (manette partie) détache.
func (b *bridge) onSource() {
	buf := make([]byte, 128)
	n, err := unix.Read(b.source, buf)
	if err == unix.EAGAIN || err == unix.EINTR {
		return
	}
	if err != nil {
		b.detach()
		return
	}
	if report, ok := btInputToUSB(buf[:n]); ok {
		_ = b.writeUhid(uhidEvent(uhidInput2, inputPayload(report)))
	}
}

// onUhid : événement du noyau pour la DualSense USB. Une erreur ici est fatale
// (uhid inutilisable).
func (b *bridge) onUhid() error {
	buf := make([]byte, uhidEventSize)
	n, err := unix.Read(b.uhid, buf)
	if err == unix.EAGAIN || err == unix.EINTR {
		return nil
	}
	if err != nil {
		return err
	}
	event := buf[:n]
	if n < 4 {
		return nil
	}
	switch kind := le32(event); kind {
	case uhidOutput:
		data, ok := parseOutput(event)
		if !ok || b.source < 0 {
			return nil
		}
		report, ok := usbOutputToBT(b.outSeq, data)
		if !ok {
			return nil
		}
		b.outSeq = (b.outSeq + 1) & 0x0F
		if _, err := unix.Write(b.source, report); err != nil {
			b.detach()
		}
	case uhidGetReport:
		if n < 9 {
			return nil
		}
		request, number := parseGetReport(event)
		var reply []byte
		if b.source >= 0 && featureSizes[number] > 0 {
			buf := make([]byte, 64)
			buf[0] = number
			r, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(b.source), hidiocgfeature(len(buf)),
				uintptr(unsafe.Pointer(&buf[0])))
			if errno == 0 {
				reply = featureReply(number, buf, int(r))
			}
			if number == 0x09 {
				replaceMAC(reply, b.mac)
			}
		}
		var errCode uint16
		if len(reply) == 0 {
			errCode = uint16(unix.EIO)
		}
		return b.writeUhid(uhidEvent(uhidGetReportReply, getReportReplyPayload(request, errCode, reply)))
	case uhidSetReport:
		if n < 8 {
			return nil
		}
		return b.writeUhid(uhidEvent(uhidSetReportReply, setReportReplyPayload(le32(event[4:]))))
	}
	return nil
}
