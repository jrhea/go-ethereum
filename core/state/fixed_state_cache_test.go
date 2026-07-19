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
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
)

// fixedCacheFixture builds a stub backed cache with tiny tables, so entries
// constantly displace each other.
func fixedCacheFixture(keys int, bits int) (*fixedStateCache, *stubCountingReader) {
	underlying := &stubCountingReader{
		accounts: make(map[common.Address]*types.StateAccount),
		storages: make(map[common.Address]map[common.Hash]common.Hash),
	}
	for i := 0; i < keys; i++ {
		addr := common.Address{byte(i), 0xff}
		underlying.accounts[addr] = &types.StateAccount{Nonce: uint64(i + 1), Balance: uint256.NewInt(uint64(i))}
		underlying.storages[addr] = map[common.Hash]common.Hash{
			{byte(i)}: {byte(i), 1},
			{0xee}:    {byte(i), 2},
		}
	}
	return newFixedStateCache(underlying, bits, bits), underlying
}

// TestFixedStateCacheDisplacement hammers a tiny cache with more keys than
// slots. Displaced entries must come back as misses, never as wrong values.
func TestFixedStateCacheDisplacement(t *testing.T) {
	cache, underlying := fixedCacheFixture(64, 2)
	for round := 0; round < 8; round++ {
		for i := 0; i < 64; i++ {
			addr := common.Address{byte(i), 0xff}
			acct, _, err := cache.account(addr)
			if err != nil || acct.Nonce != uint64(i+1) {
				t.Fatalf("wrong account for %d: %v %v", i, acct, err)
			}
			for slot, want := range underlying.storages[addr] {
				if val, _, err := cache.storage(addr, slot); err != nil || val != want {
					t.Fatalf("wrong slot value for %d: have %x want %x, err %v", i, val, want, err)
				}
			}
		}
	}
	// A nonexistent account must be cached as nil and still be a valid hit.
	missing := common.Address{0xde, 0xad}
	if acct, _, _ := cache.account(missing); acct != nil {
		t.Fatalf("expected nil account, got %v", acct)
	}
	before := underlying.reads.Load()
	if acct, incache, _ := cache.account(missing); acct != nil || (incache && underlying.reads.Load() != before) {
		t.Fatalf("nil account not cached properly")
	}
}

// TestFixedStateCacheConcurrent hammers the cache from many goroutines under
// constant displacement. Run with the race detector, every result must match
// the underlying state.
func TestFixedStateCacheConcurrent(t *testing.T) {
	cache, underlying := fixedCacheFixture(32, 3)

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for j := 0; j < 5000; j++ {
				i := (j + seed) % 32
				addr := common.Address{byte(i), 0xff}
				acct, _, err := cache.account(addr)
				if err != nil || acct == nil || acct.Nonce != uint64(i+1) {
					t.Errorf("wrong account for %d: %v %v", i, acct, err)
					return
				}
				slot := common.Hash{byte(i)}
				if val, _, err := cache.storage(addr, slot); err != nil || val != (common.Hash{byte(i), 1}) {
					t.Errorf("wrong slot value for %d: %x %v", i, val, err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	_ = underlying
}

// TestFixedStateCacheTwinRefresh pins the twin way hazard. A key sitting in
// both ways, as racing fills can leave it, must not serve a stale value after
// an update refresh.
func TestFixedStateCacheTwinRefresh(t *testing.T) {
	cache, _ := fixedCacheFixture(1, 4)
	var (
		addr  = common.Address{0, 0xff}
		slot  = common.Hash{0}
		epoch = cache.storageEpoch.Load()
	)
	// Plant the same slot in both ways with the old value, mimicking the
	// outcome of two racing fills.
	i1, i2 := tableIndices(storageKeyHash(addr, slot), 4)
	if i1 == i2 {
		t.Skip("degenerate indices for this key size")
	}
	stale := &storageCacheEntry{epoch: epoch, addr: addr, slot: slot, value: common.Hash{0, 1}}
	cache.storages[i1].Store(stale)
	cache.storages[i2].Store(stale)

	// Fold in a block rewriting the slot.
	addrHash := crypto.Keccak256Hash(addr.Bytes())
	cache.applyUpdate(&StateUpdate{
		Accounts:       map[common.Hash]*types.StateAccount{},
		AccountsOrigin: map[common.Address]*types.StateAccount{},
		Storages: map[common.Hash]map[common.Hash]common.Hash{
			addrHash: {crypto.Keccak256Hash(slot.Bytes()): {42}},
		},
		StoragesOrigin: map[common.Address]map[common.Hash]common.Hash{
			addr: {slot: {0, 1}},
		},
		StorageKeyType: StorageKeyPlain,
	})
	for round := 0; round < 2; round++ {
		if val, _, _ := cache.storage(addr, slot); val != (common.Hash{42}) {
			t.Fatalf("stale twin served after refresh, got %x", val)
		}
	}
}
