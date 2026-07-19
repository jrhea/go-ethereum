// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package state

import (
	"encoding/binary"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

const (
	// fixedAccountCacheBits and fixedStorageCacheBits size the two entry
	// tables, 256k accounts and 2m storage slots. Memory is bounded by
	// construction, colliding entries displace each other.
	fixedAccountCacheBits = 18
	fixedStorageCacheBits = 21
)

// accountCacheEntry is an immutable cached account. A nil account is a valid
// value meaning nonexistent.
type accountCacheEntry struct {
	epoch   uint64
	addr    common.Address
	account *types.StateAccount
}

// storageCacheEntry is an immutable cached storage slot.
type storageCacheEntry struct {
	epoch uint64
	addr  common.Address
	slot  common.Hash
	value common.Hash
}

// fixedStateCache is a lock free state entry cache in front of a state
// reader. Entries are immutable and published through atomic pointers into
// two fixed size, two way skew associative tables, so hits cost two atomic
// loads and a key compare, with no locks and no allocation. Entries carry an
// epoch, bumping it invalidates the whole table in constant time without
// releasing the memory.
//
// The cache trades completeness for speed, colliding entries displace each
// other and reappear as plain misses. It is the retained content of the
// cross block ExecutionCache.
type fixedStateCache struct {
	StateReader

	accountBits  int
	storageBits  int
	accountEpoch atomic.Uint64
	storageEpoch atomic.Uint64
	accounts     []atomic.Pointer[accountCacheEntry]
	storages     []atomic.Pointer[storageCacheEntry]
}

// newFixedStateCache constructs the cache with the given table sizes in bits.
func newFixedStateCache(underlying StateReader, accountBits, storageBits int) *fixedStateCache {
	return &fixedStateCache{
		StateReader: underlying,
		accountBits: accountBits,
		storageBits: storageBits,
		accounts:    make([]atomic.Pointer[accountCacheEntry], 1<<accountBits),
		storages:    make([]atomic.Pointer[storageCacheEntry], 1<<storageBits),
	}
}

// reset invalidates all cached entries in constant time.
func (c *fixedStateCache) reset() {
	c.accountEpoch.Add(1)
	c.storageEpoch.Add(1)
}

// mix64 is the splitmix64 finalizer, spreading the entropy of a key hash
// over all bits.
func mix64(h uint64) uint64 {
	h ^= h >> 30
	h *= 0xbf58476d1ce4e5b9
	h ^= h >> 27
	h *= 0x94d049bb133111eb
	h ^= h >> 31
	return h
}

// accountKeyHash derives the table hash of an account key. Collisions are
// harmless, keys are compared in full on lookup.
func accountKeyHash(addr common.Address) uint64 {
	h := binary.LittleEndian.Uint64(addr[:8]) * 0x9e3779b97f4a7c15
	h ^= binary.LittleEndian.Uint64(addr[8:16]) * 0xff51afd7ed558ccd
	h ^= uint64(binary.LittleEndian.Uint32(addr[16:])) * 0xc4ceb9fe1a85ec53
	return mix64(h)
}

// storageKeyHash derives the table hash of a storage slot key.
func storageKeyHash(addr common.Address, slot common.Hash) uint64 {
	h := accountKeyHash(addr)
	h ^= binary.LittleEndian.Uint64(slot[:8]) * 0x9e3779b97f4a7c15
	h ^= binary.LittleEndian.Uint64(slot[8:16]) * 0xff51afd7ed558ccd
	h ^= binary.LittleEndian.Uint64(slot[16:24]) * 0xc4ceb9fe1a85ec53
	h ^= binary.LittleEndian.Uint64(slot[24:]) * 0x2545f4914f6cdd1d
	return mix64(h)
}

// tableIndices derives the two skew associative positions of a key hash. The
// ways use independent bit ranges of the hash to decorrelate collisions.
func tableIndices(h uint64, bits int) (uint64, uint64) {
	mask := uint64(1)<<bits - 1
	return h & mask, (h >> 32) & mask
}

// account retrieves the account specified by the address along with a flag
// indicating whether it was found in the cache.
func (c *fixedStateCache) account(addr common.Address) (*types.StateAccount, bool, error) {
	var (
		epoch  = c.accountEpoch.Load()
		i1, i2 = tableIndices(accountKeyHash(addr), c.accountBits)
	)
	e1 := c.accounts[i1].Load()
	if matchAccount(e1, epoch, addr) {
		return e1.account, true, nil
	}
	e2 := c.accounts[i2].Load()
	if matchAccount(e2, epoch, addr) {
		return e2.account, true, nil
	}
	// Not cached, resolve from the underlying reader and publish. Prefer to
	// displace an empty or stale way over a live one.
	account, err := c.StateReader.Account(addr)
	if err != nil {
		return nil, false, err
	}
	entry := &accountCacheEntry{epoch: epoch, addr: addr, account: account}
	switch {
	case e1 == nil || e1.epoch != epoch:
		c.accounts[i1].Store(entry)
	case e2 == nil || e2.epoch != epoch:
		c.accounts[i2].Store(entry)
	default:
		c.accounts[i1].Store(entry)
	}
	return account, false, nil
}

// Account implements StateReader, retrieving the account specified by the
// address. The returned account might be nil if it's not existent.
func (c *fixedStateCache) Account(addr common.Address) (*types.StateAccount, error) {
	account, _, err := c.account(addr)
	return account, err
}

// storage retrieves the storage slot specified by the address and slot key,
// along with a flag indicating whether it was found in the cache.
func (c *fixedStateCache) storage(addr common.Address, slot common.Hash) (common.Hash, bool, error) {
	var (
		epoch  = c.storageEpoch.Load()
		i1, i2 = tableIndices(storageKeyHash(addr, slot), c.storageBits)
	)
	e1 := c.storages[i1].Load()
	if matchStorage(e1, epoch, addr, slot) {
		return e1.value, true, nil
	}
	e2 := c.storages[i2].Load()
	if matchStorage(e2, epoch, addr, slot) {
		return e2.value, true, nil
	}
	value, err := c.StateReader.Storage(addr, slot)
	if err != nil {
		return common.Hash{}, false, err
	}
	c.publishStorage(epoch, addr, slot, value, i1, i2, e1, e2)
	return value, false, nil
}

// Storage implements StateReader, retrieving the storage slot specified by
// the address and slot key.
func (c *fixedStateCache) Storage(addr common.Address, slot common.Hash) (common.Hash, error) {
	value, _, err := c.storage(addr, slot)
	return value, err
}

// publishStorage inserts a storage entry, preferring to displace an empty or
// stale way over a live one.
func (c *fixedStateCache) publishStorage(epoch uint64, addr common.Address, slot common.Hash, value common.Hash, i1, i2 uint64, e1, e2 *storageCacheEntry) {
	entry := &storageCacheEntry{epoch: epoch, addr: addr, slot: slot, value: value}
	switch {
	case e1 == nil || e1.epoch != epoch:
		c.storages[i1].Store(entry)
	case e2 == nil || e2.epoch != epoch:
		c.storages[i2].Store(entry)
	default:
		c.storages[i1].Store(entry)
	}
}

// applyUpdate folds a block state diff into the cached entries, keeping them
// valid for the child state. The caller must guarantee exclusive access.
func (c *fixedStateCache) applyUpdate(update *StateUpdate) {
	// Refresh the mutated accounts. A nil account is a valid cache entry
	// meaning nonexistent, so deletions stay warm too.
	epoch := c.accountEpoch.Load()
	hashes := make(map[common.Address]common.Hash, len(update.AccountsOrigin))
	for addr := range update.AccountsOrigin {
		addrHash := crypto.Keccak256Hash(addr.Bytes())
		hashes[addr] = addrHash

		// Racing fills can leave the same key in both ways, so every
		// matching way must be overwritten or a stale twin survives.
		i1, i2 := tableIndices(accountKeyHash(addr), c.accountBits)
		entry := &accountCacheEntry{epoch: epoch, addr: addr, account: update.Accounts[addrHash]}
		var (
			m1 = matchAccount(c.accounts[i1].Load(), epoch, addr)
			m2 = matchAccount(c.accounts[i2].Load(), epoch, addr)
		)
		if m1 || !m2 {
			c.accounts[i1].Store(entry)
		}
		if m2 {
			c.accounts[i2].Store(entry)
		}
	}
	// Refresh the mutated storage slots. With plain keyed origins every
	// touched slot is enumerable and rewritten precisely, deleted slots
	// become zero valued entries. With hashed keys the plain slot keys are
	// unknown, invalidate all storage entries instead.
	if update.StorageKeyType != StorageKeyPlain {
		if len(update.StoragesOrigin) > 0 {
			c.storageEpoch.Add(1)
		}
		return
	}
	epoch = c.storageEpoch.Load()
	for addr, slots := range update.StoragesOrigin {
		addrHash, ok := hashes[addr]
		if !ok {
			addrHash = crypto.Keccak256Hash(addr.Bytes())
		}
		post := update.Storages[addrHash]
		for key := range slots {
			i1, i2 := tableIndices(storageKeyHash(addr, key), c.storageBits)
			value := post[crypto.Keccak256Hash(key.Bytes())]
			entry := &storageCacheEntry{epoch: epoch, addr: addr, slot: key, value: value}
			var (
				m1 = matchStorage(c.storages[i1].Load(), epoch, addr, key)
				m2 = matchStorage(c.storages[i2].Load(), epoch, addr, key)
			)
			if m1 || !m2 {
				c.storages[i1].Store(entry)
			}
			if m2 {
				c.storages[i2].Store(entry)
			}
		}
	}
}

// matchAccount reports whether the entry is a live cache entry of the account.
func matchAccount(e *accountCacheEntry, epoch uint64, addr common.Address) bool {
	return e != nil && e.epoch == epoch && e.addr == addr
}

// matchStorage reports whether the entry is a live cache entry of the slot.
func matchStorage(e *storageCacheEntry, epoch uint64, addr common.Address, slot common.Hash) bool {
	return e != nil && e.epoch == epoch && e.addr == addr && e.slot == slot
}
