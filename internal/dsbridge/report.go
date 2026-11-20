package dsbridge

// Traduction des rapports et encodage des structures uhid : fonctions pures,
// sans descripteur de fichier (testées sans matériel). Chaque fonction
// reprend à l'octet près celle du script Python d'origine.

import (
	"encoding/binary"
	"encoding/hex"
	"hash/crc32"
	"strconv"
)

// Types d'événements uhid (linux/uhid.h, enum uhid_event_type).
const (
	uhidDestroy        = 1
	uhidOutput         = 6
	uhidGetReport      = 9
	uhidGetReportReply = 10
	uhidCreate2        = 11
	uhidInput2         = 12
	uhidSetReport      = 13
	uhidSetReportReply = 14
)

// uhidEventSize : taille de struct uhid_event (type u32 + union de 4372 octets),
// celle de chaque lecture et écriture sur /dev/uhid.
const uhidEventSize = 4 + 4372

// uhidDataMax : taille du tampon data[] de uhid_output_req (HID_MAX_BUFFER_SIZE) ;
// le champ size (u16) le suit.
const uhidDataMax = 4096

// Rapports d'une DualSense.
const (
	btInputID     = 0x31 // entrée Bluetooth, 78 octets
	btInputSize   = 78
	usbInputID    = 0x01 // entrée USB, 64 octets
	usbOutputID   = 0x02 // sortie USB, 48 octets
	usbOutputSize = 48
	btOutputSize  = 78
)

// usbDescriptor : descripteur HID d'une DualSense USB (libvirtualhid, d'après inputtino).
var usbDescriptor = mustHex(
	"05010905a1018501093009310932093509330934150026ff0075089506810206" +
		"00ff09209501810205010939150025073500463b016514750495018142650005" +
		"091901290f150025017501950f81020600ff0921950d81020600ff0922150026" +
		"ff0075089534810285020923952f9102850509339528b10285080934952fb102" +
		"850909249513b102850a0925951ab10285200926953fb102852109279504b102" +
		"85220940953fb10285800928953fb10285810929953fb1028582092a9509b102" +
		"8583092b953fb1028584092c953fb1028585092d9502b10285a0092e9501b102" +
		"85e0092f953fb10285f00930953fb10285f10931953fb10285f20932950fb102" +
		"85f40935953fb10285f509369503b102c0")

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// featureSizes : rapports feature relayés et leur taille en USB.
var featureSizes = map[byte]int{0x05: 41, 0x09: 20, 0x20: 64}

// hidiocgfeature : numéro de l'ioctl HIDIOCGFEATURE(size), _IOC(_IOC_WRITE|_IOC_READ,
// 'H', 0x07, size) : (3<<30) | (size<<16) | ('H'<<8) | 0x07.
func hidiocgfeature(size int) uintptr {
	return uintptr(3<<30 | size<<16 | 'H'<<8 | 0x07)
}

// uhidEvent : struct uhid_event, type en u32 petit-boutiste puis charge utile,
// complétée par des zéros jusqu'à uhidEventSize.
func uhidEvent(kind uint32, payload []byte) []byte {
	ev := make([]byte, max(uhidEventSize, 4+len(payload)))
	binary.LittleEndian.PutUint32(ev, kind)
	copy(ev[4:], payload)
	return ev
}

// padded : s complété par des zéros jusqu'à n octets (tronqué au-delà).
func padded(s string, n int) []byte {
	b := make([]byte, n)
	copy(b, s)
	return b
}

// macString : "02:00:00:00:00:50".
func macString(mac [6]byte) string {
	const digits = "0123456789abcdef"
	b := make([]byte, 0, 17)
	for i, v := range mac {
		if i > 0 {
			b = append(b, ':')
		}
		b = append(b, digits[v>>4], digits[v&15])
	}
	return string(b)
}

// createPayload : uhid_create2_req. name[128], phys[64], uniq[64], puis
// struct.pack("<HHIIII") : rd_size, bus (3 = USB), vendor, product, version,
// country ; enfin le descripteur.
func createPayload(index int, mac [6]byte) []byte {
	p := padded("Sony Interactive Entertainment Wireless Controller", 128)
	p = append(p, padded("dualsense-usb-bridge/"+strconv.Itoa(index), 64)...)
	p = append(p, padded(macString(mac), 64)...)
	var h [20]byte
	binary.LittleEndian.PutUint16(h[0:], uint16(len(usbDescriptor)))
	binary.LittleEndian.PutUint16(h[2:], 0x03)
	binary.LittleEndian.PutUint32(h[4:], 0x054C)
	binary.LittleEndian.PutUint32(h[8:], 0x0CE6)
	binary.LittleEndian.PutUint32(h[12:], 0x8111)
	binary.LittleEndian.PutUint32(h[16:], 0)
	p = append(p, h[:]...)
	return append(p, usbDescriptor...)
}

// inputPayload : uhid_input2_req, longueur u16 puis le rapport.
func inputPayload(report []byte) []byte {
	p := make([]byte, 2, 2+len(report))
	binary.LittleEndian.PutUint16(p, uint16(len(report)))
	return append(p, report...)
}

// getReportReplyPayload : uhid_get_report_reply_req, struct.pack("<IHH") puis les données.
func getReportReplyPayload(request uint32, errno uint16, data []byte) []byte {
	p := make([]byte, 8, 8+len(data))
	binary.LittleEndian.PutUint32(p, request)
	binary.LittleEndian.PutUint16(p[4:], errno)
	binary.LittleEndian.PutUint16(p[6:], uint16(len(data)))
	return append(p, data...)
}

// setReportReplyPayload : uhid_set_report_reply_req, struct.pack("<IH"), erreur 0.
func setReportReplyPayload(request uint32) []byte {
	p := make([]byte, 6)
	binary.LittleEndian.PutUint32(p, request)
	return p
}

// btInputToUSB : rapport 0x31 (au moins 65 octets lus) → rapport USB 0x01 de
// 64 octets : même bloc commun, sans l'octet d'étiquette Bluetooth. Faux si
// la lecture n'est pas un rapport d'entrée Bluetooth complet.
func btInputToUSB(data []byte) ([]byte, bool) {
	if len(data) < 65 || data[0] != btInputID {
		return nil, false
	}
	report := make([]byte, 64)
	report[0] = usbInputID
	copy(report[1:], data[2:65])
	return report, true
}

// parseOutput : données d'un événement UHID_OUTPUT (type u32, data[4096],
// size u16, rtype u8). Faux si l'événement est tronqué.
func parseOutput(event []byte) ([]byte, bool) {
	if len(event) < 4+uhidDataMax+2 {
		return nil, false
	}
	size := int(binary.LittleEndian.Uint16(event[4+uhidDataMax:]))
	return event[4 : 4+min(size, uhidDataMax)], true
}

// usbOutputToBT : rapport de sortie USB 0x02 (au moins 48 octets) → rapport
// Bluetooth 0x31 de 78 octets : étiquette de séquence (seq sur 4 bits, poids
// fort), 0x10, bloc commun de 47 octets, CRC32 petit-boutiste en 74 (préfixe
// 0xA2 sur les 74 premiers octets). Faux si ce n'est pas une sortie 0x02.
func usbOutputToBT(seq uint8, data []byte) ([]byte, bool) {
	if len(data) < usbOutputSize || data[0] != usbOutputID {
		return nil, false
	}
	report := make([]byte, btOutputSize)
	report[0] = btInputID
	report[1] = (seq << 4) & 0xF0
	report[2] = 0x10
	copy(report[3:50], data[1:48])
	crc := crc32.Update(crc32.ChecksumIEEE([]byte{0xa2}), crc32.IEEETable, report[:74])
	binary.LittleEndian.PutUint32(report[74:], crc)
	return report, true
}

// featureReply : réponse à un GET_REPORT feature à partir des octets lus par
// l'ioctl (buf, length octets valides), tronquée à la taille USB du rapport.
func featureReply(number byte, buf []byte, length int) []byte {
	length = max(0, min(length, len(buf), featureSizes[number]))
	return append([]byte(nil), buf[:length]...)
}

// replaceMAC : octets 1 à 6 du rapport 0x09 (appairage) remplacés par mac en
// ordre inverse : hid-playstation refuse une 2e manette de même adresse MAC
// (même DualSense en USB et en Bluetooth), la manette USB prend la sienne.
func replaceMAC(reply []byte, mac [6]byte) {
	if len(reply) < 7 {
		return
	}
	for i := 0; i < 6; i++ {
		reply[1+i] = mac[5-i]
	}
}

func le32(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }

// parseGetReport : identifiant de requête (u32) et numéro de rapport (u8) d'un
// événement UHID_GET_REPORT (uhid_get_report_req : id u32, rnum u8, rtype u8).
func parseGetReport(event []byte) (request uint32, number byte) {
	return le32(event[4:]), event[8]
}
