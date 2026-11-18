// Package imagefs : lecteur squashfs en Go pur et serveur FUSE en lecture seule
// d'une image .wsquashfs (format Batocera), sans squashfuse ni mksquashfs.
//
// Montage FUSE d'une image squashfs, intégré au lanceur (16/11) : remplace
// squashfuse et squashfuse_ll, absents de SteamOS (fusermount3, fuse-overlayfs
// et l'overlay noyau y sont, pas squashfuse). Le lanceur se relance avec
// --serve-image, processus à part qui sert l'image jusqu'au démontage
// (fusermount -u, umount) : mêmes démontages qu'avec squashfuse.
//
// Lecture seule. Attributs étendus de l'image exposés (anciennes images
// Batocera : user.DOSATTRIB de Wine), comme le faisait squashfuse.
// Performances : blocs décompressés en cache (LRU partagée), requêtes FUSE
// servies en parallèle, entrées et attributs gardés par le noyau (image
// immuable pendant le montage).

package imagefs

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	squashfslow "github.com/CalebQ42/squashfs/low"
	"github.com/CalebQ42/squashfs/low/directory"
	"github.com/CalebQ42/squashfs/low/inode"
	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// Options : paramètres du serveur d'image (sous-options de --serve-image, sans
// tirets : uid N, gid N, allow_other).
type Options struct {
	Image, Point string
	UID, GID     int // -1 : propriétaires de l'image
	AllowOther   bool
}

// imageCacheBlocks : blocs de données et de fragments décompressés (128 Kio
// par bloc dans toutes les images, soit 32 Mio) ; imageCacheMeta : blocs de
// métadonnées (8 Kio, soit 32 Mio).
const (
	imageCacheBlocks = 256
	imageCacheMeta   = 4096
	imageKernelTTL   = time.Hour
	// imagePrefetchBlocks : blocs décompressés d'avance après une lecture
	// (8 × 128 Kio, au-delà des 1 Mio de lecture anticipée du noyau).
	imagePrefetchBlocks = 8
)

// lru : cache à éviction du plus ancien, sûr entre goroutines. Clés et
// valeurs typées : pas de conversion en interface (allocation) à chaque accès.
type lru[K comparable, V any] struct {
	mu    sync.Mutex
	max   int
	order *list.List
	items map[K]*list.Element
}

type lruEntry[K comparable, V any] struct {
	key K
	val V
}

func newLRU[K comparable, V any](max int) *lru[K, V] {
	return &lru[K, V]{max: max, order: list.New(), items: make(map[K]*list.Element, max)}
}

func (c *lru[K, V]) get(k K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[k]; ok {
		c.order.MoveToFront(e)
		return e.Value.(*lruEntry[K, V]).val, true
	}
	var zero V
	return zero, false
}

func (c *lru[K, V]) put(k K, v V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[k]; ok {
		e.Value.(*lruEntry[K, V]).val = v
		c.order.MoveToFront(e)
		return
	}
	c.items[k] = c.order.PushFront(&lruEntry[K, V]{k, v})
	for c.order.Len() > c.max {
		last := c.order.Back()
		c.order.Remove(last)
		delete(c.items, last.Value.(*lruEntry[K, V]).key)
	}
}

// squashImage : image ouverte et ses caches.
type squashImage struct {
	rdr    squashfslow.Reader
	file   *os.File
	dec    sqDecompressor
	uid    int
	gid    int
	blocks *lru[uint64, []byte]      // position → bloc de données ou de fragments décompressé
	meta   *lru[int64, *sqMetaBlock] // position → bloc de métadonnées
	zero   []byte                    // bloc creux : zéros partagés, en lecture seule
	raw    sync.Pool                 // tampons des blocs compressés (*[]byte), rendus après décompression

	fragOnce sync.Once
	fragPtrs []uint64
	fragErr  error

	xattrOnce  sync.Once
	xattrStart uint64
	xattrPtrs  []uint64
	xattrIDs   uint32
	xattrErr   error

	inflightMu    sync.Mutex
	inflight      map[uint64]*sqInflight
	prefetchSlots chan struct{} // lectures anticipées simultanées (un par cœur)
}

func openSquashImage(path string, uid, gid int) (*squashImage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	rdr, err := squashfslow.NewReader(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("lecture du superbloc : %w", err)
	}
	dec, err := newSqDecompressor(rdr.Superblock.CompType)
	if err != nil {
		f.Close()
		return nil, err
	}
	return &squashImage{rdr: rdr, file: f, dec: dec, uid: uid, gid: gid,
		blocks: newLRU[uint64, []byte](imageCacheBlocks), meta: newLRU[int64, *sqMetaBlock](imageCacheMeta),
		zero:     make([]byte, rdr.Superblock.BlockSize),
		inflight: map[uint64]*sqInflight{}, prefetchSlots: make(chan struct{}, runtime.NumCPU())}, nil
}

// sqNode : nœud FUSE (fichier, dossier, lien…) de l'image.
type sqNode struct {
	fs.Inode
	img  *squashImage
	base squashfslow.FileBase

	mu      sync.Mutex
	entries []directory.Entry // dossier : entrées lues au premier accès
	listed  bool
	file    *sqFileLayout // fichier : blocs, lus au premier accès
	lastEnd atomic.Int64  // fin de la dernière lecture (lecture séquentielle ?)

	xattrRead bool // attributs étendus lus (xattrList)
	xattrList []sqXattr
}

var (
	_ fs.NodeGetattrer   = (*sqNode)(nil)
	_ fs.NodeLookuper    = (*sqNode)(nil)
	_ fs.NodeReaddirer   = (*sqNode)(nil)
	_ fs.NodeOpener      = (*sqNode)(nil)
	_ fs.NodeReader      = (*sqNode)(nil)
	_ fs.NodeReadlinker  = (*sqNode)(nil)
	_ fs.NodeGetxattrer  = (*sqNode)(nil)
	_ fs.NodeListxattrer = (*sqNode)(nil)
	_ fs.NodeStatfser    = (*sqNode)(nil)
	_ fs.NodeAccesser    = (*sqNode)(nil)

	_ fs.NodeOpendirHandler = (*sqNode)(nil)
	_ fs.FileReaddirenter   = (*sqDirHandle)(nil)
	_ fs.FileSeekdirer      = (*sqDirHandle)(nil)
	_ fs.FileReleasedirer   = (*sqDirHandle)(nil)
)

// ino : numéro d'inode FUSE (celui de l'image, + 1 : la racine FUSE est 1).
func (n *sqNode) ino() uint64 { return uint64(n.base.Inode.Num) + 1 }

func (n *sqNode) fillAttr(a *fuse.Attr) {
	in := n.base.Inode
	a.Ino = n.ino()
	a.Mode = uint32(in.Perm) | sqTypeMode(in.Type)
	a.Nlink = in.LinkCount()
	if a.Nlink == 0 {
		a.Nlink = 1
	}
	a.Size = in.Size()
	if t, ok := sqLinkTarget(in); ok {
		a.Size = uint64(len(t)) // comme lstat : longueur de la cible
	}
	a.Blocks = (a.Size + 511) / 512
	a.Blksize = n.img.rdr.Superblock.BlockSize
	a.Mtime = uint64(in.ModTime)
	a.Atime, a.Ctime = a.Mtime, a.Mtime
	a.Uid, a.Gid = n.owner()
	if dev, ok := sqDevice(in); ok {
		a.Rdev = dev
	}
}

// owner : uid/gid imposés (squashfuse_ll -o uid=,gid=), sinon ceux de l'image.
func (n *sqNode) owner() (uint32, uint32) {
	uid, gid := uint32(n.img.uid), uint32(n.img.gid)
	if n.img.uid < 0 {
		uid, _ = n.base.Uid(&n.img.rdr)
	}
	if n.img.gid < 0 {
		gid, _ = n.base.Gid(&n.img.rdr)
	}
	return uid, gid
}

func sqTypeMode(t uint16) uint32 {
	switch t {
	case inode.Dir, inode.EDir:
		return syscall.S_IFDIR
	case inode.Fil, inode.EFil:
		return syscall.S_IFREG
	case inode.Sym, inode.ESym:
		return syscall.S_IFLNK
	case inode.Block, inode.EBlock:
		return syscall.S_IFBLK
	case inode.Char, inode.EChar:
		return syscall.S_IFCHR
	case inode.Fifo, inode.EFifo:
		return syscall.S_IFIFO
	case inode.Sock, inode.ESock:
		return syscall.S_IFSOCK
	}
	return syscall.S_IFREG
}

func sqDevice(in inode.Inode) (uint32, bool) {
	switch d := in.Data.(type) {
	case inode.Device:
		return d.Dev, true
	case inode.EDevice:
		return d.Dev, true
	}
	return 0, false
}

func (n *sqNode) Getattr(_ context.Context, _ fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	n.fillAttr(&out.Attr)
	out.SetTimeout(imageKernelTTL)
	return 0
}

func (n *sqNode) Statfs(_ context.Context, out *fuse.StatfsOut) syscall.Errno {
	sb := n.img.rdr.Superblock
	out.Bsize = sb.BlockSize
	out.Frsize = sb.BlockSize
	out.Blocks = (sb.Size + uint64(sb.BlockSize) - 1) / uint64(sb.BlockSize)
	out.Files = uint64(sb.InodeCount)
	out.NameLen = 256
	return 0
}

// Access : aucun contrôle, comme squashfuse (sans default_permissions) :
// sinon une image dont les dossiers sont à un autre UID (racine en 744 à
// l'UID 1000, Psyvariar Delta) devient illisible. Écriture : EROFS (ro).
func (n *sqNode) Access(context.Context, uint32) syscall.Errno { return 0 }

// nodeXattrs : attributs étendus du nœud (anciennes images Batocera :
// user.DOSATTRIB de Wine), lus une fois.
func (n *sqNode) nodeXattrs() ([]sqXattr, syscall.Errno) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.xattrRead {
		x, err := n.img.xattrs(sqXattrIndex(n.base.Inode))
		if err != nil {
			return nil, syscall.EIO
		}
		n.xattrList, n.xattrRead = x, true
	}
	return n.xattrList, 0
}

func (n *sqNode) Getxattr(_ context.Context, attr string, dest []byte) (uint32, syscall.Errno) {
	list, errno := n.nodeXattrs()
	if errno != 0 {
		return 0, errno
	}
	for _, x := range list {
		if x.name == attr {
			if len(dest) < len(x.value) {
				return uint32(len(x.value)), syscall.ERANGE
			}
			return uint32(copy(dest, x.value)), 0
		}
	}
	return 0, syscall.ENODATA
}

func (n *sqNode) Listxattr(_ context.Context, dest []byte) (uint32, syscall.Errno) {
	list, errno := n.nodeXattrs()
	if errno != 0 {
		return 0, errno
	}
	var names []byte
	for _, x := range list {
		names = append(append(names, x.name...), 0)
	}
	if len(dest) < len(names) {
		return uint32(len(names)), syscall.ERANGE
	}
	return uint32(copy(dest, names)), 0
}

// dir : entrées du dossier, lues une fois (le nœud reste en mémoire tant que
// le noyau le connaît).
func (n *sqNode) dir() ([]directory.Entry, syscall.Errno) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.listed {
		if !n.base.IsDir() {
			return nil, syscall.ENOTDIR
		}
		e, err := n.img.readDir(n.base.Inode)
		if err != nil {
			return nil, syscall.EIO
		}
		n.entries, n.listed = e, true
	}
	return n.entries, 0
}

func (n *sqNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	entries, errno := n.dir()
	if errno != 0 {
		return nil, errno
	}
	// Entrées triées par nom (format squashfs) : recherche dichotomique.
	i, found := slices.BinarySearchFunc(entries, name, func(e directory.Entry, name string) int {
		return strings.Compare(e.Name, name)
	})
	if !found {
		return nil, syscall.ENOENT
	}
	in, err := n.img.inodeAt(entries[i].BlockStart, entries[i].Offset)
	if err != nil {
		return nil, syscall.EIO
	}
	child := &sqNode{img: n.img, base: squashfslow.FileBase{Inode: in, Name: name}}
	child.fillAttr(&out.Attr)
	out.SetEntryTimeout(imageKernelTTL)
	out.SetAttrTimeout(imageKernelTTL)
	return n.NewInode(ctx, child, fs.StableAttr{Mode: out.Attr.Mode & syscall.S_IFMT, Ino: child.ino()}), 0
}

func (n *sqNode) Readdir(context.Context) (fs.DirStream, syscall.Errno) {
	entries, errno := n.dir()
	if errno != 0 {
		return nil, errno
	}
	list := make([]fuse.DirEntry, 0, len(entries))
	for _, e := range entries {
		list = append(list, fuse.DirEntry{Name: e.Name, Ino: uint64(e.Num) + 1, Mode: sqTypeMode(e.InodeType)})
	}
	return fs.NewListDirStream(list), 0
}

// OpendirHandle : contenu immuable, le noyau garde la liste du dossier en
// cache (FOPEN_CACHE_DIR) d'une ouverture à l'autre. Wine relit souvent les
// dossiers (recherche de noms sans tenir compte de la casse).
func (n *sqNode) OpendirHandle(context.Context, uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if !n.base.IsDir() {
		return nil, 0, syscall.ENOTDIR
	}
	return &sqDirHandle{node: n}, fuse.FOPEN_CACHE_DIR | fuse.FOPEN_KEEP_CACHE, 0
}

// sqDirHandle : dossier ouvert ; liste construite à la première lecture.
type sqDirHandle struct {
	node *sqNode
	ds   fs.DirStream
}

func (d *sqDirHandle) stream(ctx context.Context) syscall.Errno {
	if d.ds != nil {
		return 0
	}
	ds, errno := d.node.Readdir(ctx)
	d.ds = ds
	return errno
}

func (d *sqDirHandle) Readdirent(ctx context.Context) (*fuse.DirEntry, syscall.Errno) {
	if errno := d.stream(ctx); errno != 0 {
		return nil, errno
	}
	if !d.ds.HasNext() {
		return nil, 0
	}
	e, errno := d.ds.Next()
	return &e, errno
}

func (d *sqDirHandle) Seekdir(ctx context.Context, off uint64) syscall.Errno {
	if errno := d.stream(ctx); errno != 0 {
		return errno
	}
	if sd, ok := d.ds.(fs.FileSeekdirer); ok {
		return sd.Seekdir(ctx, off)
	}
	return syscall.ENOTSUP
}

func (d *sqDirHandle) Releasedir(context.Context, uint32) {
	if d.ds != nil {
		d.ds.Close()
	}
}

func sqLinkTarget(in inode.Inode) ([]byte, bool) {
	switch s := in.Data.(type) {
	case inode.Symlink:
		return s.Target, true
	case inode.ESymlink:
		return s.Target, true
	}
	return nil, false
}

func (n *sqNode) Readlink(context.Context) ([]byte, syscall.Errno) {
	if t, ok := sqLinkTarget(n.base.Inode); ok {
		return t, 0
	}
	return nil, syscall.EINVAL
}

func (n *sqNode) Open(_ context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if flags&(syscall.O_WRONLY|syscall.O_RDWR) != 0 {
		return nil, 0, syscall.EROFS
	}
	if !n.base.IsRegular() {
		return nil, 0, syscall.EINVAL
	}
	// Contenu immuable : le cache de pages du noyau reste valable d'une
	// ouverture à l'autre. NOFLUSH : pas d'aller-retour FLUSH à chaque
	// fermeture (rien à écrire ; sensible avec des milliers de petits fichiers).
	return nil, fuse.FOPEN_KEEP_CACHE | fuse.FOPEN_NOFLUSH, 0
}

// layout : blocs du fichier, lus au premier accès.
func (n *sqNode) layout() (*sqFileLayout, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.file == nil {
		l, err := n.img.layout(n.base.Inode)
		if err != nil {
			return nil, err
		}
		n.file = l
	}
	return n.file, nil
}

func (n *sqNode) Read(_ context.Context, _ fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	l, err := n.layout()
	if err != nil {
		return nil, syscall.EIO
	}
	size := int64(l.size)
	if off >= size || len(dest) == 0 {
		return fuse.ReadResultData(nil), 0
	}
	end := min(off+int64(len(dest)), size)
	bs := int64(n.img.rdr.Superblock.BlockSize)
	got := 0
	for pos := off; pos < end; {
		idx := uint32(pos / bs)
		b, err := n.img.fileBlock(l, idx)
		if err != nil {
			return nil, syscall.EIO
		}
		in := pos - int64(idx)*bs
		if in >= int64(len(b)) {
			return nil, syscall.EIO
		}
		c := copy(dest[got:end-off], b[in:])
		got += c
		pos += int64(c)
	}
	// Lecture séquentielle (elle reprend où la précédente s'est arrêtée) : les
	// blocs suivants seront demandés. Pas en accès aléatoire : 8 blocs
	// décompressés pour rien à chaque lecture.
	if n.lastEnd.Swap(end) == off {
		n.img.prefetch(l, uint32((end-1)/bs)+1, imagePrefetchBlocks)
	}
	return fuse.ReadResultData(dest[:got]), 0
}

// Serve : ouvre l'image, la monte sur o.Point et la sert jusqu'au démontage ;
// SIGTERM/SIGINT/SIGHUP démontent proprement. Les erreurs d'ouverture et de
// montage sont renvoyées ; l'appelant ajoute son préfixe.
func Serve(o Options) error {
	img, err := openSquashImage(o.Image, o.UID, o.GID)
	if err != nil {
		return fmt.Errorf("%s : %w", o.Image, err)
	}
	root := &sqNode{img: img, base: img.rdr.Root.FileBase}
	ttl := imageKernelTTL
	srv, err := fs.Mount(o.Point, root, &fs.Options{
		EntryTimeout:    &ttl,
		AttrTimeout:     &ttl,
		NegativeTimeout: &ttl, // nom absent : réponse gardée par le noyau (image immuable)
		NullPermissions: true, // droits de l'image tels quels (0000 compris)
		MountOptions: fuse.MountOptions{
			FsName:       o.Image,
			Name:         "squashfuse",
			AllowOther:   o.AllowOther,
			DirectMount:  true, // root du namespace : mount(2) sans fusermount
			MaxReadAhead: 1 << 20,
			MaxWrite:     1 << 20, // requêtes de lecture de 1 Mio (128 Kio sinon)
			Options:      []string{"ro"},
		},
	})
	if err != nil {
		return fmt.Errorf("montage impossible : %w", err)
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		<-sig
		if srv.Unmount() != nil {
			_ = syscall.Unmount(o.Point, syscall.MNT_DETACH)
		}
	}()
	srv.Wait()
	return nil
}

// ParseArgs : arguments de --serve-image, IMAGE POINT [uid N] [gid N]
// [allow_other].
func ParseArgs(args []string) (Options, error) {
	o := Options{UID: -1, GID: -1}
	var pos []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "uid", "gid":
			if i+1 >= len(args) {
				return o, errors.New(a + " sans valeur")
			}
			v, err := strconv.Atoi(args[i+1])
			if err != nil || v < 0 {
				return o, errors.New(a + " invalide : " + args[i+1])
			}
			if a == "uid" {
				o.UID = v
			} else {
				o.GID = v
			}
			i++
		case "allow_other":
			o.AllowOther = true
		default:
			pos = append(pos, a)
		}
	}
	if len(pos) != 2 {
		return o, errors.New("usage : --serve-image IMAGE POINT [uid N] [gid N] [allow_other]")
	}
	o.Image, o.Point = pos[0], pos[1]
	return o, nil
}
