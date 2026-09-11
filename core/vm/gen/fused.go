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

package main

import (
	"fmt"

	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
)

// This file emits the cases that run more than one opcode per dispatch: the
// jumps, which land past the JUMPDEST they are certain to find, and the fused
// opcodes, which the code analysis writes over known opcode sequences (see
// fusedops.go in core/vm).
//
// A fused case runs its whole sequence when the sequential run would have
// succeeded, and otherwise hands the head opcode back to the loop so the
// sequence runs one opcode at a time with its own errors. That keeps every
// error message and every gas trajectory the sequential one, with nothing
// restated here: the fused case only ever takes the path where all the
// sequence's guards pass, and the combined gas charge is atomic, so a failed
// charge leaves the budget for the sequential run to charge in order.

// emitJumpCase emits JUMP or JUMPI by hand rather than through opJump and
// opJumpi. The flow is the handlers', plus one step: a taken jump has just
// proved through validJumpdest that the byte at its destination is JUMPDEST,
// whose whole effect is one unit of gas, so the jump charges that unit and
// lands on the opcode after it. Every taken jump saves the JUMPDEST dispatch,
// whatever put the destination on the stack.
func (g *generator) emitJumpCase(code byte) {
	spec := g.specs[code]
	if g.tierOf(code) != tierStatic {
		abortf("opcode %#x (%s) must be on the static tier to be emitted by hand", code, spec.Name)
	}
	g.p("case %s:\n", spec.Name)
	g.emitStackChecks(spec.stackGuards())
	g.emitStaticGas(spec.ConstantGas)
	g.emitAbortCheck()
	if code == byte(vm.JUMP) {
		g.p("pos := stack.pop1()\n")
		g.emitStackStep(spec.stackDelta())
		g.emitLandOnStack("pos")
		return
	}
	g.p("pos, cond := stack.pop2()\n")
	g.emitStackStep(spec.stackDelta())
	g.p("if !cond.IsZero() {\n")
	g.emitLandOnStack("pos")
	g.p("}\n")
	g.emitAdvance()
}

// emitAbortCheck emits the abort test the jump handlers run before they pop.
func (g *generator) emitAbortCheck() {
	g.p(`
		if evm.abort.Load() {
			res, err = nil, errStopToken
			break mainLoop
		}
	`)
}

// emitLandOnStack validates a destination popped off the stack with the checks
// validJumpdest makes, then lands past its JUMPDEST.
func (g *generator) emitLandOnStack(pos string) {
	g.p("udest, overflow := %s.Uint64WithOverflow()\n", pos)
	g.p(`
		if overflow {
			res, err = nil, ErrInvalidJump
			break mainLoop
		}
	`)
	g.emitLandAt("udest")
}

// emitLandAt validates a destination with the checks validJumpdest makes, then
// lands past its JUMPDEST. The JUMPDEST byte is read from the fetch array, not
// the code: the fast path reads nothing but that array and the stack, so the
// code bytes are not pulled into the cache a second time. The two agree at
// every JUMPDEST, since the analysis never writes over one, and the array is
// the code itself until the analysis is resolved.
func (g *generator) emitLandAt(udest string) {
	g.p(`
		if %[1]s >= uint64(len(contract.ops)) || OpCode(contract.ops[%[1]s]) != JUMPDEST || !contract.isCode(%[1]s) {
			res, err = nil, ErrInvalidJump
			break mainLoop
		}
	`, udest)
	g.emitLand(udest)
}

// emitLand charges the landing JUMPDEST's gas and continues at the opcode after
// it. The destination has been validated, so the byte there is JUMPDEST and
// this is the sequential run's next step, minus its dispatch.
func (g *generator) emitLand(udest string) {
	g.emitStaticGas(g.specs[byte(vm.JUMPDEST)].ConstantGas)
	g.p(`
		pc = %s + 1
		op = contract.GetFastOp(pc)
		continue mainLoop
	`, udest)
}

// emitFusedOps emits one case per fused opcode, then the escape, which has to
// be the clause right before default because it falls through into it.
func (g *generator) emitFusedOps() {
	fused := vm.GenFusedOps()
	for _, f := range fused {
		if g.specs[byte(f.Op)].Defined {
			abortf("fused opcode %s uses byte %#x, which fork tables define as %s", f.Name, byte(f.Op), g.specs[byte(f.Op)].Name)
		}
		if g.tierOf(byte(f.Op)) != tierTable {
			abortf("fused opcode %s uses byte %#x, which is in hotOps", f.Name, byte(f.Op))
		}
	}
	for _, f := range fused {
		if f.Ops == nil {
			continue
		}
		g.emitFusedOp(f)
	}
	for _, f := range fused {
		if f.Ops == nil {
			g.emitEscape(f)
		}
	}
}

// fusedFacts returns what a fused case checks up front, derived from the
// members' specs: the combined constant gas, the window of stack depths at which
// every member's own guards pass, the net stack delta, and the fork gates of
// any member that a fork introduced. Each member's guards apply at the depth
// the members before it leave behind.
func (g *generator) fusedFacts(f vm.GenFusedOp) (gas uint64, lo, hi, delta int, gates []string) {
	hi = int(params.StackLimit)
	seen := map[string]bool{}
	for _, op := range f.Ops {
		spec := g.specs[byte(op)]
		if g.tierOf(byte(op)) != tierStatic {
			abortf("fused opcode %s contains %s, which is not on the static tier, so its gas cannot be combined", f.Name, op)
		}
		if spec.fork != "" && !seen[spec.fork] {
			seen[spec.fork] = true
			gates = append(gates, spec.fork)
		}
		gas += spec.ConstantGas
		lo = max(lo, spec.MinStack-delta)
		hi = min(hi, spec.MaxStack-delta)
		delta += spec.stackDelta()
	}
	return gas, lo, hi, delta, gates
}

// emitFusedGuard opens a fused case. The first test catches a byte in the fused
// range that is really in the code: before a frame's first jump the fast path
// still fetches from the code itself, so such a byte arrives here rather than
// at the table, and it has to fail as the undefined opcode it is. A fused byte
// is genuine exactly when the analysis is installed, since that is what swaps
// the shadow in. Then the members' guards, combined: a member's fork not yet
// active, a depth outside the window, or short of the combined gas, and the
// head opcode goes back round the loop so the sequence runs one opcode at a
// time, which produces the sequential error in the sequential place.
func (g *generator) emitFusedGuard(f vm.GenFusedOp, gas uint64, lo, hi int, gates []string) {
	head := g.specs[byte(f.Ops[0])].Name
	g.p(`
		case %s:
			if contract.analysis == nil {
				res, err = opUndefined(&pc, evm, scope)
				break mainLoop
			}
	`, f.Name)
	cond := ""
	for _, gate := range gates {
		cond += fmt.Sprintf("!rules.%s || ", gate)
	}
	if lo > 0 {
		cond += fmt.Sprintf("sp < %d || ", lo)
	}
	if hi < int(params.StackLimit) {
		cond += fmt.Sprintf("sp > %d || ", hi)
	}
	g.p(`
		if %s!contract.Gas.ChargeExecutionOnly(%d) {
			op = %s
			continue mainLoop
		}
	`, cond, gas, head)
}

// fusedLen is the number of code bytes a fused sequence covers.
func fusedLen(ops []vm.OpCode) int {
	n := 0
	for _, op := range ops {
		n++
		if op >= vm.PUSH1 && op <= vm.PUSH32 {
			n += int(op-vm.PUSH1) + 1
		}
	}
	return n
}

// emitFusedOp emits the case for one fused opcode. The bodies are written per
// sequence: each reads its immediates at the offsets the sequence fixes, and
// each step is what the sequential run would have done with its intermediate
// values kept off the stack. Immediates are read from the fetch array, which
// holds them verbatim: the analysis writes only over the first opcode of a
// sequence it matched, and its own walk is what said those bytes are
// immediates.
func (g *generator) emitFusedOp(f vm.GenFusedOp) {
	gas, lo, hi, delta, gates := g.fusedFacts(f)
	g.emitFusedGuard(f, gas, lo, hi, gates)
	skip := fusedLen(f.Ops)
	switch f.Op {
	case 0xc1: // PUSH2 dest JUMP
		g.emitAbortCheck()
		g.p("udest := uint64(contract.ops[pc+1])<<8 | uint64(contract.ops[pc+2])\n")
		g.emitStackStep(delta)
		g.emitLandAt("udest")

	case 0xc2: // PUSH2 dest JUMPI: the destination never touches the stack
		g.emitAbortCheck()
		g.p("cond := stack.pop1()\n")
		g.emitStackStep(delta)
		g.p("if !cond.IsZero() {\n")
		g.p("udest := uint64(contract.ops[pc+1])<<8 | uint64(contract.ops[pc+2])\n")
		g.emitLandAt("udest")
		g.p("}\n")
		g.emitSkip(skip)

	case 0xc3: // ISZERO PUSH2 dest JUMPI: jump when the operand is zero
		g.emitAbortCheck()
		g.p("x := stack.pop1()\n")
		g.emitStackStep(delta)
		g.p("if x.IsZero() {\n")
		g.p("udest := uint64(contract.ops[pc+2])<<8 | uint64(contract.ops[pc+3])\n")
		g.emitLandAt("udest")
		g.p("}\n")
		g.emitSkip(skip)

	case 0xc4: // LT ISZERO PUSH2 dest JUMPI: jump when top < second is false
		g.emitAbortCheck()
		g.p("x, y := stack.pop2()\n")
		g.emitStackStep(delta)
		g.p("if !x.Lt(y) {\n")
		g.p("udest := uint64(contract.ops[pc+3])<<8 | uint64(contract.ops[pc+4])\n")
		g.emitLandAt("udest")
		g.p("}\n")
		g.emitSkip(skip)

	case 0xc5: // DUP1 PUSH4 sel EQ PUSH2 dest JUMPI: jump when the top equals the selector, top stays
		g.emitAbortCheck()
		g.p("sel := stack.peek()\n")
		g.emitStackStep(delta)
		g.p("if sel.IsUint64() && sel.Uint64() == uint64(contract.ops[pc+2])<<24|uint64(contract.ops[pc+3])<<16|uint64(contract.ops[pc+4])<<8|uint64(contract.ops[pc+5]) {\n")
		g.p("udest := uint64(contract.ops[pc+8])<<8 | uint64(contract.ops[pc+9])\n")
		g.emitLandAt("udest")
		g.p("}\n")
		g.emitSkip(skip)

	case 0xc6: // PUSH1 x PUSH1 y PUSH1 z SHL SUB: push (y<<z)-x. z is one byte, so the shift is below 256.
		g.p(`
			v := stack.get()
			v.SetUint64(uint64(contract.ops[pc+3]))
			v.Lsh(v, uint(contract.ops[pc+5]))
			v.SubUint64(v, uint64(contract.ops[pc+1]))
		`)
		g.emitStackStep(delta)
		g.emitSkip(skip)

	default:
		abortf("fused opcode %s (%#x) has no emitter", f.Name, byte(f.Op))
	}
}

// emitSkip continues at the opcode after a fused sequence of n code bytes.
func (g *generator) emitSkip(n int) {
	g.p(`
		pc += %d
		op = contract.GetFastOp(pc)
		continue mainLoop
	`, n)
}

// emitEscape emits the case for a code byte that is itself in the fused
// range. The analysis writes the escape over it so it cannot be mistaken for a
// fused opcode; here it is dispatched as what it is, through the table, which
// reports it as the undefined opcode it is.
func (g *generator) emitEscape(f vm.GenFusedOp) {
	g.p(`
		case %s:
			op = OpCode(contract.Code[pc])
			fallthrough
	`, f.Name)
}
