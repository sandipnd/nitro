package plasma

import (
	"github.com/couchbase/nitro/skiplist"
	"reflect"
	"sort"
	"time"
	"unsafe"
)

func memcopy(dst, src unsafe.Pointer, sz int) {
	var sb, db []byte
	hdrsb := (*reflect.SliceHeader)(unsafe.Pointer(&sb))
	hdrsb.Len = sz
	hdrsb.Cap = hdrsb.Len
	hdrsb.Data = uintptr(src)

	hdrdb := (*reflect.SliceHeader)(unsafe.Pointer(&db))
	hdrdb.Len = sz
	hdrdb.Cap = hdrdb.Len
	hdrdb.Data = uintptr(dst)
	copy(db, sb)
}

type pageItemSorter struct {
	itms []PageItem
	cmp  skiplist.CompareFn
}

func (s *pageItemSorter) Run() []PageItem {
	sort.Stable(s)
	return s.itms
}

func (s *pageItemSorter) Len() int {
	return len(s.itms)
}

func (s *pageItemSorter) Less(i, j int) bool {
	return s.cmp(s.itms[i].Item(), s.itms[j].Item()) < 0
}

func (s *pageItemSorter) Swap(i, j int) {
	s.itms[i], s.itms[j] = s.itms[j], s.itms[i]
}

func minLSSOffset(a, b LSSOffset) LSSOffset {
	if a < b {
		return a
	}

	return b
}

type Buffer struct {
	bs []byte
}

func (b *Buffer) Grow(offset, size int) {
	if len(b.bs) < offset+size {
		sz := len(b.bs) * 2
		if sz < offset+size {
			sz = offset + size
		}

		newBuf := make([]byte, sz)
		copy(newBuf, b.bs)
		b.bs = newBuf
	}
}

func (b *Buffer) Get(offset int, size int) []byte {
	b.Grow(offset, size)
	return b.bs[offset : offset+size]
}

func (b *Buffer) Ptr(offset int) unsafe.Pointer {
	return unsafe.Pointer(&b.bs[offset])
}

func newBuffer(size int) *Buffer {
	return &Buffer{
		bs: make([]byte, size),
	}
}

type DecayInterval struct {
	initial time.Duration
	curr    time.Duration
	final   time.Duration
	incr    time.Duration
}

func NewDecayInterval(initial, final time.Duration) DecayInterval {
	return DecayInterval{
		initial: initial,
		curr:    final,
		final:   final,
		incr:    final / initial,
	}
}

func (d *DecayInterval) Sleep() {
	time.Sleep(d.curr)
	if d.curr < d.final {
		d.curr += d.incr
	}
}

func (d *DecayInterval) Reset() {
	d.curr = d.initial
}
