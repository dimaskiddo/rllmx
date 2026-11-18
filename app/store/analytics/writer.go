package analytics

import (
	"context"
	"rllmx/app/store/dialect"
	"sync"
	"sync/atomic"
	"time"

	contractsdb "github.com/goravel/framework/contracts/database/db"
)

const (
	defaultQueue      = 10000
	defaultFlushRows  = 500
	defaultFlushEvery = time.Second
	logEvery          = 30 * time.Second
	flushTimeout      = 30 * time.Second
)

// WriterOptions tunes the writer; zero values give the defaults.
type WriterOptions struct {
	QueueSize  int
	FlushRows  int
	FlushEvery time.Duration

	beforeFlush func()
}

// Writer batches records into the store. Enqueue never blocks: when the queue
// is full the record is dropped and counted, and a request never waits for it.
type Writer struct {
	s    *Store
	ch   chan row
	opts WriterOptions

	mu     sync.RWMutex
	closed bool
	done   chan struct{}

	ctx    context.Context
	cancel context.CancelFunc

	// pending is the per-key totals of rows queued but not yet in the database,
	// so a budget check sees this second's spend. A row leaves it when its batch
	// is flushed, written or dropped.
	pendingMu sync.Mutex
	pending   map[string]Totals

	dropped atomic.Uint64
	failed  atomic.Uint64
	flushed atomic.Uint64
	lastLog atomic.Int64
}

// NewWriter starts the flush goroutine, so a caller that never closes it leaves one running.
func (s *Store) NewWriter(o WriterOptions) *Writer {
	if o.QueueSize <= 0 {
		o.QueueSize = defaultQueue
	}
	if o.FlushRows <= 0 {
		o.FlushRows = defaultFlushRows
	}
	if o.FlushEvery <= 0 {
		o.FlushEvery = defaultFlushEvery
	}
	w := &Writer{s: s, ch: make(chan row, o.QueueSize), opts: o, done: make(chan struct{}), pending: map[string]Totals{}}
	w.ctx, w.cancel = context.WithCancel(context.Background())
	go w.run()
	return w
}

// Enqueue queues a record and reports whether it was accepted.
func (w *Writer) Enqueue(r Record) bool { return w.enqueue(row{Record: sanitize(r)}) }

// Dropped is the number of records a full queue refused.
func (w *Writer) Dropped() uint64 { return w.dropped.Load() }

// Failed is the number of records a failed flush lost.
func (w *Writer) Failed() uint64 { return w.failed.Load() }

// Flushed counts the batches a reader has not seen yet: it moves once per
// committed batch, so a feed can tell whether new rows landed.
func (w *Writer) Flushed() uint64 { return w.flushed.Load() }

func (w *Writer) enqueue(r row) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.closed {
		return false
	}
	// Counted before the send: the flusher may take the row at once, and its
	// removal must find the count already there.
	w.addPending(r)
	select {
	case w.ch <- r:
		return true
	default:
		w.subPending([]row{r})
		w.dropped.Add(1)
		return false
	}
}

// Pending is the totals of one key's rows that are queued but not yet flushed.
func (w *Writer) Pending(keyID string) Totals {
	w.pendingMu.Lock()
	defer w.pendingMu.Unlock()
	return w.pending[keyID]
}

// addPending counts one queued row against its key.
func (w *Writer) addPending(r row) {
	if r.APIKeyID == "" {
		return
	}
	w.pendingMu.Lock()
	defer w.pendingMu.Unlock()
	t := w.pending[r.APIKeyID]
	t.Requests++
	// Tokens and cost count every row, as the rollup sums them; only the request
	// itself is taken back out for a failure, matching KeyTotals.
	t.Tokens += r.PromptTokens + r.CompletionTokens
	t.Cost += r.Cost
	w.pending[r.APIKeyID] = t
}

// subPending takes a flushed batch back off the delta, whether it was written or
// dropped: either way it is no longer waiting.
func (w *Writer) subPending(batch []row) {
	w.pendingMu.Lock()
	defer w.pendingMu.Unlock()
	for _, r := range batch {
		if r.APIKeyID == "" {
			continue
		}
		t := w.pending[r.APIKeyID]
		t.Requests--
		t.Tokens -= r.PromptTokens + r.CompletionTokens
		t.Cost -= r.Cost
		if t.Requests <= 0 {
			delete(w.pending, r.APIKeyID)
			continue
		}
		w.pending[r.APIKeyID] = t
	}
}

// Close stops accepting records, writes what is queued and waits until ctx
// ends. When ctx ends first the pending write is cancelled and Close still
// waits for the flush goroutine, so nothing writes after Close returns.
func (w *Writer) Close(ctx context.Context) error {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.ch)
	}
	w.mu.Unlock()
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		w.cancel()
		<-w.done
		return ctx.Err()
	}
}

func (w *Writer) run() {
	defer close(w.done)
	defer w.cancel()
	ticker := time.NewTicker(w.opts.FlushEvery)
	defer ticker.Stop()
	batch := make([]row, 0, w.opts.FlushRows)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		w.flush(batch)
		batch = batch[:0]
	}
	for {
		select {
		case r, ok := <-w.ch:
			if !ok {
				flush()
				return
			}
			batch = append(batch, r)
			if len(batch) >= w.opts.FlushRows {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

func (w *Writer) flush(batch []row) {
	defer w.subPending(batch)
	if w.opts.beforeFlush != nil {
		w.opts.beforeFlush()
	}
	ctx, cancel := context.WithTimeout(w.ctx, flushTimeout)
	defer cancel()
	if err := w.s.insert(ctx, batch); err != nil {
		w.failed.Add(uint64(len(batch)))
		now := time.Now().UnixNano()
		if last := w.lastLog.Load(); now-last > int64(logEvery) && w.lastLog.CompareAndSwap(last, now) {
			w.s.log.Errorf("analytics: batch of %d dropped: %v", len(batch), err)
		}
		return
	}
	w.flushed.Add(1)
}

const insertSQL = `INSERT INTO "requests"("ts","requestId","apiKeyId","apiKeyName","connectionId","provider","model","endpoint","sourceFormat","status","errorClass","stream","cached","estimated","promptTokens","completionTokens","cachedTokens","cacheWriteTokens","reasoningTokens","cost","latencyMs","ttfbMs","comboName","attempt","importId") VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`

func rowArgs(r row) []any {
	var imp any
	if r.importID != nil {
		imp = *r.importID
	}
	return []any{r.Time.UnixMilli(), r.RequestID, r.APIKeyID, r.APIKeyName, r.ConnectionID,
		r.Provider, r.Model, r.Endpoint, r.SourceFormat, int64(r.Status), r.ErrorClass,
		dialect.BoolInt(r.Stream), dialect.BoolInt(r.Cached), dialect.BoolInt(r.Estimated),
		r.PromptTokens, r.CompletionTokens, r.CachedTokens, r.CacheWriteTokens, r.ReasoningTokens, r.Cost,
		r.LatencyMs, r.TTFBMs, r.ComboName, int64(r.Attempt), imp}
}

// insert writes the rows, their rollups and the lifetime counter in one
// transaction, so a chart never sees a row its rollup does not count.
func (s *Store) insert(ctx context.Context, rows []row) error {
	return s.Tx(ctx, func(tx contractsdb.Tx) error {
		stmt := s.q(insertSQL)
		for _, r := range rows {
			if err := tx.Statement(stmt, rowArgs(r)...); err != nil {
				return err
			}
		}
		if err := s.addRollups(tx, rows); err != nil {
			return err
		}
		return s.bumpLifetime(tx, int64(len(rows)))
	})
}
