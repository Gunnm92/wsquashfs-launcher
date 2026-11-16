package main

// Lecture des blocs d'une image squashfs pour imagefs.go : métadonnées
// (inodes, dossiers, table des fragments), données et fragments, avec caches
// partagés. La bibliothèque CalebQ42/squashfs ne sert qu'à décoder les
// structures (superbloc, inodes, entrées de dossier, table des UID/GID) : la
// sienne relit et décompresse un bloc de métadonnées à chaque recherche, sans
// cache (16/11 : 15 000 entrées parcourues en 1,5 s contre 0,3 s pour
// squashfuse_ll).

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sync"

	"github.com/CalebQ42/squashfs/low/directory"
	"github.com/CalebQ42/squashfs/low/inode"
	"github.com/klauspost/compress/zlib"
	"github.com/klauspost/compress/zstd"
	"github.com/mikelolasagasti/xz"
	"github.com/pierrec/lz4/v4"
)

// Compressions squashfs (superbloc) : 309 images sur 318 en zstd, le reste en
// gzip (16/11) ; xz et lz4 en plus. lzma et lzo : mode copy (unsquashfs).
const (
	sqCompGzip = 1
	sqCompXz   = 4
	sqCompLz4  = 5
	sqCompZstd = 6
)

const (
	sqMetaSize        = 8192    // bloc de métadonnées décompressé
	sqUncompressedBit = 1 << 24 // bloc de données stocké tel quel
	sqNoFragment      = 0xFFFFFFFF
)

type sqDecompressor func(src []byte, max int) ([]byte, error)

func newSqDecompressor(comp uint16) (sqDecompressor, error) {
	switch comp {
	case sqCompZstd:
		d, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(runtime.NumCPU()))
		if err != nil {
			return nil, err
		}
		return func(src []byte, max int) ([]byte, error) {
			return d.DecodeAll(src, make([]byte, 0, max))
		}, nil
	case sqCompGzip:
		var pool sync.Pool
		return func(src []byte, max int) ([]byte, error) {
			var r io.ReadCloser
			var err error
			if v := pool.Get(); v != nil {
				r = v.(io.ReadCloser)
				err = r.(zlib.Resetter).Reset(bytes.NewReader(src), nil)
			} else {
				r, err = zlib.NewReader(bytes.NewReader(src))
			}
			if err != nil {
				return nil, err
			}
			defer pool.Put(r)
			return sqReadAll(r, max)
		}, nil
	case sqCompXz:
		return func(src []byte, max int) ([]byte, error) {
			r, err := xz.NewReader(bytes.NewReader(src), 0)
			if err != nil {
				return nil, err
			}
			return sqReadAll(r, max)
		}, nil
	case sqCompLz4:
		// squashfs : blocs LZ4 bruts (LZ4_decompress_safe), pas le format « frame ».
		return func(src []byte, max int) ([]byte, error) {
			out := make([]byte, max)
			n, err := lz4.UncompressBlock(src, out)
			return out[:n], err
		}, nil
	}
	return nil, fmt.Errorf("compression %d non prise en charge (mode copy)", comp)
}

func sqReadAll(r io.Reader, max int) ([]byte, error) {
	out := bytes.NewBuffer(make([]byte, 0, max))
	_, err := out.ReadFrom(io.LimitReader(r, int64(max)+1))
	if out.Len() > max {
		return nil, errors.New("bloc décompressé trop grand")
	}
	return out.Bytes(), err
}

// sqMetaBlock : bloc de métadonnées décompressé et position du suivant.
type sqMetaBlock struct {
	data []byte
	next int64
}

// metaBlock : bloc de métadonnées à la position absolue off, en cache.
func (s *squashImage) metaBlock(off int64) (*sqMetaBlock, error) {
	if v, ok := s.meta.get(off); ok {
		return v.(*sqMetaBlock), nil
	}
	var hdr [2]byte
	if _, err := s.file.ReadAt(hdr[:], off); err != nil {
		return nil, err
	}
	h := binary.LittleEndian.Uint16(hdr[:])
	size := int(h &^ 0x8000)
	raw := make([]byte, size)
	if _, err := s.file.ReadAt(raw, off+2); err != nil {
		return nil, err
	}
	data := raw
	if h&0x8000 == 0 {
		var err error
		if data, err = s.dec(raw, sqMetaSize); err != nil {
			return nil, err
		}
	}
	b := &sqMetaBlock{data: data, next: off + 2 + int64(size)}
	s.meta.put(off, b)
	return b, nil
}

// sqMetaReader : lecture continue des métadonnées à partir d'un bloc et d'un
// décalage dans ce bloc (référence d'inode, entrée de dossier).
type sqMetaReader struct {
	img *squashImage
	blk *sqMetaBlock
	pos int
	err error
}

func (s *squashImage) metaReader(block int64, offset int) *sqMetaReader {
	r := &sqMetaReader{img: s, pos: offset}
	r.blk, r.err = s.metaBlock(block)
	return r
}

func (r *sqMetaReader) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		if r.err != nil {
			return n, r.err
		}
		if r.pos >= len(r.blk.data) {
			r.pos -= len(r.blk.data)
			r.blk, r.err = r.img.metaBlock(r.blk.next)
			continue
		}
		c := copy(p[n:], r.blk.data[r.pos:])
		r.pos += c
		n += c
	}
	return n, nil
}

// inodeAt : inode d'une entrée de dossier (bloc relatif à la table des
// inodes, décalage dans le bloc décompressé).
func (s *squashImage) inodeAt(block uint32, offset uint16) (inode.Inode, error) {
	sb := &s.rdr.Superblock
	return inode.Read(s.metaReader(int64(sb.InodeTableStart)+int64(block), int(offset)), sb.BlockSize)
}

// readDir : entrées d'un dossier, triées par nom (format squashfs).
func (s *squashImage) readDir(in inode.Inode) ([]directory.Entry, error) {
	var block uint32
	var size uint32
	var offset uint16
	switch d := in.Data.(type) {
	case inode.Directory:
		block, size, offset = d.BlockStart, uint32(d.Size), d.Offset
	case inode.EDirectory:
		block, size, offset = d.BlockStart, d.Size, d.Offset
	default:
		return nil, errors.New("pas un dossier")
	}
	if size <= 3 { // dossier vide
		return nil, nil
	}
	return directory.ReadDirectory(s.metaReader(int64(s.rdr.Superblock.DirTableStart)+int64(block), int(offset)), size)
}

// sqFragment : entrée de la table des fragments.
type sqFragment struct {
	start uint64
	size  uint32
}

// fragIndex : positions des blocs de la table des fragments (512 entrées par
// bloc), lues une fois : sinon un pread par lecture de petit fichier.
func (s *squashImage) fragIndex() ([]uint64, error) {
	s.fragOnce.Do(func() {
		sb := &s.rdr.Superblock
		raw := make([]byte, (int(sb.FragCount)+511)/512*8)
		if _, s.fragErr = s.file.ReadAt(raw, int64(sb.FragTableStart)); s.fragErr == nil {
			s.fragPtrs = make([]uint64, len(raw)/8)
			for i := range s.fragPtrs {
				s.fragPtrs[i] = binary.LittleEndian.Uint64(raw[i*8:])
			}
		}
	})
	return s.fragPtrs, s.fragErr
}

// fragment : entrée i de la table des fragments.
func (s *squashImage) fragment(i uint32) (sqFragment, error) {
	if i >= s.rdr.Superblock.FragCount {
		return sqFragment{}, errors.New("fragment hors table")
	}
	ptrs, err := s.fragIndex()
	if err != nil {
		return sqFragment{}, err
	}
	r := s.metaReader(int64(ptrs[i/512]), int(i%512)*16)
	var e [16]byte
	if _, err := io.ReadFull(r, e[:]); err != nil {
		return sqFragment{}, err
	}
	return sqFragment{binary.LittleEndian.Uint64(e[0:8]), binary.LittleEndian.Uint32(e[8:12])}, nil
}

// sqInflight : décompression en cours d'un bloc, partagée entre lecteurs
// (lecture du jeu et lecture anticipée).
type sqInflight struct {
	done chan struct{}
	data []byte
	err  error
}

// dataBlock : bloc de données (ou de fragments) stocké à off, sizeField tel
// que dans l'inode (bit 24 : non compressé), en cache ; un bloc déjà en cours
// de décompression est attendu, pas refait.
func (s *squashImage) dataBlock(off uint64, sizeField uint32) ([]byte, error) {
	if v, ok := s.blocks.get(off); ok {
		return v.([]byte), nil
	}
	s.inflightMu.Lock()
	if c, ok := s.inflight[off]; ok {
		s.inflightMu.Unlock()
		<-c.done
		return c.data, c.err
	}
	c := &sqInflight{done: make(chan struct{})}
	s.inflight[off] = c
	s.inflightMu.Unlock()
	c.data, c.err = s.loadBlock(off, sizeField)
	if c.err == nil {
		s.blocks.put(off, c.data)
	}
	s.inflightMu.Lock()
	delete(s.inflight, off)
	s.inflightMu.Unlock()
	close(c.done)
	return c.data, c.err
}

// prefetch : décompresse en tâche de fond les blocs from..from+n-1 du fichier
// (lecture séquentielle : le décodage des blocs suivants se fait sur les
// autres cœurs pendant que le jeu consomme le bloc courant).
func (s *squashImage) prefetch(l *sqFileLayout, from uint32, n int) {
	for i := from; i < from+uint32(n) && int(i) < len(l.sizes); i++ {
		if l.sizes[i] == 0 {
			continue
		}
		if _, ok := s.blocks.get(l.offsets[i]); ok {
			continue
		}
		select {
		case s.prefetchSlots <- struct{}{}:
		default:
			return // tous les cœurs déjà occupés
		}
		go func(off uint64, size uint32) {
			defer func() { <-s.prefetchSlots }()
			_, _ = s.dataBlock(off, size)
		}(l.offsets[i], l.sizes[i])
	}
}

func (s *squashImage) loadBlock(off uint64, sizeField uint32) ([]byte, error) {
	size := sizeField &^ sqUncompressedBit
	raw := make([]byte, size)
	if _, err := s.file.ReadAt(raw, int64(off)); err != nil {
		return nil, err
	}
	data := raw
	if sizeField&sqUncompressedBit == 0 {
		var err error
		if data, err = s.dec(raw, int(s.rdr.Superblock.BlockSize)); err != nil {
			return nil, err
		}
	}
	return data, nil
}

// sqFileLayout : emplacement des blocs d'un fichier régulier.
type sqFileLayout struct {
	size       uint64
	sizes      []uint32 // taille stockée de chaque bloc (0 : creux)
	offsets    []uint64 // position de chaque bloc
	fragIndex  uint32
	fragOffset uint32
}

func (s *squashImage) layout(in inode.Inode) (*sqFileLayout, error) {
	var l sqFileLayout
	var start uint64
	switch f := in.Data.(type) {
	case inode.File:
		start, l.size, l.sizes, l.fragIndex, l.fragOffset = uint64(f.BlockStart), uint64(f.Size), f.BlockSizes, f.FragInd, f.FragOffset
	case inode.EFile:
		start, l.size, l.sizes, l.fragIndex, l.fragOffset = f.BlockStart, f.Size, f.BlockSizes, f.FragInd, f.FragOffset
	default:
		return nil, errors.New("pas un fichier")
	}
	l.offsets = make([]uint64, len(l.sizes))
	for i, sz := range l.sizes {
		l.offsets[i] = start
		start += uint64(sz &^ sqUncompressedBit)
	}
	return &l, nil
}

// fileBlock : bloc i décompressé d'un fichier (dernier : fragment éventuel).
// Bloc creux : des zéros, de la longueur attendue.
func (s *squashImage) fileBlock(l *sqFileLayout, i uint32) ([]byte, error) {
	bs := uint64(s.rdr.Superblock.BlockSize)
	want := min(bs, l.size-uint64(i)*bs)
	if int(i) < len(l.sizes) {
		if l.sizes[i] == 0 {
			return make([]byte, want), nil
		}
		b, err := s.dataBlock(l.offsets[i], l.sizes[i])
		if err == nil && uint64(len(b)) < want {
			err = errors.New("bloc tronqué")
		}
		return b, err
	}
	if l.fragIndex == sqNoFragment || int(i) != len(l.sizes) {
		return nil, errors.New("bloc hors fichier")
	}
	fr, err := s.fragment(l.fragIndex)
	if err != nil {
		return nil, err
	}
	b, err := s.dataBlock(fr.start, fr.size)
	if err != nil {
		return nil, err
	}
	end := uint64(l.fragOffset) + want
	if end > uint64(len(b)) {
		return nil, errors.New("fragment tronqué")
	}
	return b[l.fragOffset:end], nil
}
