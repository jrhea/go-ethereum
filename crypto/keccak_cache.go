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

//go:build !ziren

package crypto

import (
	"encoding/binary"
	"hash/maphash"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
)

const (
	keccakCacheBits     = 17 // 128K entries, 16 MB
	keccakCacheMaxInput = 87 // longest input the cache holds
	keccakCacheWords    = (keccakCacheMaxInput + 7) / 8

	// The seq word holds the lock in bit 0, the input length plus one in bits 1-7,
	// a version in bits 8-23 and the top 40 bits of the input's hash above that.
	keccakSeqLocked      = 1
	keccakSeqVersionUnit = 1 << 8
	keccakSeqVersionMask = 0xffff << 8
	keccakSeqKeyMask     = ^uint64(keccakSeqVersionMask)
)

// keccakCacheEntry holds one input and its hash as words, so readers can load them atomically.
type keccakCacheEntry struct {
	seq  atomic.Uint64
	data [keccakCacheWords]atomic.Uint64
	hash [4]atomic.Uint64
}

var (
	keccakCache     = new([1 << keccakCacheBits]keccakCacheEntry) // page aligned, so every 128 byte entry starts a cache line
	keccakCacheSeed = maphash.MakeSeed()
)

// cachedKeccak256 returns the hash of a short input, reusing an earlier result for the same input.
func cachedKeccak256(data []byte) common.Hash {
	h := maphash.Bytes(keccakCacheSeed, data)
	e := &keccakCache[h&(1<<keccakCacheBits-1)]
	key := h>>24<<24 | uint64(len(data)+1)<<1

	var in [keccakCacheWords]uint64
	n := keccakInputWords(data, &in)

	// Lookups never write. A copy is only used if the seq word did not change while it was taken,
	// and the key mask keeps the lock bit so a locked entry never matches.
	if seq := e.seq.Load(); seq&keccakSeqKeyMask == key {
		match := true
		for i := 0; i < n; i++ {
			if e.data[i].Load() != in[i] {
				match = false
				break
			}
		}
		if match {
			h0, h1, h2, h3 := e.hash[0].Load(), e.hash[1].Load(), e.hash[2].Load(), e.hash[3].Load()
			if e.seq.Load() == seq {
				var out common.Hash
				binary.LittleEndian.PutUint64(out[0:], h0)
				binary.LittleEndian.PutUint64(out[8:], h1)
				binary.LittleEndian.PutUint64(out[16:], h2)
				binary.LittleEndian.PutUint64(out[24:], h3)
				return out
			}
		}
	}
	out := keccak256(data)

	// Store unless another goroutine is writing the entry. The version bump tells readers
	// that overlapped this write to discard their copy, even if the input is the same.
	if seq := e.seq.Load(); seq&keccakSeqLocked == 0 && e.seq.CompareAndSwap(seq, seq|keccakSeqLocked) {
		for i := 0; i < n; i++ {
			e.data[i].Store(in[i])
		}
		e.hash[0].Store(binary.LittleEndian.Uint64(out[0:]))
		e.hash[1].Store(binary.LittleEndian.Uint64(out[8:]))
		e.hash[2].Store(binary.LittleEndian.Uint64(out[16:]))
		e.hash[3].Store(binary.LittleEndian.Uint64(out[24:]))
		e.seq.Store(key | (seq+keccakSeqVersionUnit)&keccakSeqVersionMask)
	}
	return out
}

// keccakInputWords packs data into little endian words, zero padding the last one, and returns the word count.
func keccakInputWords(data []byte, words *[keccakCacheWords]uint64) int {
	n := len(data) / 8
	for i := 0; i < n; i++ {
		words[i] = binary.LittleEndian.Uint64(data[i*8:])
	}
	if r := len(data) % 8; r != 0 {
		var tail [8]byte
		copy(tail[:], data[n*8:])
		words[n] = binary.LittleEndian.Uint64(tail[:])
		n++
	}
	return n
}

// keccak256 hashes a single input without the cache.
func keccak256(data []byte) (h common.Hash) {
	d := hasherPool.Get().(KeccakState)
	d.Reset()
	d.Write(data)
	d.Read(h[:])
	hasherPool.Put(d)
	return h
}
