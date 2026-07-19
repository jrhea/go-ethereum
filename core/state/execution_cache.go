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

	"github.com/ethereum/go-ethereum/common"
)

// ExecutionCache retains the state reader cache shared by the prefetcher and
// block processing across blocks, so the hot read set does not start cold on
// every block. The content is tagged with the state root it reflects and is
// only reused by a block built on a matching parent root, anything else
// starts over.
//
// The cache is checked out through ReadersWithCacheStats and stays locked
// until Release hands it back, so a block is never warmed by writes of a
// concurrent one.
type ExecutionCache struct {
	mu    sync.Mutex // held from checkout until release
	cache *fixedStateCache
	root  common.Hash
}

// NewExecutionCache constructs the cross block execution cache.
func NewExecutionCache() *ExecutionCache {
	return new(ExecutionCache)
}

// use checks the cache out for a block built on the given root, reusing the
// retained content on a tag match and invalidating it otherwise. The wrapped
// state reader is swapped either way. The internal lock is held until Release
// is called.
func (ec *ExecutionCache) use(root common.Hash, underlying StateReader) *fixedStateCache {
	ec.mu.Lock()
	if ec.cache == nil {
		ec.cache = newFixedStateCache(underlying, fixedAccountCacheBits, fixedStorageCacheBits)
	} else {
		ec.cache.StateReader = underlying
		if ec.root != root {
			ec.cache.reset()
		}
	}
	ec.root = root
	return ec.cache
}

// Release hands the cache back after a block. A non nil update anchored on
// the checkout root folds the written entries into the cache and moves the
// tag to the child state. A nil update keeps the content valid for the
// checkout root, covering failed or discarded blocks. The caller must
// guarantee that both readers stopped writing before releasing.
func (ec *ExecutionCache) Release(update *StateUpdate) {
	if update != nil && ec.cache != nil {
		if update.OriginRoot == ec.root {
			ec.cache.applyUpdate(update)
			ec.root = update.Root
		} else {
			// The settled block does not descend from the checkout root.
			// This is not expected to happen, invalidate to be safe.
			ec.cache.reset()
		}
	}
	ec.mu.Unlock()
}
