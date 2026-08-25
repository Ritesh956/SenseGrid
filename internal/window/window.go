package window

import (
	"sort"
	"time"
)

// sample is one reading held in a Window's buffer, enough to evict it
// (DeviceTime) and to subtract it back out of Stats (Value) and to dedupe
// JetStream redeliveries (Seq).
type sample struct {
	deviceTime time.Time
	value      float64
	seq        uint64
}

// Window is a sliding window bound by both sample count and sample age —
// whichever evicts first. Samples are kept sorted by DeviceTime rather than
// arrival order, so a reading that arrives slightly out of order (normal
// over MQTT/JetStream at-least-once delivery) still lands in the correct
// position for eviction purposes instead of corrupting the window's notion
// of "oldest sample" every insert re-derives Stats/EWMA incrementally
// (Update on insert, Remove on evict) rather than recomputing from the
// buffer, which is what keeps a window update O(window size) only in the
// rare worst case (an out-of-order insert near the head) and O(1)
// amortized in the common case (append at the tail, evict at the head).
type Window struct {
	MaxCount int
	MaxAge   time.Duration

	buf      []sample
	stats    Stats
	ewma     *EWMA
	prevEWMA float64

	seenSeq  []uint64 // small ring of recently-processed seqs, redelivery dedupe
	seenHead int
}

const dedupeRingSize = 64

func New(maxCount int, maxAge time.Duration, ewmaAlpha float64) *Window {
	return &Window{
		MaxCount: maxCount,
		MaxAge:   maxAge,
		ewma:     NewEWMA(ewmaAlpha),
		seenSeq:  make([]uint64, 0, dedupeRingSize),
	}
}

// alreadySeen reports whether seq was already folded into this window
// recently, so a JetStream redelivery (nak, or a restart replaying the
// last few unacked messages) doesn't double-count a sample. This is a
// bounded lookback, not a general gap/duplicate detector — seq's role for
// that purpose belongs to Phase 7's chaos tests, not this window.
func (w *Window) alreadySeen(seq uint64) bool {
	for _, s := range w.seenSeq {
		if s == seq {
			return true
		}
	}
	return false
}

func (w *Window) markSeen(seq uint64) {
	if len(w.seenSeq) < dedupeRingSize {
		w.seenSeq = append(w.seenSeq, seq)
		return
	}
	w.seenSeq[w.seenHead] = seq
	w.seenHead = (w.seenHead + 1) % dedupeRingSize
}

// Insert folds a new reading into the window, evicts anything now outside
// the count/age bounds, and returns false without modifying state if seq
// was already processed. now is the reference time for age-based eviction
// (the latest device_time seen, not wall-clock time, so a burst of
// slightly-delayed messages doesn't evict itself).
func (w *Window) Insert(deviceTime time.Time, value float64, seq uint64) bool {
	if w.alreadySeen(seq) {
		return false
	}
	w.markSeen(seq)

	s := sample{deviceTime: deviceTime, value: value, seq: seq}
	idx := sort.Search(len(w.buf), func(i int) bool { return w.buf[i].deviceTime.After(deviceTime) })
	w.buf = append(w.buf, sample{})
	copy(w.buf[idx+1:], w.buf[idx:])
	w.buf[idx] = s
	w.stats.Update(value)
	w.prevEWMA = w.ewma.Value()
	w.ewma.Update(value)

	w.evict()
	return true
}

// evict drops samples that fall outside MaxCount or MaxAge, relative to
// the newest sample currently in the window.
func (w *Window) evict() {
	if len(w.buf) == 0 {
		return
	}
	newest := w.buf[len(w.buf)-1].deviceTime

	i := 0
	for i < len(w.buf) {
		tooOld := w.MaxAge > 0 && newest.Sub(w.buf[i].deviceTime) > w.MaxAge
		tooMany := w.MaxCount > 0 && len(w.buf)-i > w.MaxCount
		if !tooOld && !tooMany {
			break
		}
		w.stats.Remove(w.buf[i].value)
		i++
	}
	if i > 0 {
		w.buf = w.buf[i:]
	}
}

func (w *Window) Count() int64      { return w.stats.Count() }
func (w *Window) Mean() float64     { return w.stats.Mean() }
func (w *Window) StdDev() float64   { return w.stats.StdDev() }
func (w *Window) EWMA() float64     { return w.ewma.Value() }
func (w *Window) PrevEWMA() float64 { return w.prevEWMA }
func (w *Window) NewestTime() time.Time {
	if len(w.buf) == 0 {
		return time.Time{}
	}
	return w.buf[len(w.buf)-1].deviceTime
}
func (w *Window) OldestTime() time.Time {
	if len(w.buf) == 0 {
		return time.Time{}
	}
	return w.buf[0].deviceTime
}
