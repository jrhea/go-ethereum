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

// Fused opcodes stand for a short sequence of real opcodes that the generated
// fast path runs as one step. The code analysis finds the sequences once per
// code hash and writes the fused value over the first opcode of each, in a
// shadow copy of the code that only the fast path fetches opcodes from. The
// code itself is untouched: immediates, jump destination checks, CODECOPY,
// tracers and the table loop all read the original bytes, so nothing outside
// the fast path can observe a fused value. The values sit in a range no fork
// has assigned an opcode to; the generator checks that against the tables.
const (
	fusedEscape             OpCode = 0xc0 // the code byte is itself in the fused range: dispatch it from the code
	fusedPush2Jump          OpCode = 0xc1 // PUSH2 dest JUMP
	fusedPush2Jumpi         OpCode = 0xc2 // PUSH2 dest JUMPI
	fusedIszeroPush2Jumpi   OpCode = 0xc3 // ISZERO PUSH2 dest JUMPI, the branch on a false condition
	fusedLtIszeroPush2Jumpi OpCode = 0xc4 // LT ISZERO PUSH2 dest JUMPI, the loop exit test
	fusedSelector           OpCode = 0xc5 // DUP1 PUSH4 sel EQ PUSH2 dest JUMPI, one arm of the function dispatcher
	fusedShlSubConst        OpCode = 0xc6 // PUSH1 x PUSH1 y PUSH1 z SHL SUB, the (y<<z)-x mask constant
	fusedEnd                OpCode = 0xc7 // one past the last fused value
)

// fusedPattern is the opcode sequence one fused opcode stands for. A PUSH in the
// sequence carries its immediates, which the opcode implies.
type fusedPattern struct {
	op   OpCode
	name string // the Go identifier of op, which the generator writes as the case label
	ops  []OpCode
}

// fusedPatterns lists every fused opcode with its sequence. The analysis tries
// them longest first at each opcode boundary, so a sequence that starts inside a
// longer one is only fused where the longer one does not match.
var fusedPatterns = []fusedPattern{
	{fusedSelector, "fusedSelector", []OpCode{DUP1, PUSH4, EQ, PUSH2, JUMPI}},
	{fusedShlSubConst, "fusedShlSubConst", []OpCode{PUSH1, PUSH1, PUSH1, SHL, SUB}},
	{fusedLtIszeroPush2Jumpi, "fusedLtIszeroPush2Jumpi", []OpCode{LT, ISZERO, PUSH2, JUMPI}},
	{fusedIszeroPush2Jumpi, "fusedIszeroPush2Jumpi", []OpCode{ISZERO, PUSH2, JUMPI}},
	{fusedPush2Jump, "fusedPush2Jump", []OpCode{PUSH2, JUMP}},
	{fusedPush2Jumpi, "fusedPush2Jumpi", []OpCode{PUSH2, JUMPI}},
}

// fusedByHead indexes the patterns by their first opcode, longest first, so the
// analysis tests one opcode boundary against only the patterns that can start
// there.
var fusedByHead = func() (idx [256][]fusedPattern) {
	for _, f := range fusedPatterns {
		head := f.ops[0]
		idx[head] = append(idx[head], f)
	}
	for op := range idx {
		ps := idx[op]
		for i := 1; i < len(ps); i++ {
			if len(ps[i].ops) > len(ps[i-1].ops) {
				panic("fusedPatterns must be listed longest first")
			}
		}
	}
	return idx
}()

// isFused reports whether a byte is a fused opcode value.
func isFused(op OpCode) bool {
	return op >= fusedEscape && op < fusedEnd
}

// fuseCode writes the fused opcodes into shadow, which starts as a copy of code.
// It walks the opcode boundaries exactly as codeBitmapInternal does, so a fused
// value only ever lands where execution can dispatch an opcode and never over
// an immediate. An opcode boundary whose own byte is in the fused range gets the
// escape, so an undefined opcode there still reports itself.
func fuseCode(code, shadow []byte) {
	for pc := 0; pc < len(code); {
		op := OpCode(code[pc])
		if isFused(op) {
			shadow[pc] = byte(fusedEscape)
		} else if f, ok := matchFused(code, pc); ok {
			shadow[pc] = byte(f.op)
		}
		pc++
		if op >= PUSH1 && op <= PUSH32 {
			pc += int(op - PUSH1 + 1)
		}
	}
}

// matchFused returns the longest fused pattern whose sequence starts at pc.
func matchFused(code []byte, pc int) (fusedPattern, bool) {
	for _, f := range fusedByHead[code[pc]] {
		if fusedMatches(code, pc, f.ops) {
			return f, true
		}
	}
	return fusedPattern{}, false
}

// fusedMatches reports whether the sequence ops starts at pc, each member at
// the boundary the one before it implies. A PUSH whose immediates run past the
// end of the code has no following member, so nothing matches past it.
func fusedMatches(code []byte, pc int, ops []OpCode) bool {
	for _, want := range ops {
		if pc >= len(code) || OpCode(code[pc]) != want {
			return false
		}
		pc++
		if want >= PUSH1 && want <= PUSH32 {
			pc += int(want - PUSH1 + 1)
		}
	}
	return true
}
