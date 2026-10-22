// Package stream implements bounded, incremental media search. A session stores
// only keyed digests, media and encrypted provider cursors, never request text.
package stream

import (
	"container/list"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"searchy/internal/search"
)

const (
	maxPages        = 10
	maxSources      = 32
	maxCursorBytes  = 4096
	maxSessionCount = 1024
)

// ErrUnavailable distinguishes failed upstream work from a successful empty
// search. Failed pages are not frozen, so their cursor can be retried.
var ErrUnavailable = search.ErrUnavailable

type Options struct {
	Sources       []search.Source
	Rank          func(string, []search.MediaResult, int) []search.MediaResult
	Fallback      search.Backend
	FirstPageWait time.Duration
	Timeout       time.Duration
	NativeTimeout time.Duration
	CacheTTL      time.Duration
	CacheSize     int // total retained results, including frozen pages
	MaxResults    int // complete pool limit; inline pages are limited to 1..50
	MaxConcurrent int // both upstream calls and query coordinators are bounded
	Logger        *slog.Logger
}

type Engine struct {
	mu          sync.Mutex
	options     Options
	sources     []source
	secret      [32]byte
	fingerprint [32]byte
	aead        cipher.AEAD
	byKey       map[[32]byte]*session
	byID        map[[16]byte]*session
	lru         list.List
	used        int
	active      int
	jobs        int
	fallbacks   int
	owners      map[[32]byte]map[*subscription]struct{}
	leases      map[[32]byte]*lease
	leaseLRU    list.List
	subscribers int
	changed     chan struct{}
	ctx         context.Context
	cancel      context.CancelFunc
	closed      bool
	wg          sync.WaitGroup
}

type source struct {
	backend  search.Source
	name     string
	category search.Category
	active   int
}

type continuation struct {
	cursor  []byte
	seen    [][32]byte
	calls   int
	done    bool
	failed  bool
	pending bool
}

type frozenPage struct {
	results  []search.MediaResult
	more     bool
	degraded bool
}

type session struct {
	id        [16]byte
	key       [32]byte
	created   time.Time
	element   *list.Element
	states    []continuation
	fallback  continuation
	pool      []search.MediaResult
	pages     []frozenPage
	delivered map[[32]byte]struct{}
	count     int
	budget    int
	refs      int
	retry     bool
	degraded  bool
	limited   bool
	evicted   bool
	job       *job
	leases    map[[32]byte]struct{}
}

type job struct {
	ctx          context.Context
	cancel       context.CancelFunc
	native       context.Context
	cancelNative context.CancelFunc
	start        time.Time
	target       int
}

type subscription struct {
	s      *session
	key    [32]byte
	cancel context.CancelFunc
}

type lease struct {
	owner   [32]byte
	s       *session
	element *list.Element
}

func New(o Options) *Engine {
	if o.FirstPageWait <= 0 {
		o.FirstPageWait = 800 * time.Millisecond
	}
	if o.Timeout <= 0 {
		o.Timeout = 8 * time.Second
	}
	if o.NativeTimeout <= 0 {
		o.NativeTimeout = 3500 * time.Millisecond
	}
	o.NativeTimeout = min(o.NativeTimeout, o.Timeout)
	if o.CacheTTL <= 0 {
		o.CacheTTL = 5 * time.Minute
	}
	if o.CacheSize <= 0 {
		o.CacheSize = 4096
	}
	if o.MaxResults <= 0 {
		o.MaxResults = 50
	}
	o.MaxResults = min(o.MaxResults, 50*maxPages, o.CacheSize)
	if o.MaxConcurrent <= 0 {
		o.MaxConcurrent = 8
	}
	o.MaxConcurrent = min(o.MaxConcurrent, 128)
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{
		options: o, byKey: make(map[[32]byte]*session), byID: make(map[[16]byte]*session),
		owners: make(map[[32]byte]map[*subscription]struct{}), leases: make(map[[32]byte]*lease), changed: make(chan struct{}),
		ctx: ctx, cancel: cancel,
	}
	if _, err := rand.Read(e.secret[:]); err != nil {
		panic("stream: secure random unavailable")
	}
	block, err := aes.NewCipher(e.secret[:])
	if err != nil {
		panic("stream: invalid cipher key")
	}
	e.aead, err = cipher.NewGCM(block)
	if err != nil {
		panic("stream: cipher unavailable")
	}
	h := sha256.New()
	writePart(h, "stream-v1")
	registered := make(map[string]bool)
	for _, s := range o.Sources {
		if s == nil || len(e.sources) == maxSources {
			continue
		}
		cat := s.Category()
		if cat != search.CatImage && cat != search.CatVideo {
			continue
		}
		identity := s.Name() + "\x00" + string(cat)
		if registered[identity] {
			continue
		}
		registered[identity] = true
		e.sources = append(e.sources, source{backend: s, name: boundedString(s.Name(), 128), category: cat})
		writePart(h, s.Name())
		writePart(h, string(cat))
	}
	writePart(h, fmt.Sprintf("%T/%T/%d/%d", o.Fallback, o.Rank, o.MaxResults, o.CacheSize))
	copy(e.fingerprint[:], h.Sum(nil))
	// Do not retain a second copy of the caller's source slice.
	e.options.Sources = nil
	e.wg.Add(1)
	go e.reap()
	return e
}

// Close cancels all coordinators and upstream contexts, including idle leases.
// As with context.Context itself, an upstream must honor cancellation to stop
// its own call; such calls keep their concurrency slot until they return.
func (e *Engine) Close() {
	e.mu.Lock()
	if !e.closed {
		e.closed = true
		e.cancel()
		for _, s := range e.byID {
			e.removeLocked(s)
		}
		for _, subscriptions := range e.owners {
			for sub := range subscriptions {
				sub.cancel()
			}
		}
		e.notifyLocked()
	}
	e.mu.Unlock()
	e.wg.Wait()
}

func (e *Engine) SearchPage(ctx context.Context, r search.PageRequest) (search.Page, error) {
	if err := ctx.Err(); err != nil {
		return search.Page{}, err
	}
	r.Query = strings.TrimSpace(r.Query)
	if len(r.Query) > 4096 {
		return search.Page{}, errors.New("search query too long")
	}
	if r.Query == "" {
		return search.Page{}, nil
	}
	r.Categories = categories(r.Categories)
	r.Language = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(r.Language), "_", "-"))
	if r.Language == "" {
		r.Language = "en"
	}
	if r.Limit <= 0 {
		r.Limit = 24
		if r.WaitAll {
			r.Limit = min(50, e.options.MaxResults)
		}
	}
	r.Limit = min(r.Limit, 50)
	key := e.requestKey(r)
	owner := e.ownerKey(r.UserID, r.Scope)
	var id [16]byte
	index := 0
	if r.Offset != "" {
		var err error
		id, index, err = e.decodeCursor(r.Offset, key, owner)
		if err != nil {
			return search.Page{}, err
		}
	}
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return search.Page{}, context.Canceled
	}
	e.expireLocked(time.Now())
	var s *session
	if r.Offset != "" {
		s = e.byID[id]
		if s == nil {
			e.mu.Unlock()
			return search.Page{}, search.ErrExpiredCursor
		}
		if s.key != key || index > len(s.pages) {
			e.mu.Unlock()
			return search.Page{}, search.ErrInvalidCursor
		}
	} else {
		s = e.byKey[key]
		// Partially failed searches keep their issued cursors, but a fresh search
		// retries upstream work instead of caching the failure for five minutes.
		if s != nil && s.retry && len(s.pages) > 0 {
			s = nil
		}
		if s == nil {
			if !e.makeSessionRoomLocked() {
				e.mu.Unlock()
				return search.Page{}, search.ErrBusy
			}
			s = e.newSessionLocked(key, r)
		}
	}
	e.lru.MoveToFront(s.element)
	for old := range e.owners[owner] {
		if old.key != key {
			old.cancel()
		}
	}
	if old := e.leases[owner]; old != nil && old.s != s {
		e.dropLeaseLocked(old)
	}
	if index < len(s.pages) {
		e.leaseLocked(owner, s)
		page := e.pageLocked(s, index, key, owner)
		e.mu.Unlock()
		return page, nil
	}
	if e.subscribers >= e.options.MaxConcurrent*32 {
		e.mu.Unlock()
		return search.Page{}, search.ErrBusy
	}
	sub := &subscription{s: s, key: key, cancel: cancel}
	if e.owners[owner] == nil {
		e.owners[owner] = make(map[*subscription]struct{})
	}
	e.owners[owner][sub] = struct{}{}
	e.subscribers++
	s.refs++
	if s.job == nil {
		if !e.startLocked(s, r) {
			e.unsubscribeLocked(owner, sub, false)
			e.mu.Unlock()
			return search.Page{}, search.ErrBusy
		}
	}
	e.leaseLocked(owner, s)
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.unsubscribeLocked(owner, sub, wctx.Err() != nil)
		e.mu.Unlock()
	}()
	for {
		e.mu.Lock()
		if err := wctx.Err(); err != nil {
			e.mu.Unlock()
			return search.Page{}, err
		}
		if e.closed {
			e.mu.Unlock()
			return search.Page{}, context.Canceled
		}
		if s.evicted || time.Since(s.created) >= e.options.CacheTTL {
			e.removeLocked(s)
			e.mu.Unlock()
			return search.Page{}, search.ErrExpiredCursor
		}
		if index < len(s.pages) {
			page := e.pageLocked(s, index, key, owner)
			e.mu.Unlock()
			return page, nil
		}
		complete := s.job == nil
		ready := complete
		if !r.WaitAll && s.job != nil && len(s.pool) >= min(3, r.Limit) && time.Since(s.job.start) >= e.options.FirstPageWait {
			ready = true
		}
		if ready {
			if len(s.pool) == 0 && s.retry {
				e.unlinkKeyLocked(s)
				e.mu.Unlock()
				return search.Page{Degraded: true}, ErrUnavailable
			}
			n := min(r.Limit, len(s.pool))
			if r.WaitAll {
				n = min(s.budget, len(s.pool))
			}
			// Telegram needs a short cache lifetime for a partial snapshot even
			// if its outstanding sources eventually complete successfully.
			f := frozenPage{results: cloneResults(s.pool[:n]), degraded: s.degraded || s.job != nil}
			// A small frozen slice must not pin an entire old lookahead array.
			s.pool = cloneResults(s.pool[n:])
			for _, result := range f.results {
				for _, k := range resultKeys(result) {
					s.delivered[k] = struct{}{}
				}
			}
			s.count += n
			f.more = !r.WaitAll && len(s.pages)+1 < maxPages && s.count < s.budget && e.moreLocked(s)
			s.pages = append(s.pages, f)
			if len(f.results) == 0 || s.degraded {
				s.retry = true
				e.unlinkKeyLocked(s)
			}
			e.notifyLocked()
			page := e.pageLocked(s, index, key, owner)
			e.mu.Unlock()
			return page, nil
		}
		changed := e.changed
		e.mu.Unlock()
		select {
		case <-wctx.Done():
		case <-changed:
		}
	}
}

func (e *Engine) newSessionLocked(key [32]byte, r search.PageRequest) *session {
	s := &session{key: key, created: time.Now(), states: make([]continuation, len(e.sources)), delivered: make(map[[32]byte]struct{}), leases: make(map[[32]byte]struct{}), budget: e.options.MaxResults}
	if _, err := rand.Read(s.id[:]); err != nil {
		panic("stream: secure random unavailable")
	}
	if !r.WaitAll {
		s.budget = min(s.budget, r.Limit*maxPages)
	} else {
		s.budget = min(s.budget, r.Limit)
	}
	for i, src := range e.sources {
		s.states[i].done = !containsCategory(r.Categories, src.category)
	}
	s.element = e.lru.PushFront(s)
	e.byKey[key] = s
	e.byID[s.id] = s
	return s
}

func (e *Engine) startLocked(s *session, r search.PageRequest) bool {
	if e.jobs >= e.options.MaxConcurrent {
		return false
	}
	ctx, cancel := context.WithTimeout(e.ctx, e.options.Timeout)
	native, cancelNative := context.WithTimeout(ctx, e.options.NativeTimeout)
	j := &job{ctx: ctx, cancel: cancel, native: native, cancelNative: cancelNative, start: time.Now(), target: min(2*r.Limit, s.budget-s.count)}
	if r.WaitAll {
		j.target = s.budget - s.count
	}
	for i := range s.states {
		s.states[i].failed = false
		s.states[i].pending = false
	}
	s.fallback.failed = false
	s.fallback.pending = false
	s.job = j
	e.jobs++
	e.wg.Add(1)
	go e.run(s, j, r.Query, r.Language, r.Categories, r.Limit, r.WaitAll)
	return true
}

func (e *Engine) unsubscribeLocked(owner [32]byte, sub *subscription, aborted bool) {
	delete(e.owners[owner], sub)
	if len(e.owners[owner]) == 0 {
		delete(e.owners, owner)
	}
	e.subscribers--
	sub.s.refs--
	if aborted {
		interested := false
		for other := range e.owners[owner] {
			interested = interested || other.s == sub.s
		}
		if l := e.leases[owner]; !interested && l != nil && l.s == sub.s {
			e.dropLeaseLocked(l)
		}
	}
	e.cancelUnusedLocked(sub.s)
	e.notifyLocked()
}

func (e *Engine) cancelUnusedLocked(s *session) {
	if s.refs == 0 && len(s.leases) == 0 && s.job != nil {
		s.job.cancel()
		s.retry = true
		e.unlinkKeyLocked(s)
	}
}

func (e *Engine) leaseLocked(owner [32]byte, s *session) {
	if s.job == nil {
		return
	}
	if old := e.leases[owner]; old != nil {
		if old.s == s {
			e.leaseLRU.MoveToFront(old.element)
			return
		}
		e.dropLeaseLocked(old)
	}
	limit := min(4096, max(32, e.options.CacheSize))
	for len(e.leases) >= limit {
		e.dropLeaseLocked(e.leaseLRU.Back().Value.(*lease))
	}
	l := &lease{owner: owner, s: s}
	l.element = e.leaseLRU.PushFront(l)
	e.leases[owner] = l
	s.leases[owner] = struct{}{}
}

func (e *Engine) dropLeaseLocked(l *lease) {
	delete(e.leases, l.owner)
	delete(l.s.leases, l.owner)
	e.leaseLRU.Remove(l.element)
	e.cancelUnusedLocked(l.s)
}

func (e *Engine) clearLeasesLocked(s *session) {
	for owner := range s.leases {
		l := e.leases[owner]
		delete(e.leases, owner)
		delete(s.leases, owner)
		e.leaseLRU.Remove(l.element)
	}
}

func (e *Engine) moreLocked(s *session) bool {
	if len(s.pool) > 0 {
		return true
	}
	if s.limited {
		return false
	}
	for _, state := range s.states {
		if !state.done {
			return true
		}
	}
	return s.fallback.calls > 0 && !s.fallback.done || s.fallback.pending
}

func (e *Engine) pageLocked(s *session, index int, key, owner [32]byte) search.Page {
	f := s.pages[index]
	p := search.Page{Results: cloneResults(f.results), Degraded: f.degraded}
	if f.more {
		p.NextOffset = e.encodeCursor(s.id, index+1, key, owner)
	}
	return p
}

func (e *Engine) notifyLocked() {
	close(e.changed)
	e.changed = make(chan struct{})
}

func (e *Engine) unlinkKeyLocked(s *session) {
	if e.byKey[s.key] == s {
		delete(e.byKey, s.key)
	}
}

func (e *Engine) removeLocked(s *session) {
	if s.evicted {
		return
	}
	s.evicted = true
	e.clearLeasesLocked(s)
	if s.job != nil {
		s.job.cancel()
	}
	e.unlinkKeyLocked(s)
	delete(e.byID, s.id)
	e.lru.Remove(s.element)
	e.used -= s.count + len(s.pool)
	e.notifyLocked()
}

func (e *Engine) expireLocked(now time.Time) {
	for _, s := range e.byID {
		if now.Sub(s.created) >= e.options.CacheTTL {
			e.removeLocked(s)
		}
	}
}

func (e *Engine) victimLocked(keep *session) *session {
	for el := e.lru.Back(); el != nil; el = el.Prev() {
		s := el.Value.(*session)
		if s != keep && s.refs == 0 {
			return s
		}
	}
	return nil
}

func (e *Engine) makeSessionRoomLocked() bool {
	limit := min(maxSessionCount, max(1, e.options.CacheSize))
	for len(e.byID) >= limit {
		v := e.victimLocked(nil)
		if v == nil {
			return false
		}
		e.removeLocked(v)
	}
	return true
}

func (e *Engine) reap() {
	defer e.wg.Done()
	ticker := time.NewTicker(max(time.Millisecond, min(time.Second, e.options.CacheTTL/2)))
	defer ticker.Stop()
	for {
		select {
		case <-e.ctx.Done():
			return
		case now := <-ticker.C:
			e.mu.Lock()
			e.expireLocked(now)
			e.mu.Unlock()
		}
	}
}

func categories(in []search.Category) []search.Category {
	out := make([]search.Category, 0, 2)
	for _, cat := range []search.Category{search.CatImage, search.CatVideo} {
		if len(in) == 0 || containsCategory(in, cat) {
			out = append(out, cat)
		}
	}
	return out
}

func containsCategory(cats []search.Category, cat search.Category) bool {
	for _, c := range cats {
		if c == cat {
			return true
		}
	}
	return false
}

func writePart(h hash.Hash, text string) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(text)))
	h.Write(n[:])
	h.Write([]byte(text))
}

func (e *Engine) requestKey(r search.PageRequest) [32]byte {
	h := hmac.New(sha256.New, e.secret[:])
	h.Write(e.fingerprint[:])
	writePart(h, r.Query)
	for _, cat := range r.Categories {
		writePart(h, string(cat))
	}
	writePart(h, r.Language)
	writePart(h, fmt.Sprintf("%d/%t", r.Limit, r.WaitAll))
	var key [32]byte
	copy(key[:], h.Sum(nil))
	return key
}

func (e *Engine) ownerKey(userID int64, scope string) [32]byte {
	h := hmac.New(sha256.New, e.secret[:])
	writePart(h, "owner")
	writePart(h, fmt.Sprint(userID))
	writePart(h, scope)
	var key [32]byte
	copy(key[:], h.Sum(nil))
	return key
}

func (e *Engine) cursorMAC(payload []byte, key, owner [32]byte) []byte {
	h := hmac.New(sha256.New, e.secret[:])
	h.Write(payload)
	h.Write(key[:])
	h.Write(owner[:])
	return h.Sum(nil)[:16]
}

func (e *Engine) cursorDigest(cursor string) [32]byte {
	h := hmac.New(sha256.New, e.secret[:])
	writePart(h, "source-cursor")
	writePart(h, cursor)
	var key [32]byte
	copy(key[:], h.Sum(nil))
	return key
}

func (e *Engine) encodeCursor(id [16]byte, page int, key, owner [32]byte) string {
	var b [33]byte
	copy(b[:16], id[:])
	b[16] = byte(page)
	copy(b[17:], e.cursorMAC(b[:17], key, owner))
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func (e *Engine) decodeCursor(raw string, key, owner [32]byte) ([16]byte, int, error) {
	var id [16]byte
	if len(raw) != 44 {
		return id, 0, search.ErrInvalidCursor
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(b) != 33 || b[16] < 1 || b[16] >= maxPages || !hmac.Equal(b[17:], e.cursorMAC(b[:17], key, owner)) {
		return id, 0, search.ErrInvalidCursor
	}
	copy(id[:], b[:16])
	return id, int(b[16]), nil
}

func (e *Engine) sealCursor(cursor string) ([]byte, error) {
	if len(cursor) > maxCursorBytes {
		return nil, errors.New("source cursor too long")
	}
	if cursor == "" {
		return nil, nil
	}
	nonce := make([]byte, e.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, errors.New("cursor encryption unavailable")
	}
	return e.aead.Seal(nonce, nonce, []byte(cursor), nil), nil
}

func (e *Engine) openCursor(cursor []byte) string {
	if len(cursor) == 0 {
		return ""
	}
	n := e.aead.NonceSize()
	plain, err := e.aead.Open(nil, cursor[:n], cursor[n:], nil)
	if err != nil {
		return ""
	}
	return string(plain)
}

func cloneResults(in []search.MediaResult) []search.MediaResult {
	if in == nil {
		return nil
	}
	out := append([]search.MediaResult(nil), in...)
	for i := range out {
		out[i].Engines = append([]string(nil), out[i].Engines...)
		out[i].Positions = append([]int(nil), out[i].Positions...)
	}
	return out
}

func defaultRank(_ string, in []search.MediaResult, limit int) []search.MediaResult {
	sort.SliceStable(in, func(i, j int) bool {
		if in[i].RankScore != in[j].RankScore {
			return in[i].RankScore > in[j].RankScore
		}
		if in[i].Score != in[j].Score {
			return in[i].Score > in[j].Score
		}
		return in[i].ID < in[j].ID
	})
	return in[:min(limit, len(in))]
}
