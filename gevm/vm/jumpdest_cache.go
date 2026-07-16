package vm

import (
	"sync/atomic"

	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/Giulio2002/gevm/types"
)

// jumpDestCacheSize is the default capacity of the process-global
// JUMPDEST bitmap cache. Sized for mainnet's hot contract set; tune via
// ResizeGlobalJumpDestCache when embedding under different workloads.
const jumpDestCacheSize = 32768

var (
	globalJumpDestCache *lru.Cache[types.B256, []byte]
	jumpDestCacheOn     atomic.Bool
)

func init() {
	cache, _ := lru.New[types.B256, []byte](jumpDestCacheSize)
	globalJumpDestCache = cache
	jumpDestCacheOn.Store(true)
}

// SetGlobalJumpDestCacheEnabled toggles the process-wide JUMPDEST cache.
// When disabled, GetCachedJumpDest always returns (nil, false) and
// PutCachedJumpDest is a no-op. Defaults to enabled.
func SetGlobalJumpDestCacheEnabled(on bool) {
	jumpDestCacheOn.Store(on)
}

// IsGlobalJumpDestCacheEnabled reports whether the JUMPDEST cache is
// currently serving lookups.
func IsGlobalJumpDestCacheEnabled() bool {
	return jumpDestCacheOn.Load()
}

// ResizeGlobalJumpDestCache rebuilds the cache at a new capacity, dropping
// any existing entries. size <= 0 is treated as jumpDestCacheSize.
func ResizeGlobalJumpDestCache(size int) {
	if size <= 0 {
		size = jumpDestCacheSize
	}
	cache, _ := lru.New[types.B256, []byte](size)
	globalJumpDestCache = cache
}

// PurgeGlobalJumpDestCache evicts every entry. Useful for benchmarks that
// want a cold start without restarting the process.
func PurgeGlobalJumpDestCache() {
	globalJumpDestCache.Purge()
}

// GetCachedJumpDest returns the cached JUMPDEST bitmap for codeHash.
func GetCachedJumpDest(codeHash types.B256) ([]byte, bool) {
	if !jumpDestCacheOn.Load() {
		return nil, false
	}
	return globalJumpDestCache.Get(codeHash)
}

// PutCachedJumpDest stores the JUMPDEST bitmap for codeHash. Empty bitmaps
// are ignored. Eviction is LRU when capacity is reached.
func PutCachedJumpDest(codeHash types.B256, bitmap []byte) {
	if !jumpDestCacheOn.Load() || len(bitmap) == 0 {
		return
	}
	globalJumpDestCache.Add(codeHash, bitmap)
}
