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

package vm

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params"
)

// warm is a taken jump that resolves the contract's analysis, after which the
// fast path fetches from the fused shadow. A fused opcode can only fire after
// it, so every fused canary starts here. It is 4 bytes, so what follows starts
// at pc 4.
var warm = asm(PUSH1, 0x03, JUMP, JUMPDEST)

// ret32 returns the 32-byte word at memory 0.
var ret32 = asm(PUSH1, 0x00, MSTORE, PUSH1, 0x20, PUSH1, 0x00, RETURN)

// fusedDiffPrograms pin every fused opcode edge by edge. Each runs through the
// differential test against the table loop, and fusedOutcomes below says what
// the ones with a definite result must produce, so a canary that quietly stops
// reaching its fused path shows up.
var fusedDiffPrograms = []struct {
	name string
	code []byte
	gas  uint64
}{
	// PUSH2 dest JUMP: taken, to a non-JUMPDEST, and into PUSH data that holds 0x5b.
	{"fused-push2-jump", asm(warm, PUSH2, 0x00, 0x09, JUMP, INVALID, JUMPDEST, PUSH1, 0x2a, ret32), 100000},
	{"fused-push2-jump-invalid", asm(warm, PUSH2, 0x00, 0x08, JUMP, PUSH1, 0x00, STOP), 100000},
	{"fused-push2-jump-into-data", asm(warm, PUSH2, 0x00, 0x09, JUMP, PUSH1, 0x5b, STOP), 100000},
	// PUSH2 dest JUMPI: a not taken and a taken one in a row, then the empty
	// stack case, where the sequential run pushes and JUMPI underflows.
	{"fused-push2-jumpi-both", asm(warm, PUSH1, 0x00, PUSH2, 0x00, 0x1c, JUMPI, PUSH1, 0x01, PUSH2, 0x00, 0x11, JUMPI, INVALID, JUMPDEST, PUSH1, 0x2a, ret32, JUMPDEST, INVALID), 100000},
	{"fused-jumpi-empty-stack", asm(warm, PUSH2, 0x00, 0x00, JUMPI), 100000},
	// Gas boundaries of a taken PUSH2 JUMPI: the warm-up costs 12, PUSH1 3,
	// the pair 13, the landing JUMPDEST 1. 27 fails inside the pair, 28 on the
	// landing, 29 runs.
	{"fused-push2-jumpi-gas-27", asm(warm, PUSH1, 0x01, PUSH2, 0x00, 0x0b, JUMPI, INVALID, JUMPDEST, STOP), 27},
	{"fused-push2-jumpi-gas-28", asm(warm, PUSH1, 0x01, PUSH2, 0x00, 0x0b, JUMPI, INVALID, JUMPDEST, STOP), 28},
	{"fused-push2-jumpi-gas-29", asm(warm, PUSH1, 0x01, PUSH2, 0x00, 0x0b, JUMPI, INVALID, JUMPDEST, STOP), 29},
	// Immediates that look like a jump, a PUSH2 cut off by the end of code,
	// and a fused loop that has to burn its gas and halt.
	{"fused-imm-contains-jump", asm(warm, PUSH2, 0x56, 0x57, POP, STOP), 100000},
	{"fused-truncated-push2", asm(warm, PUSH2, 0x00), 100000},
	{"fused-jump-loop-oog", asm(warm, JUMPDEST, PUSH2, 0x00, 0x04, JUMP), 10000},
	// ISZERO PUSH2 dest JUMPI: taken on zero, not taken on one, invalid
	// destination, and the empty stack, where the sequential ISZERO underflows.
	{"fused-iszero-taken", asm(warm, PUSH1, 0x00, ISZERO, PUSH2, 0x00, 0x0c, JUMPI, INVALID, JUMPDEST, PUSH1, 0x2a, ret32), 100000},
	{"fused-iszero-not-taken", asm(warm, PUSH1, 0x01, ISZERO, PUSH2, 0x00, 0x15, JUMPI, PUSH1, 0x07, ret32, JUMPDEST, INVALID), 100000},
	{"fused-iszero-invalid-dest", asm(warm, PUSH1, 0x00, ISZERO, PUSH2, 0x00, 0x0b, JUMPI, INVALID), 100000},
	{"fused-iszero-underflow", asm(warm, ISZERO, PUSH2, 0x00, 0x00, JUMPI), 100000},
	// LT ISZERO PUSH2 dest JUMPI: the loop exit test. LT compares top < second.
	{"fused-lt-taken", asm(warm, PUSH1, 0x03, PUSH1, 0x05, LT, ISZERO, PUSH2, 0x00, 0x0f, JUMPI, INVALID, JUMPDEST, PUSH1, 0x2a, ret32), 100000},
	{"fused-lt-not-taken", asm(warm, PUSH1, 0x05, PUSH1, 0x03, LT, ISZERO, PUSH2, 0x00, 0x18, JUMPI, PUSH1, 0x07, ret32, JUMPDEST, INVALID), 100000},
	{"fused-lt-underflow", asm(warm, PUSH1, 0x01, LT, ISZERO, PUSH2, 0x00, 0x00, JUMPI), 100000},
	// DUP1 PUSH4 sel EQ PUSH2 dest JUMPI: the dispatcher arm. The selector stays
	// on the stack either way, so the hit and the miss both return it.
	{"fused-selector-hit", asm(warm, PUSH4, 0xde, 0xad, 0xbe, 0xef, DUP1, PUSH4, 0xde, 0xad, 0xbe, 0xef, EQ, PUSH2, 0x00, 0x15, JUMPI, INVALID, JUMPDEST, ret32), 100000},
	{"fused-selector-miss", asm(warm, PUSH4, 0xca, 0xfe, 0xba, 0xbe, DUP1, PUSH4, 0xde, 0xad, 0xbe, 0xef, EQ, PUSH2, 0x00, 0x1c, JUMPI, ret32, JUMPDEST, INVALID), 100000},
	{"fused-selector-chain", asm(warm, PUSH4, 0xde, 0xad, 0xbe, 0xef,
		DUP1, PUSH4, 0xca, 0xfe, 0xba, 0xbe, EQ, PUSH2, 0x00, 0x29, JUMPI,
		DUP1, PUSH4, 0xde, 0xad, 0xbe, 0xef, EQ, PUSH2, 0x00, 0x20, JUMPI,
		INVALID, JUMPDEST, ret32, JUMPDEST, INVALID), 100000},
	{"fused-selector-underflow", asm(warm, DUP1, PUSH4, 0xde, 0xad, 0xbe, 0xef, EQ, PUSH2, 0x00, 0x00, JUMPI), 100000},
	// A top wider than 64 bits never equals a selector.
	{"fused-selector-wide-top", asm(warm, PUSH9, 0x01, 0x00, 0x00, 0x00, 0x00, 0xde, 0xad, 0xbe, 0xef, DUP1, PUSH4, 0xde, 0xad, 0xbe, 0xef, EQ, PUSH2, 0x00, 0x21, JUMPI, ret32, JUMPDEST, INVALID), 100000},
	// PUSH1 x PUSH1 y PUSH1 z SHL SUB: the address mask, a wrapping subtraction,
	// a shift of 255, and the stack window: at 1022 items the sequential run
	// overflows on the third push.
	{"fused-shlsub-mask", asm(warm, PUSH1, 0x01, PUSH1, 0x01, PUSH1, 0xa0, SHL, SUB, ret32), 100000},
	{"fused-shlsub-wrap", asm(warm, PUSH1, 0xff, PUSH1, 0x02, PUSH1, 0x00, SHL, SUB, ret32), 100000},
	{"fused-shlsub-shift255", asm(warm, PUSH1, 0x01, PUSH1, 0x01, PUSH1, 0xff, SHL, SUB, ret32), 100000},
	{"fused-shlsub-near-full", asm(warm, bytes.Repeat(asm(PUSH1, 0x01), 1022), PUSH1, 0x01, PUSH1, 0x01, PUSH1, 0xa0, SHL, SUB, STOP), 100000},
	{"fused-push2-jumpi-full-stack", asm(warm, bytes.Repeat(asm(PUSH1, 0x01), 1024), PUSH2, 0x00, 0x00, JUMPI), 100000},
	// Bytes in the fused range that are really in the code: before the first
	// jump the fast path fetches them from the code itself, after it through
	// the escape. Both have to report the undefined opcode. In PUSH data they
	// are data.
	{"fused-raw-before-jump", asm(0xc1, STOP), 100000},
	{"fused-raw-after-jump", asm(warm, 0xc2, STOP), 100000},
	{"fused-raw-escape-value", asm(warm, 0xc0, STOP), 100000},
	{"fused-raw-in-push-data", asm(warm, PUSH1, 0xc1, POP, PUSH2, 0xc2, 0xc0, POP, STOP), 100000},
	// DUPN's immediate is not skipped by the analysis, so a DUP1 byte there
	// (0x80, which decodes to depth 17) is seen as heading a selector sequence.
	// It is never dispatched: DUPN reads it from the code and steps over it, and
	// the PUSH2 JUMPI after it fuses on its own. Amsterdam runs it, earlier
	// forks halt on the undefined DUPN, and both loops must agree either way.
	{"fused-dupn-immediate-is-head", asm(warm, bytes.Repeat(asm(PUSH1, 0x01), 17), DUPN, 0x80, PUSH4, 0x00, 0x00, 0x00, 0x01, EQ, PUSH2, 0x00, 0x33, JUMPI, INVALID, JUMPDEST, STOP), 100000},
	// The first jump of a frame runs unfused, the second fused.
	{"fused-first-jump-unfused", asm(PUSH1, 0x01, PUSH2, 0x00, 0x07, JUMPI, INVALID, JUMPDEST, PUSH1, 0x01, PUSH2, 0x00, 0x0e, JUMPI, JUMPDEST, STOP), 100000},
	// The error branches of the taken paths: landing on a JUMPDEST the frame
	// cannot pay for, and destinations that are not a JUMPDEST. The plain jumps
	// are hand emitted too, so they get the same edges.
	{"jump-underflow", asm(warm, JUMP), 100000},
	{"jump-overflow-dest", asm(warm, PUSH32, bytes.Repeat([]byte{0xff}, 32), JUMP), 100000},
	{"jumpi-overflow-dest", asm(warm, PUSH1, 0x01, PUSH32, bytes.Repeat([]byte{0xff}, 32), JUMPI), 100000},
	{"jump-landing-oog", asm(warm, PUSH1, 0x07, JUMP, JUMPDEST, STOP), 23},
	{"jump-landing-paid", asm(warm, PUSH1, 0x07, JUMP, JUMPDEST, STOP), 24},
	{"jumpi-landing-oog", asm(warm, PUSH1, 0x01, PUSH1, 0x09, JUMPI, JUMPDEST, STOP), 28},
	{"jumpi-landing-paid", asm(warm, PUSH1, 0x01, PUSH1, 0x09, JUMPI, JUMPDEST, STOP), 29},
	{"fused-push2-jump-landing-oog", asm(warm, PUSH2, 0x00, 0x09, JUMP, INVALID, JUMPDEST, STOP), 23},
	{"fused-push2-jump-landing-paid", asm(warm, PUSH2, 0x00, 0x09, JUMP, INVALID, JUMPDEST, STOP), 24},
	{"fused-push2-jumpi-invalid-dest", asm(warm, PUSH1, 0x01, PUSH2, 0x00, 0x0a, JUMPI, INVALID), 100000},
	{"fused-iszero-landing-oog", asm(warm, PUSH1, 0x00, ISZERO, PUSH2, 0x00, 0x0c, JUMPI, INVALID, JUMPDEST, STOP), 31},
	{"fused-iszero-landing-paid", asm(warm, PUSH1, 0x00, ISZERO, PUSH2, 0x00, 0x0c, JUMPI, INVALID, JUMPDEST, STOP), 32},
	{"fused-lt-invalid-dest", asm(warm, PUSH1, 0x03, PUSH1, 0x05, LT, ISZERO, PUSH2, 0x00, 0x0e, JUMPI, INVALID), 100000},
	{"fused-lt-landing-oog", asm(warm, PUSH1, 0x03, PUSH1, 0x05, LT, ISZERO, PUSH2, 0x00, 0x0f, JUMPI, INVALID, JUMPDEST, STOP), 37},
	{"fused-lt-landing-paid", asm(warm, PUSH1, 0x03, PUSH1, 0x05, LT, ISZERO, PUSH2, 0x00, 0x0f, JUMPI, INVALID, JUMPDEST, STOP), 38},
	{"fused-selector-invalid-dest", asm(warm, PUSH4, 0xde, 0xad, 0xbe, 0xef, DUP1, PUSH4, 0xde, 0xad, 0xbe, 0xef, EQ, PUSH2, 0x00, 0x14, JUMPI, INVALID), 100000},
	{"fused-selector-landing-oog", asm(warm, PUSH4, 0xde, 0xad, 0xbe, 0xef, DUP1, PUSH4, 0xde, 0xad, 0xbe, 0xef, EQ, PUSH2, 0x00, 0x15, JUMPI, INVALID, JUMPDEST, STOP), 37},
	{"fused-selector-landing-paid", asm(warm, PUSH4, 0xde, 0xad, 0xbe, 0xef, DUP1, PUSH4, 0xde, 0xad, 0xbe, 0xef, EQ, PUSH2, 0x00, 0x15, JUMPI, INVALID, JUMPDEST, STOP), 38},
	// Initcode has no code hash, so its analysis is local to the frame.
	{"fused-initcode", asm(PUSH17, []byte{0x60, 0x03, 0x56, 0x5b, 0x60, 0x01, 0x61, 0x00, 0x0b, 0x57, 0xfe, 0x5b, 0x60, 0x00, 0x60, 0x00, 0xf3},
		PUSH1, 0x00, MSTORE, PUSH1, 0x11, PUSH1, 0x0f, PUSH1, 0x00, CREATE, STOP), 2000000},
}

func init() {
	// Every fused value as a raw code byte before the first jump, where the fast
	// path fetches it from the code itself and its case has to see through it,
	// and as the first byte of initcode, whose analysis is local to the frame.
	for op := fusedEscape; op < fusedEnd; op++ {
		name := fmt.Sprintf("fused-raw-value-%#x", byte(op))
		fusedDiffPrograms = append(fusedDiffPrograms, struct {
			name string
			code []byte
			gas  uint64
		}{name, asm(byte(op), STOP), 100000})
		fusedOutcomes[name] = struct{ ret, err string }{err: fmt.Sprintf("invalid opcode: opcode %#x not defined", byte(op))}

		// initcode = <op> STOP, stored right-aligned in memory word 0 and created
		// from offset 30, size 2. The create fails inside, the outer frame stops.
		name = fmt.Sprintf("fused-raw-in-initcode-%#x", byte(op))
		fusedDiffPrograms = append(fusedDiffPrograms, struct {
			name string
			code []byte
			gas  uint64
		}{name, asm(PUSH2, byte(op), 0x00, PUSH1, 0x00, MSTORE, PUSH1, 0x02, PUSH1, 0x1e, PUSH1, 0x00, CREATE, STOP), 2000000})
		fusedOutcomes[name] = struct{ ret, err string }{}
	}
	diffPrograms = append(diffPrograms, fusedDiffPrograms...)
}

// fusedOutcomes is what the canaries with a definite result must produce on
// the generated path: a returned word, or an error. A canary that reached the
// wrong branch would still pass the differential test if both loops agreed, so
// this pins the branch.
var fusedOutcomes = map[string]struct {
	ret string // hex of the 32-byte return, "" for no return data
	err string // substring of the error, "" for success
}{
	"fused-push2-jump":               {ret: "2a"},
	"fused-push2-jump-invalid":       {err: "invalid jump destination"},
	"fused-push2-jump-into-data":     {err: "invalid jump destination"},
	"fused-push2-jumpi-both":         {ret: "2a"},
	"fused-jumpi-empty-stack":        {err: "stack underflow (1 <=> 2)"},
	"fused-push2-jumpi-gas-27":       {err: "out of gas"},
	"fused-push2-jumpi-gas-28":       {err: "out of gas"},
	"fused-push2-jumpi-gas-29":       {},
	"fused-imm-contains-jump":        {},
	"fused-truncated-push2":          {},
	"fused-jump-loop-oog":            {err: "out of gas"},
	"fused-iszero-taken":             {ret: "2a"},
	"fused-iszero-not-taken":         {ret: "07"},
	"fused-iszero-invalid-dest":      {err: "invalid jump destination"},
	"fused-iszero-underflow":         {err: "stack underflow (0 <=> 1)"},
	"fused-lt-taken":                 {ret: "2a"},
	"fused-lt-not-taken":             {ret: "07"},
	"fused-lt-underflow":             {err: "stack underflow (1 <=> 2)"},
	"fused-selector-hit":             {ret: "deadbeef"},
	"fused-selector-miss":            {ret: "cafebabe"},
	"fused-selector-chain":           {ret: "deadbeef"},
	"fused-selector-underflow":       {err: "stack underflow (0 <=> 1)"},
	"fused-selector-wide-top":        {ret: "0100000000deadbeef"},
	"fused-shlsub-mask":              {ret: "ffffffffffffffffffffffffffffffffffffffff"},
	"fused-shlsub-wrap":              {ret: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff03"},
	"fused-shlsub-shift255":          {ret: "7fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"},
	"fused-shlsub-near-full":         {err: "stack limit reached 1024 (1023)"},
	"fused-push2-jumpi-full-stack":   {err: "stack limit reached 1024 (1023)"},
	"fused-raw-before-jump":          {err: "invalid opcode: opcode 0xc1 not defined"},
	"fused-raw-after-jump":           {err: "invalid opcode: opcode 0xc2 not defined"},
	"fused-raw-escape-value":         {err: "invalid opcode: opcode 0xc0 not defined"},
	"fused-raw-in-push-data":         {},
	"jump-underflow":                 {err: "stack underflow (0 <=> 1)"},
	"jump-landing-oog":               {err: "out of gas"},
	"jump-landing-paid":              {},
	"jumpi-landing-oog":              {err: "out of gas"},
	"jumpi-landing-paid":             {},
	"fused-push2-jump-landing-oog":   {err: "out of gas"},
	"fused-push2-jump-landing-paid":  {},
	"fused-push2-jumpi-invalid-dest": {err: "invalid jump destination"},
	"fused-iszero-landing-oog":       {err: "out of gas"},
	"fused-iszero-landing-paid":      {},
	"fused-lt-invalid-dest":          {err: "invalid jump destination"},
	"fused-lt-landing-oog":           {err: "out of gas"},
	"fused-lt-landing-paid":          {},
	"fused-selector-invalid-dest":    {err: "invalid jump destination"},
	"fused-selector-landing-oog":     {err: "out of gas"},
	"fused-selector-landing-paid":    {},
	"fused-dupn-immediate-is-head":   {},
	"fused-first-jump-unfused":       {},
	"jump-overflow-dest":             {err: "invalid jump destination"},
	"jumpi-overflow-dest":            {err: "invalid jump destination"},
	"fused-initcode":                 {},
}

func TestFusedOutcomes(t *testing.T) {
	// The Amsterdam lane, so DUPN is defined.
	fk := diffForks[len(diffForks)-1]
	for _, prog := range fusedDiffPrograms {
		want, ok := fusedOutcomes[prog.name]
		if !ok {
			t.Errorf("%s has no expected outcome", prog.name)
			continue
		}
		got := runOne(t, fk.cfg, fk.merged, false, prog.code, nil, prog.gas)
		if want.err != "" {
			if !strings.Contains(got.errStr, want.err) {
				t.Errorf("%s: error %q, want %q", prog.name, got.errStr, want.err)
			}
			continue
		}
		if got.errStr != "" {
			t.Errorf("%s: unexpected error %q", prog.name, got.errStr)
			continue
		}
		var wantRet []byte
		if want.ret != "" {
			wantRet = common.LeftPadBytes(common.FromHex(want.ret), 32)
		}
		if !bytes.Equal(got.ret, wantRet) {
			t.Errorf("%s: returned %x, want %x", prog.name, got.ret, wantRet)
		}
	}
}

// TestFuseCode checks the analysis pass: the shadow is the code except at the
// heads of matched sequences and at code bytes in the fused range, the bitmap is
// codeBitmap's, and the heads land where the canaries expect them.
func TestFuseCode(t *testing.T) {
	type head struct {
		pc uint64
		op OpCode
	}
	tests := []struct {
		name  string
		code  []byte
		heads []head
	}{
		{"push2-jump", fusedDiffPrograms[0].code, []head{{4, fusedPush2Jump}}},
		{"push2-jumpi-both", asm(warm, PUSH1, 0x00, PUSH2, 0x00, 0x1c, JUMPI, PUSH1, 0x01, PUSH2, 0x00, 0x11, JUMPI), []head{{6, fusedPush2Jumpi}, {12, fusedPush2Jumpi}}},
		{"iszero-triple", asm(warm, PUSH1, 0x00, ISZERO, PUSH2, 0x00, 0x0c, JUMPI), []head{{6, fusedIszeroPush2Jumpi}, {7, fusedPush2Jumpi}}},
		{"lt-quad", asm(warm, LT, ISZERO, PUSH2, 0x00, 0x00, JUMPI), []head{{4, fusedLtIszeroPush2Jumpi}, {5, fusedIszeroPush2Jumpi}, {6, fusedPush2Jumpi}}},
		{"selector", asm(DUP1, PUSH4, 1, 2, 3, 4, EQ, PUSH2, 0, 0, JUMPI), []head{{0, fusedSelector}, {7, fusedPush2Jumpi}}},
		{"shlsub", asm(PUSH1, 1, PUSH1, 1, PUSH1, 0xa0, SHL, SUB), []head{{0, fusedShlSubConst}}},
		{"imm-not-a-head", asm(PUSH2, 0x61, 0x57, POP, STOP), nil},
		{"truncated", asm(PUSH2, 0x00), nil},
		{"push2-then-end", asm(PUSH2, 0x00, 0x00), nil},
		{"escape", asm(0xc1, PUSH1, 0xc2, 0xc0), []head{{0, fusedEscape}, {3, fusedEscape}}},
		{"jumpdest-tail-not-fused", asm(PUSH2, 0x00, 0x00, JUMPDEST, JUMPI), nil},
	}
	for _, tt := range tests {
		a := analyzeCode(tt.code)
		if len(a) != len(tt.code)+len(tt.code)/8+1+4 {
			t.Fatalf("%s: packed length %d", tt.name, len(a))
		}
		if !bytes.Equal(a.bits(len(tt.code)), codeBitmap(tt.code)) {
			t.Errorf("%s: bitmap differs from codeBitmap", tt.name)
		}
		want := append([]byte(nil), tt.code...)
		for _, h := range tt.heads {
			want[h.pc] = byte(h.op)
		}
		if got := a.shadow(len(tt.code)); !bytes.Equal(got, want) {
			t.Errorf("%s: shadow\n got %x\nwant %x", tt.name, got, want)
		}
	}
}

// TestFusedValuesUndefined pins the fused range to bytes no fork defines, and
// keeps them out of the opcode string tables, since they are not opcodes.
func TestFusedValuesUndefined(t *testing.T) {
	for _, f := range GenForks() {
		for op := fusedEscape; op < fusedEnd; op++ {
			if f.Ops[op].Defined {
				t.Errorf("fork %s defines %#x, which the fused opcodes use", f.Name, byte(op))
			}
		}
	}
	for op := fusedEscape; op < fusedEnd; op++ {
		if s := op.String(); !strings.HasPrefix(s, "opcode ") {
			t.Errorf("%#x has an opcode name %q", byte(op), s)
		}
	}
	_ = params.StackLimit
}
