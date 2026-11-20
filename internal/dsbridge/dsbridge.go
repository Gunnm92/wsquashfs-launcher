// Package dsbridge : pont uhid qui présente chaque DualSense Bluetooth comme
// une DualSense USB, intégré au lanceur (commande interne --dualsense-bridge,
// lancée par le lanceur pour les jeux en HIDRAW=1 ; ancien script Python
// dualsense-usb-bridge, repris octet pour octet). Le problème, le
// fonctionnement et les limites sont décrits dans docs/dualsense-bridge.md.
//
// Traduction (report.go) : entrée Bluetooth 0x31 (78 octets) → USB 0x01
// (64 octets) ; sortie USB 0x02 (48 octets) → Bluetooth 0x31 (78 octets,
// séquence, CRC32) ; rapports feature 0x05, 0x09 (adresse MAC remplacée) et
// 0x20 lus sur la manette Bluetooth, écritures feature acquittées sans être
// transmises.
//
// Boucle d'événements : une seule goroutine (celle de Serve) attend avec
// unix.Poll tous les descripteurs (un /dev/uhid et, tant qu'elle est là, un
// hidraw par manette) plus un tube de réveil, avec un délai de 1 s (balayage
// des nouvelles manettes, surveillance du parent). Aucun descripteur n'est
// partagé entre goroutines : tout est lu, écrit et fermé par la goroutine de
// Serve. Une seule autre goroutine existe, watchSignals : elle vit de
// l'appel de Serve jusqu'à son retour (Serve la termine et l'attend) ; elle
// convertit SIGTERM, SIGINT et SIGHUP en fermeture du canal d'arrêt et en
// octet dans le tube de réveil.
//
// Le paquet ne quitte jamais le processus : Run rend le code de sortie.
package dsbridge

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// hidIDBT : ligne d'uevent d'une DualSense Bluetooth (bus 0005, 054c:0ce6).
const hidIDBT = "HID_ID=0005:0000054C:00000CE6"

// logPrefix : préfixe des messages (celui du script d'origine).
const logPrefix = "dualsense-usb-bridge: "

// ErrNoController : aucune DualSense Bluetooth au démarrage.
var ErrNoController = errors.New("aucune DualSense Bluetooth")

// Options : paramètres du pont (options --hide, --parent, --ready-file).
type Options struct {
	Hide      bool      // rendre les hidraw Bluetooth illisibles (chmod 600) tant que le pont tourne
	ReadyFile string    // fichier créé une fois les manettes USB en place ("" : aucun)
	Parent    int       // s'arrêter quand ce processus se termine (0 : aucun)
	Log       io.Writer // messages (os.Stderr si nil)

	// Chemins surchargeables (tests) ; valeurs par défaut si vides.
	Sysfs string // /sys/class/hidraw
	Dev   string // /dev (nœuds hidraw)
	Uhid  string // /dev/uhid
	Proc  string // /proc
}

func (o *Options) defaults() {
	if o.Log == nil {
		o.Log = os.Stderr
	}
	for p, d := range map[*string]string{&o.Sysfs: "/sys/class/hidraw", &o.Dev: "/dev", &o.Uhid: "/dev/uhid", &o.Proc: "/proc"} {
		if *p == "" {
			*p = d
		}
	}
}

const usageLine = "usage : --dualsense-bridge [--hide] [--ready-file F] [--parent PID]"

const helpText = usageLine + `

Présente les DualSense Bluetooth comme des DualSense USB (uhid).

options :
  --hide             rendre les DualSense Bluetooth illisibles tant que le
                     pont tourne
  --ready-file F     fichier créé une fois les manettes USB en place
  --parent PID       s'arrêter quand ce processus se termine
`

// ParseArgs : options de la ligne de commande (--hide, --ready-file F,
// --parent N ; --opt=valeur accepté). help est vrai pour -h et --help.
func ParseArgs(args []string) (o Options, help bool, err error) {
	for i := 0; i < len(args); i++ {
		name, val, hasVal := strings.Cut(args[i], "=")
		value := func() (string, error) {
			if hasVal {
				return val, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("argument %s : attend une valeur", name)
			}
			i++
			return args[i], nil
		}
		switch name {
		case "-h", "--help":
			return o, true, nil
		case "--hide":
			if hasVal {
				return o, false, errors.New("argument --hide : ne prend pas de valeur")
			}
			o.Hide = true
		case "--ready-file":
			if o.ReadyFile, err = value(); err != nil {
				return o, false, err
			}
		case "--parent":
			v, e := value()
			if e != nil {
				return o, false, e
			}
			n, e := strconv.Atoi(strings.TrimSpace(v))
			if e != nil {
				return o, false, fmt.Errorf("argument --parent : valeur entière invalide : %q", v)
			}
			o.Parent = n
		default:
			return o, false, fmt.Errorf("arguments non reconnus : %s", args[i])
		}
	}
	return o, false, nil
}

// Run : exécute le pont avec les arguments de --dualsense-bridge et rend le
// code de sortie : 0 arrêt normal, 1 erreur (ou aucune DualSense Bluetooth),
// 2 arguments invalides.
func Run(args []string) int {
	o, help, err := ParseArgs(args)
	if help {
		fmt.Print(helpText)
		return 0
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, usageLine)
		fmt.Fprintln(os.Stderr, logPrefix+"error: "+err.Error())
		return 2
	}
	if err := Serve(o); err != nil {
		fmt.Fprintln(os.Stderr, logPrefix+err.Error())
		return 1
	}
	return 0
}

// server : état de Serve, utilisé par la seule goroutine de Serve.
type server struct {
	o       Options
	bridges []*bridge
	wake    [2]int // tube de réveil (lecture, écriture) : signaux
	stop    chan struct{}
}

func (s *server) logf(format string, a ...any) {
	fmt.Fprintf(s.o.Log, logPrefix+format+"\n", a...)
}

// btSources : nœuds /dev/hidrawN des DualSense Bluetooth présentes, par ordre
// de nom.
func (s *server) btSources() []string {
	entries, _ := os.ReadDir(s.o.Sysfs)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	var found []string
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(s.o.Sysfs, name, "device", "uevent"))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimRight(line, "\r") == hidIDBT {
				found = append(found, filepath.Join(s.o.Dev, name))
				break
			}
		}
	}
	return found
}

// watchSignals : goroutine unique, vivante pendant Serve. Au premier SIGTERM,
// SIGINT ou SIGHUP, ferme stop et réveille la boucle ; se termine quand done
// est fermé.
func (s *server) watchSignals(done <-chan struct{}, wg *sync.WaitGroup) {
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer signal.Stop(ch)
		var once sync.Once
		for {
			select {
			case <-ch:
				once.Do(func() { close(s.stop) })
				_, _ = unix.Write(s.wake[1], []byte{1})
			case <-done:
				return
			}
		}
	}()
}

func (s *server) stopped() bool {
	select {
	case <-s.stop:
		return true
	default:
		return false
	}
}

// Serve : exécute le pont jusqu'à un signal d'arrêt (SIGTERM, SIGINT, SIGHUP)
// ou la fin du processus parent (nil), ou une erreur. ErrNoController si
// aucune DualSense Bluetooth n'est présente au démarrage. À la sortie, les
// périphériques uhid sont détruits et les droits des hidraw restaurés.
func Serve(o Options) error {
	o.defaults()
	s := &server{o: o, stop: make(chan struct{})}
	if err := unix.Pipe2(s.wake[:], unix.O_CLOEXEC|unix.O_NONBLOCK); err != nil {
		return err
	}
	defer unix.Close(s.wake[0])
	defer unix.Close(s.wake[1])
	done := make(chan struct{})
	var wg sync.WaitGroup
	s.watchSignals(done, &wg)
	defer func() { close(done); wg.Wait() }()
	defer func() {
		for _, b := range s.bridges {
			b.close()
		}
	}()

	for _, node := range s.btSources() {
		b, err := s.newBridge(len(s.bridges))
		if err != nil {
			return err
		}
		s.bridges = append(s.bridges, b)
		if !b.attach(node, o.Hide) {
			s.bridges = s.bridges[:len(s.bridges)-1]
			b.close()
		}
	}
	if len(s.bridges) == 0 {
		return ErrNoController
	}
	if o.ReadyFile != "" {
		// Le noyau crée le nœud hidraw de la manette USB.
		select {
		case <-time.After(500 * time.Millisecond):
		case <-s.stop:
		}
		f, err := os.Create(o.ReadyFile)
		if err != nil {
			return err
		}
		f.Close()
	}
	return s.loop()
}

// loop : boucle d'événements, une itération par attente de unix.Poll (1 s au
// plus).
func (s *server) loop() error {
	lastScan, failed := time.Now(), map[string]bool{}
	for !s.stopped() {
		type entry struct {
			b      *bridge
			source bool
			fd     int
		}
		fds := []unix.PollFd{{Fd: int32(s.wake[0]), Events: unix.POLLIN}}
		entries := []entry{{}}
		for _, b := range s.bridges {
			fds = append(fds, unix.PollFd{Fd: int32(b.uhid), Events: unix.POLLIN})
			entries = append(entries, entry{b, false, b.uhid})
			if b.source >= 0 {
				fds = append(fds, unix.PollFd{Fd: int32(b.source), Events: unix.POLLIN})
				entries = append(entries, entry{b, true, b.source})
			}
		}
		if _, err := unix.Poll(fds, 1000); err != nil && err != unix.EINTR {
			return err
		}
		for i, p := range fds {
			if p.Revents == 0 {
				continue
			}
			e := entries[i]
			switch {
			case i == 0:
				var buf [16]byte
				_, _ = unix.Read(s.wake[0], buf[:])
			case e.source:
				if e.b.source == e.fd { // détachée plus tôt dans cette itération : rien à lire
					e.b.onSource()
				}
			default:
				if err := e.b.onUhid(); err != nil {
					return err
				}
			}
		}
		if time.Since(lastScan) < time.Second {
			continue
		}
		lastScan = time.Now()
		if s.o.Parent != 0 {
			if _, err := os.Stat(fmt.Sprintf("%s/%d", s.o.Proc, s.o.Parent)); err != nil {
				return nil
			}
		}
		// Moonlight reconnecté : nouvelle DualSense Bluetooth, même manette USB
		// (le jeu ne la perd pas).
		used := map[string]bool{}
		for n := range failed {
			used[n] = true
		}
		for _, b := range s.bridges {
			used[b.node] = true
		}
		for _, node := range s.btSources() {
			if used[node] {
				continue
			}
			var idle *bridge
			for _, b := range s.bridges {
				if b.source < 0 {
					idle = b
					break
				}
			}
			if idle == nil {
				var err error
				if idle, err = s.newBridge(len(s.bridges)); err != nil {
					return err
				}
				s.bridges = append(s.bridges, idle)
			}
			if !idle.attach(node, s.o.Hide) {
				failed[node] = true
			}
		}
	}
	return nil
}

// setMode : chmod du nœud, par sudo -n chmod si les droits manquent.
func setMode(node string, mode fs.FileMode) bool {
	err := os.Chmod(node, mode)
	switch {
	case err == nil:
		return true
	case errors.Is(err, fs.ErrPermission):
		cmd := exec.Command("sudo", "-n", "chmod", fmt.Sprintf("%o", uint32(mode)), node)
		return cmd.Run() == nil
	}
	return false
}
