package app

import (
	"sync"

	"btcx/internal/model"
)

// cacheKey identifies a cached message. model.Ref is unique per server, so
// two accounts can mint identical refs: the account must be part of the key
// or one account would be served the other's mail.
type cacheKey struct {
	account string
	ref     model.Ref
}

// cachedMessage is a parsed message and the raw bytes it was parsed from. The
// raw form is kept so a forward can enclose the original verbatim instead of
// a re-serialization that mints a new Message-ID and Date.
type cachedMessage struct {
	msg *model.Message
	raw []byte
}

// size is the memory the entry is charged against the cache budget.
func (e *cachedMessage) size() int {
	n := len(e.raw) + len(e.msg.Plain) + len(e.msg.HTML)
	for _, a := range e.msg.Attachments {
		n += len(a.Data)
	}
	return n
}

// messageCache owns the parsed-message cache: account+ref keyed entries with
// byte-budgeted oldest-first eviction. Its own lock; safe to use without
// Controller.mu.
type messageCache struct {
	mu      sync.Mutex
	entries map[cacheKey]*cachedMessage
	order   []cacheKey
	size    int
	budget  int
}

// newMessageCache returns an empty cache bounded by budget bytes. Tests lower
// the budget so eviction is exercised without allocating 256 MiB.
func newMessageCache(budget int) *messageCache {
	return &messageCache{entries: make(map[cacheKey]*cachedMessage), budget: budget}
}

// Get returns the cached entry for key.
func (m *messageCache) Get(key cacheKey) (*cachedMessage, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.entries[key]
	return entry, ok
}

// Put stores msg and its raw bytes, evicting the oldest entries until the
// entry cap and the byte budget are both satisfied.
func (m *messageCache) Put(key cacheKey, msg *model.Message, raw []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry := &cachedMessage{msg: msg, raw: raw}
	if old, ok := m.entries[key]; ok {
		m.size += entry.size() - old.size()
		m.entries[key] = entry
	} else {
		m.entries[key] = entry
		m.order = append(m.order, key)
		m.size += entry.size()
	}
	for len(m.order) > 0 && (len(m.order) > maxCachedMessages || m.size > m.budget) {
		m.drop(m.order[0])
	}
}

// Drop forgets one cached message.
func (m *messageCache) Drop(key cacheKey) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.drop(key)
}

// drop forgets one cached message and its recency slot. The caller holds m.mu.
func (m *messageCache) drop(key cacheKey) {
	entry, ok := m.entries[key]
	if !ok {
		return
	}
	delete(m.entries, key)
	m.size -= entry.size()
	for i, k := range m.order {
		if k == key {
			m.order = append(m.order[:i], m.order[i+1:]...)
			return
		}
	}
}

// Clear drops everything. Reload calls it because the next generation may be a
// different server minting the same UIDVALIDITY and refs.
func (m *messageCache) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = make(map[cacheKey]*cachedMessage)
	m.order = nil
	m.size = 0
}

// Stats reports the number of entries and the bytes they are charged.
func (m *messageCache) Stats() (entries int, bytes int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.order), m.size
}
