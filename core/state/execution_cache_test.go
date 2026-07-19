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
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
)

// stubCountingReader is a stub StateReader tracking underlying accesses.
type stubCountingReader struct {
	accounts map[common.Address]*types.StateAccount
	storages map[common.Address]map[common.Hash]common.Hash
	reads    int
}

func (r *stubCountingReader) Account(addr common.Address) (*types.StateAccount, error) {
	r.reads++
	return r.accounts[addr], nil
}

func (r *stubCountingReader) Storage(addr common.Address, slot common.Hash) (common.Hash, error) {
	r.reads++
	return r.storages[addr][slot], nil
}

func TestExecutionCacheReuse(t *testing.T) {
	var (
		ec    = NewExecutionCache()
		addr  = common.Address{1}
		slot  = common.Hash{2}
		acct  = &types.StateAccount{Nonce: 1, Balance: uint256.NewInt(100)}
		rootA = common.Hash{0xaa}
		rootB = common.Hash{0xbb}
	)
	underlying := &stubCountingReader{
		accounts: map[common.Address]*types.StateAccount{addr: acct},
		storages: map[common.Address]map[common.Hash]common.Hash{addr: {slot: {3}}},
	}
	// First block, cold cache, reads fall through.
	cache := ec.use(rootA, underlying)
	cache.Account(addr)
	cache.Storage(addr, slot)
	if underlying.reads != 2 {
		t.Fatalf("expected 2 underlying reads, got %d", underlying.reads)
	}
	ec.Release(&StateUpdate{OriginRoot: rootA, Root: rootB})

	// Second block on the child root, content must be reused.
	cache = ec.use(rootB, underlying)
	cache.Account(addr)
	cache.Storage(addr, slot)
	if underlying.reads != 2 {
		t.Fatalf("cache not reused, %d underlying reads", underlying.reads)
	}
	ec.Release(nil)

	// A failed or discarded block keeps the content valid for its root.
	cache = ec.use(rootB, underlying)
	cache.Account(addr)
	if underlying.reads != 2 {
		t.Fatalf("cache not retained after nil release, %d underlying reads", underlying.reads)
	}
	ec.Release(nil)

	// A mismatching parent root must start over.
	cache = ec.use(common.Hash{0xcc}, underlying)
	cache.Account(addr)
	if underlying.reads != 3 {
		t.Fatalf("cache not reset on root mismatch, %d underlying reads", underlying.reads)
	}
	ec.Release(nil)
}

func TestExecutionCacheApply(t *testing.T) {
	var (
		ec    = NewExecutionCache()
		addr  = common.Address{1}
		slotA = common.Hash{0xa}
		slotB = common.Hash{0xb}
		rootA = common.Hash{0xaa}
		rootB = common.Hash{0xbb}
	)
	underlying := &stubCountingReader{
		accounts: map[common.Address]*types.StateAccount{addr: {Nonce: 1, Balance: uint256.NewInt(1)}},
		storages: map[common.Address]map[common.Hash]common.Hash{addr: {slotA: {1}, slotB: {2}}},
	}
	cache := ec.use(rootA, underlying)
	cache.Account(addr)
	cache.Storage(addr, slotA)
	cache.Storage(addr, slotB)

	// The block bumps the account nonce, rewrites slotA and deletes slotB.
	addrHash := crypto.Keccak256Hash(addr.Bytes())
	post := &types.StateAccount{Nonce: 2, Balance: uint256.NewInt(1)}
	ec.Release(&StateUpdate{
		OriginRoot:     rootA,
		Root:           rootB,
		Accounts:       map[common.Hash]*types.StateAccount{addrHash: post},
		AccountsOrigin: map[common.Address]*types.StateAccount{addr: nil},
		Storages: map[common.Hash]map[common.Hash]common.Hash{
			addrHash: {crypto.Keccak256Hash(slotA.Bytes()): {42}},
		},
		StoragesOrigin: map[common.Address]map[common.Hash]common.Hash{
			addr: {slotA: {1}, slotB: {2}},
		},
		StorageKeyType: StorageKeyPlain,
	})

	// The refreshed values must be served without underlying reads.
	underlying.reads = 0
	cache = ec.use(rootB, underlying)
	if acct, _ := cache.Account(addr); acct.Nonce != 2 {
		t.Fatalf("account not refreshed, nonce %d", acct.Nonce)
	}
	if val, _ := cache.Storage(addr, slotA); val != (common.Hash{42}) {
		t.Fatalf("rewritten slot not refreshed, got %x", val)
	}
	if val, _ := cache.Storage(addr, slotB); val != (common.Hash{}) {
		t.Fatalf("deleted slot not zeroed, got %x", val)
	}
	if underlying.reads != 0 {
		t.Fatalf("refreshed entries not served from cache, %d underlying reads", underlying.reads)
	}
	ec.Release(nil)
}

func TestExecutionCacheApplyHashedKeys(t *testing.T) {
	var (
		ec    = NewExecutionCache()
		addr  = common.Address{1}
		slotA = common.Hash{0xa}
		rootA = common.Hash{0xaa}
		rootB = common.Hash{0xbb}
	)
	underlying := &stubCountingReader{
		accounts: map[common.Address]*types.StateAccount{addr: {Nonce: 1, Balance: uint256.NewInt(1)}},
		storages: map[common.Address]map[common.Hash]common.Hash{addr: {slotA: {1}}},
	}
	cache := ec.use(rootA, underlying)
	cache.Storage(addr, slotA)

	// With hashed keys the plain slot keys are unknown, the account's slots
	// must be dropped instead of refreshed.
	ec.Release(&StateUpdate{
		OriginRoot: rootA,
		Root:       rootB,
		StoragesOrigin: map[common.Address]map[common.Hash]common.Hash{
			addr: {crypto.Keccak256Hash(slotA.Bytes()): {1}},
		},
		StorageKeyType: StorageKeyHashed,
	})
	underlying.storages[addr][slotA] = common.Hash{9}
	underlying.reads = 0

	cache = ec.use(rootB, underlying)
	if val, _ := cache.Storage(addr, slotA); val != (common.Hash{9}) {
		t.Fatalf("stale slot served after hashed key update, got %x", val)
	}
	if underlying.reads != 1 {
		t.Fatalf("expected an underlying read after invalidation, got %d", underlying.reads)
	}
	ec.Release(nil)
}

func TestExecutionCacheLineageMismatch(t *testing.T) {
	var (
		ec    = NewExecutionCache()
		addr  = common.Address{1}
		rootA = common.Hash{0xaa}
	)
	underlying := &stubCountingReader{
		accounts: map[common.Address]*types.StateAccount{addr: {Nonce: 1, Balance: uint256.NewInt(1)}},
	}
	cache := ec.use(rootA, underlying)
	cache.Account(addr)

	// An update not anchored on the checkout root must drop the content.
	ec.Release(&StateUpdate{OriginRoot: common.Hash{0xdd}, Root: common.Hash{0xee}})

	underlying.reads = 0
	cache = ec.use(rootA, underlying)
	cache.Account(addr)
	if underlying.reads != 1 {
		t.Fatalf("cache not dropped on lineage mismatch, %d underlying reads", underlying.reads)
	}
	ec.Release(nil)
}
