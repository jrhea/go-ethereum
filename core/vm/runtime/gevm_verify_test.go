// Copyright 2024 The go-ethereum Authors
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

package runtime

import (
	"bytes"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm/gevmbridge"
)

// withGevm runs fn with the gevm backend forced on, restoring the previous
// setting afterward.
func withGevm(t *testing.T, fn func()) { withBackend(t, true, fn) }

// withNative runs fn with the gevm backend forced off (native geth EVM),
// needed because gevm is on by default on this branch.
func withNative(t *testing.T, fn func()) { withBackend(t, false, fn) }

func withBackend(t *testing.T, enabled bool, fn func()) {
	t.Helper()
	prev := gevmbridge.Enabled
	gevmbridge.Enabled = enabled
	defer func() { gevmbridge.Enabled = prev }()
	fn()
}

// TestGevmExecuteParity checks that a compute workload executed through gevm
// returns the same value as geth's native interpreter.
func TestGevmExecuteParity(t *testing.T) {
	// (2 + 3) * 4 = 20; store at mem[0] and return 32 bytes.
	code := []byte{
		0x60, 0x02, // PUSH1 2
		0x60, 0x03, // PUSH1 3
		0x01,       // ADD
		0x60, 0x04, // PUSH1 4
		0x02,       // MUL
		0x60, 0x00, // PUSH1 0
		0x52,       // MSTORE
		0x60, 0x20, // PUSH1 32
		0x60, 0x00, // PUSH1 0
		0xf3, // RETURN
	}

	var (
		native []byte
		err    error
	)
	withNative(t, func() {
		native, _, err = Execute(code, nil, nil)
	})
	if err != nil {
		t.Fatalf("native execute: %v", err)
	}

	var gevm []byte
	withGevm(t, func() {
		gevm, _, err = Execute(code, nil, nil)
	})
	if err != nil {
		t.Fatalf("gevm execute: %v", err)
	}

	if !bytes.Equal(native, gevm) {
		t.Fatalf("return mismatch: native=%x gevm=%x", native, gevm)
	}
	if len(gevm) != 32 || gevm[31] != 20 {
		t.Fatalf("unexpected return value: %x", gevm)
	}
}

// TestGevmStorageWriteBack checks that storage written by a contract executed
// through gevm is mirrored back into geth's StateDB.
func TestGevmStorageWriteBack(t *testing.T) {
	// SSTORE(slot 1, 42); return SLOAD(slot 1).
	code := []byte{
		0x60, 0x2a, // PUSH1 42
		0x60, 0x01, // PUSH1 1
		0x55,       // SSTORE
		0x60, 0x01, // PUSH1 1
		0x54,       // SLOAD
		0x60, 0x00, // PUSH1 0
		0x52,       // MSTORE
		0x60, 0x20, // PUSH1 32
		0x60, 0x00, // PUSH1 0
		0xf3, // RETURN
	}

	var (
		ret []byte
		err error
		cfg = new(Config)
	)
	withGevm(t, func() {
		ret, _, err = Execute(code, nil, cfg)
	})
	if err != nil {
		t.Fatalf("gevm execute: %v", err)
	}
	if len(ret) != 32 || ret[31] != 42 {
		t.Fatalf("unexpected return value: %x", ret)
	}

	// The contract address used by Execute is deterministic; slot 1 must now
	// hold 42 in the StateDB, proving the write-back path ran.
	contract := common.BytesToAddress([]byte("contract"))
	got := cfg.State.GetState(contract, common.Hash{31: 1})
	if want := (common.Hash{31: 42}); got != want {
		t.Fatalf("slot 1 = %x, want %x", got, want)
	}
}
