package plasma

import (
	"reflect"
	"unsafe"
)

var (
	recDeltaSize        = unsafe.Sizeof(*new(recordDelta))
	basePageSize        = unsafe.Sizeof(*new(basePage))
	splitPageDeltaSize  = unsafe.Sizeof(*new(splitPageDelta))
	mergePageDeltaSize  = unsafe.Sizeof(*new(mergePageDelta))
	flushPageDeltaSize  = unsafe.Sizeof(*new(flushPageDelta))
	removePageDeltaSize = unsafe.Sizeof(*new(removePageDelta))
	rollbackDeltaSize   = unsafe.Sizeof(*new(rollbackDelta))
)

//pg.memUsed -= pg.ComputeMemUsed()
//w.sts.MemSz += int64(pg.GetMemUsed())

// TODO: meta delta

type allocCtx struct {
	allocDeltaList []*pageDelta
	freePageList   []*pageDelta
}

func (ctx *allocCtx) addDeltaAlloc(ptr unsafe.Pointer) {
	ctx.allocDeltaList = append(ctx.allocDeltaList, (*pageDelta)(ptr))
}

func (ctx *allocCtx) freePg(ptr *pageDelta) {
	if ptr != nil {
		ctx.freePageList = append(ctx.freePageList, ptr)
	}
}

func (pg *page) allocRecordDelta(itm unsafe.Pointer) *recordDelta {
	l := pg.itemSize(itm)
	size := recDeltaSize + l
	pg.memUsed += int(size)

	if pg.useMemMgmt {
		ptr := pg.allocMM(size)
		d := (*recordDelta)(ptr)
		d.itm = unsafe.Pointer(uintptr(ptr) + recDeltaSize)
		memcopy(d.itm, itm, int(l))
		pg.addDeltaAlloc(ptr)
		return d
	}

	d := new(recordDelta)
	d.itm = pg.dup(itm)
	return d
}

func (pg *page) allocBasePage(n int, dataSz uintptr, hiItm unsafe.Pointer) *basePage {
	hiItmSize := pg.itemSize(hiItm)
	size := basePageSize + dataSz + uintptr(n)*8 + hiItmSize
	pg.memUsed += int(size)

	if pg.useMemMgmt {
		ptr := pg.allocMM(size)
		bp := (*basePage)(ptr)
		sh := (*reflect.SliceHeader)(unsafe.Pointer(&bp.items))
		sh.Data = uintptr(ptr) + basePageSize
		sh.Len = n
		sh.Cap = n
		bp.data = unsafe.Pointer(uintptr(ptr) + basePageSize + uintptr(n)*8)
		bp.hiItm = unsafe.Pointer(uintptr(ptr) + basePageSize + uintptr(n)*8 + dataSz)
		memcopy(bp.hiItm, hiItm, int(hiItmSize))
		pg.addDeltaAlloc(ptr)
		return bp
	}

	bp := new(basePage)
	bp.items = make([]unsafe.Pointer, n)
	bp.data = pg.alloc(dataSz)
	bp.hiItm = pg.dup(hiItm)
	return bp

}

func (pg *page) allocSplitPageDelta(itm unsafe.Pointer) *splitPageDelta {
	l := pg.itemSize(itm)
	size := splitPageDeltaSize + l
	pg.memUsed += int(size)

	if pg.useMemMgmt {
		ptr := pg.allocMM(size)
		d := (*splitPageDelta)(ptr)
		d.itm = unsafe.Pointer(uintptr(ptr) + splitPageDeltaSize)
		d.hiItm = d.itm
		memcopy(d.itm, itm, int(l))
		pg.addDeltaAlloc(ptr)
		return d
	}

	d := new(splitPageDelta)
	d.itm = itm
	d.hiItm = itm
	return d
}

func (pg *page) allocMergePageDelta(itm unsafe.Pointer) *mergePageDelta {
	l := pg.itemSize(itm)
	size := mergePageDeltaSize + l
	pg.memUsed += int(size)

	if pg.useMemMgmt {
		ptr := pg.allocMM(size)
		d := (*mergePageDelta)(ptr)
		d.hiItm = unsafe.Pointer(uintptr(ptr) + mergePageDeltaSize)
		memcopy(d.hiItm, itm, int(l))
		pg.addDeltaAlloc(ptr)
		return d
	}

	d := new(mergePageDelta)
	d.itm = itm
	d.hiItm = itm
	return d
}

func (pg *page) allocFlushPageDelta() *flushPageDelta {
	pg.memUsed += int(flushPageDeltaSize)
	if pg.useMemMgmt {
		ptr := pg.allocMM(flushPageDeltaSize)
		pg.addDeltaAlloc(ptr)
		return (*flushPageDelta)(ptr)
	}

	return new(flushPageDelta)
}

func (pg *page) allocRemovePageDelta() *removePageDelta {
	pg.memUsed += int(removePageDeltaSize)
	if pg.useMemMgmt {
		ptr := pg.allocMM(removePageDeltaSize)
		pg.addDeltaAlloc(ptr)
		return (*removePageDelta)(ptr)
	}

	return new(removePageDelta)
}

func (pg *page) allocRollbackPageDelta() *rollbackDelta {
	pg.memUsed += int(rollbackDeltaSize)
	if pg.useMemMgmt {
		ptr := pg.allocMM(rollbackDeltaSize)
		pg.addDeltaAlloc(ptr)
		return (*rollbackDelta)(ptr)
	}

	return new(rollbackDelta)
}