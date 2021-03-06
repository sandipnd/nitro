package plasma

import (
	"encoding/binary"
	"errors"
	"sync/atomic"
	"unsafe"
)

var ErrItemNotFound = errors.New("item not found")
var ErrItemNoValue = errors.New("item has no value")
var ErrKeyTooLarge = errors.New("key is too large")

type Snapshot struct {
	sn       uint64
	refCount int32
	child    *Snapshot
	db       *Plasma

	count     int64
	persisted bool
	meta      []byte
}

func (sn *Snapshot) Count() int64 {
	return sn.count
}

type rollbackSn struct {
	start, end uint64
}

type rollbackFilter struct {
	filters []*rollbackSn
}

func (f *rollbackFilter) Process(o PageItem) PageItemsList {
	if f.filters == nil {
		return o
	}

	itm := (*item)(o.Item())
	sn := itm.Sn()
	for _, filter := range f.filters {
		if sn >= filter.start && sn <= filter.end {
			return nilPageItemsList
		}
	}

	return o
}

func (f *rollbackFilter) AddFilter(o interface{}) {
	rbf := o.(*rollbackSn)
	f.filters = append(f.filters, rbf)
}

func (f *rollbackFilter) Reset() {
	f.filters = nil
}

// Used by snapshot iterator
type snFilter struct {
	sn   uint64
	skip bool
	rollbackFilter
}

func (f *snFilter) Process(o PageItem) PageItemsList {
	if f.rollbackFilter.Process(o) == nilPageItemsList {
		return nilPageItemsList
	}

	itm := (*item)(o.Item())
	if f.skip || itm.Sn() > f.sn {
		f.skip = false
		return nilPageItemsList
	}

	if !itm.IsInsert() {
		f.skip = true
		return nilPageItemsList
	}

	return o
}

// Used by page compactor to GC dead snapshot items
type gcFilter struct {
	snIntervals []uint64

	skipItm *item
	rollbackFilter
}

func (f *gcFilter) findInterval(sn uint64) (int, bool) {
	in := -1

	for i := 0; i < len(f.snIntervals)-1; i++ {
		if sn > f.snIntervals[i] && sn <= f.snIntervals[i+1] {
			in = i
			break
		}
	}

	return in, in > -1
}

func (f *gcFilter) inInterval(in int, sn uint64) bool {
	return sn > f.snIntervals[in] && sn < f.snIntervals[in+1]
}

func (f *gcFilter) Process(o PageItem) PageItemsList {
	if f.rollbackFilter.Process(o) == nilPageItemsList {
		return nilPageItemsList
	}

	itm := (*item)(o.Item())
	sn := itm.Sn()
	skipItm := f.skipItm
	f.skipItm = nil

	if !itm.IsInsert() {
		f.skipItm = itm
		return nilPageItemsList
	}

	if skipItm != nil {
		if skipItm.Sn() == sn {
			return nilPageItemsList
		}

		if in, ok := f.findInterval(skipItm.Sn()); ok {
			if f.inInterval(in, sn) {
				return nilPageItemsList
			}
		}

		return (*pageItemsList)(&[]PageItem{skipItm, o})
	}

	return o
}

func (s *Snapshot) Close() {
	if atomic.AddInt32(&s.refCount, -1) == 0 {
		atomic.AddUint64(&s.db.gcSn, 1)
		s.child.Close()
	}
}

type MVCCIterator struct {
	snap *Snapshot
	*Iterator
	token TxToken
}

func (itr *MVCCIterator) Seek(k []byte) {
	sn := atomic.LoadUint64(&itr.snap.db.currSn)
	kbuf := itr.Iterator.GetBuffer(bufTempItem)
	newItm, _ := newItem(k, nil, sn, false, kbuf)
	itm := unsafe.Pointer(newItm)
	itr.Iterator.Seek(itm)
}

func (itr *MVCCIterator) Key() []byte {
	return (*item)(itr.Get()).Key()
}

func (itr *MVCCIterator) Value() []byte {
	return (*item)(itr.Get()).Value()
}

func (itr *MVCCIterator) HasValue() bool {
	return (*item)(itr.Get()).HasValue()
}

func (itr *MVCCIterator) Close() {
	itr.snap.Close()
	itr.Iterator.Close()
	itr.EndTx(itr.token)
}

func (s *Snapshot) NewIterator() *MVCCIterator {
	s.Open()
	itr := s.db.NewIterator().(*Iterator)
	itr.filter = &snFilter{
		sn: s.sn,
	}

	tok := itr.BeginTx()
	return &MVCCIterator{
		token:    tok,
		snap:     s,
		Iterator: itr,
	}
}

func (s *Snapshot) Open() {
	atomic.AddInt32(&s.refCount, 1)
}

func (s *Plasma) NewSnapshot() (snap *Snapshot) {
	s.mvcc.Lock()
	defer s.mvcc.Unlock()
	return s.newSnapshot()
}

func (s *Plasma) newSnapshot() (snap *Snapshot) {

	if !s.EnableShapshots {
		panic("snapshots not enabled")
	}

	snap = s.currSnapshot

	nextSnap := &Snapshot{
		sn:       atomic.AddUint64(&s.currSn, 1),
		refCount: 2,
		db:       s,
	}

	s.currSnapshot.child = nextSnap
	s.currSnapshot = nextSnap
	s.updateMaxSn(nextSnap.sn, false)

	var smrList [][]reclaimObject
	for _, w := range s.wlist {
		if s.useMemMgmt {
			if len(w.wCtx.reclaimList) > 0 {
				smrList = append(smrList, w.wCtx.reclaimList)
				w.wCtx.reclaimList = nil
			}

		}

		s.itemsCount += w.count
		w.count = 0
	}

	snap.count = s.itemsCount
	s.FreeObjects(smrList)

	return
}

func (w *Writer) InsertKV(k, v []byte) error {
	sn := atomic.LoadUint64(&w.currSn)
	itmBuf := w.GetBuffer(bufTempItem)
	itm, err := newItem(k, v, sn, false, itmBuf)
	if err != nil {
		return err
	}

	w.count++
	return w.Insert(unsafe.Pointer(itm))
}

func (w *Writer) DeleteKV(k []byte) error {
	sn := atomic.LoadUint64(&w.currSn)
	itmBuf := w.GetBuffer(bufTempItem)
	itm, err := newItem(k, nil, sn, true, itmBuf)
	if err != nil {
		return err
	}

	w.count--
	return w.Insert(unsafe.Pointer(itm))
}

func (w *Writer) LookupKV(k []byte) ([]byte, error) {
	itmBuf := w.GetBuffer(bufTempItem)
	itm, err := newItem(k, nil, 0, false, itmBuf)
	if err != nil {
		return nil, err
	}

	o, err := w.Lookup(unsafe.Pointer(itm))
	itm = (*item)(o)

	if err != nil {
		return nil, err
	}

	if itm == nil || !itm.IsInsert() {
		return nil, ErrItemNotFound
	}

	if itm.HasValue() {
		return itm.Value(), nil
	}

	return nil, ErrItemNoValue
}

type RecoveryPoint struct {
	sn    uint64
	count int64
	meta  []byte
}

func (rp *RecoveryPoint) Meta() []byte {
	return rp.meta
}

func (s *Plasma) updateRecoveryPoints(rps []*RecoveryPoint) {
	if s.shouldPersist {
		version := s.rpVersion + 1
		bs := marshalRPs(rps, version)
		_, wbuf, res := s.lss.ReserveSpace(len(bs) + lssBlockTypeSize)
		writeLSSBlock(wbuf, lssRecoveryPoints, bs)
		s.lss.FinalizeWrite(res)

		s.rpVersion = version
		s.recoveryPoints = rps
	}
}

func (s *Plasma) updateRPSns(rps []*RecoveryPoint) {
	rpSns := make([]uint64, len(rps))
	for i, rp := range rps {
		rpSns[i] = rp.sn
	}
	atomic.StorePointer((*unsafe.Pointer)(unsafe.Pointer(&s.rpSns)), unsafe.Pointer(&rpSns))
}

func (s *Plasma) CreateRecoveryPoint(sn *Snapshot, meta []byte) error {
	if s.shouldPersist {
		// Prepare
		s.mvcc.Lock()
		rp := &RecoveryPoint{
			sn:    sn.sn,
			count: sn.count,
			meta:  meta,
		}

		rps := append(s.recoveryPoints, rp)
		s.updateRecoveryPoints(rps)
		s.updateRPSns(rps)

		s.mvcc.Unlock()

		sn.Close()
		s.PersistAll()

		// Commit
		s.mvcc.Lock()
		s.updateRecoveryPoints(rps)
		s.mvcc.Unlock()

		s.lss.Sync(true)
	} else {
		sn.Close()
	}
	return nil
}

func (s *Plasma) GetRecoveryPoints() []*RecoveryPoint {
	s.mvcc.RLock()
	defer s.mvcc.RUnlock()
	return s.recoveryPoints
}

func (s *Plasma) Rollback(rollRP *RecoveryPoint) (*Snapshot, error) {
	s.mvcc.Lock()
	defer s.mvcc.Unlock()

	start := rollRP.sn + 1
	end := s.currSn

	callb := func(pid PageId, partn RangePartition) error {
		w := s.persistWriters[partn.Shard]
		pgBuf := w.GetBuffer(bufPersist)
	retry:
		if pg, err := s.ReadPage(pid, w.pgRdrFn, false, w); err == nil {
			pg.Rollback(start, end)
			pgBuf, fdSz, staleFdSz, numSegments := pg.Marshal(pgBuf, s.Config.MaxPageLSSSegments)
			offset, wbuf, res := s.lss.ReserveSpace(len(pgBuf) + lssBlockTypeSize)
			typ := pgFlushLSSType(pg, numSegments)
			writeLSSBlock(wbuf, typ, pgBuf)
			pg.AddFlushRecord(offset, fdSz, numSegments)
			s.lss.FinalizeWrite(res)
			w.sts.FlushDataSz += int64(fdSz) - int64(staleFdSz)

			// May conflict with cleaner
			if !s.UpdateMapping(pid, pg, w) {
				goto retry
			}

		} else {
			return err
		}

		return nil
	}

	if err := s.PageVisitor(callb, s.NumPersistorThreads); err != nil {
		return nil, err
	}

	s.lss.Sync(false)

	s.itemsCount = rollRP.count
	newSnap := s.newSnapshot()
	var newRpts []*RecoveryPoint
	for _, rp := range s.recoveryPoints {
		if rp.sn <= rollRP.sn {
			newRpts = append(newRpts, rp)
		}
	}

	s.updateRecoveryPoints(newRpts)
	s.gcSn = newSnap.sn

	s.lss.Sync(true)
	return newSnap, nil
}

func (s *Plasma) RemoveRecoveryPoint(rmRP *RecoveryPoint) {
	s.mvcc.Lock()
	defer s.mvcc.Unlock()

	var newRpts []*RecoveryPoint
	for _, rp := range s.recoveryPoints {
		if rp.sn != rmRP.sn {
			newRpts = append(newRpts, rp)
		}
	}

	s.updateRecoveryPoints(newRpts)
	s.updateRPSns(newRpts)
}

func marshalRPs(rps []*RecoveryPoint, version uint16) []byte {
	var l int
	for _, rp := range rps {
		l += 4 + 8 + 8 + len(rp.meta)
	}

	bs := make([]byte, 2+2+l)
	binary.BigEndian.PutUint16(bs[:2], version)
	offset := 2
	binary.BigEndian.PutUint16(bs[offset:offset+2], uint16(len(rps)))
	offset += 2
	for _, rp := range rps {
		l := uint32(4 + 8 + 8 + len(rp.meta))
		binary.BigEndian.PutUint32(bs[offset:offset+4], l)
		offset += 4
		binary.BigEndian.PutUint64(bs[offset:offset+8], rp.sn)
		offset += 8
		binary.BigEndian.PutUint64(bs[offset:offset+8], uint64(rp.count))
		offset += 8
		copy(bs[offset:], rp.meta)
		offset += len(rp.meta)
	}

	return bs
}

func unmarshalRPs(bs []byte) (version uint16, rps []*RecoveryPoint) {
	version = binary.BigEndian.Uint16(bs[:2])
	offset := 2
	n := int(binary.BigEndian.Uint16(bs[offset : offset+2]))
	offset += 2
	for i := 0; i < n; i++ {
		rp := new(RecoveryPoint)
		l := int(binary.BigEndian.Uint32(bs[offset : offset+4]))
		endOffset := offset + l
		offset += 4
		rp.sn = binary.BigEndian.Uint64(bs[offset : offset+8])
		offset += 8
		rp.count = int64(binary.BigEndian.Uint64(bs[offset : offset+8]))
		offset += 8
		rp.meta = append([]byte(nil), bs[offset:endOffset]...)
		rps = append(rps, rp)
		offset = endOffset
	}

	return
}

func (s *Plasma) updateMaxSn(sn uint64, force bool) {
	if s.shouldPersist {
		freq := s.MaxSnSyncFrequency
		if s.numSnCreated%freq == 0 || force {
			var bs [8]byte
			maxSn := sn + uint64(freq+1)
			binary.BigEndian.PutUint64(bs[:], maxSn)
			_, wbuf, res := s.lss.ReserveSpace(len(bs) + lssBlockTypeSize)
			writeLSSBlock(wbuf, lssMaxSn, bs[:])
			s.lss.FinalizeWrite(res)
			s.lss.Sync(true)
			s.lastMaxSn = maxSn
		}

		s.numSnCreated++
	}
}

func decodeMaxSn(data []byte) uint64 {
	return binary.BigEndian.Uint64(data)
}
