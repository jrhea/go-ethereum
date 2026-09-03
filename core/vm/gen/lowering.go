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
	"go/ast"
	"go/parser"
	"go/token"
)

// This file works out which of the two shapes Go will compile the opcode switch
// into, and stops generation if it is not the intended one.
//
// A switch on an integer becomes either a jump table, which is a range check
// and one indirect jump, or a binary search over conditional compares. The
// language gives no way to ask for one. The compiler decides from the number of
// case clauses and the span of their values, and the choice is worth a great
// deal here: measured on 100 consecutive mainnet blocks, at identical opcode
// coverage, the compare tree ran 8.7% faster than the jump table.
//
// That inverts the usual advice, and the workload is why. A single indirect
// jump on a stream of EVM opcodes is close to unpredictable, so it mispredicts
// on a large share of dispatches. The compare tree runs more instructions, but
// every branch in it is heavily biased by the opcode distribution, so they
// predict well and the pipeline keeps moving.
//
// The catch is how narrowly the choice gets decided. Folding DUP and SWAP into
// parametric cases took the clause count from 60 to 39, which left the switch
// four units inside the jump table's density bound. Widening it by the seven
// values of DUP15, DUP16 and SWAP10 to SWAP16 pushed it three units past, and
// the lowering flipped. Nothing in the source says any of that, and one opcode
// added to hotOps later would flip it back and look like a 9% regression with
// no visible cause. So the generator checks the shape it is about to get.

// lowering is a shape the compiler can give the dispatch switch.
type lowering int

const (
	jumpTable   lowering = iota // range check, then one indirect jump
	compareTree                 // binary search over conditional compares
)

func (l lowering) String() string {
	if l == jumpTable {
		return "a jump table"
	}
	return "a compare tree"
}

// wantLowering is the shape the dispatch is meant to compile to. The note above
// has the measurement behind the choice.
const wantLowering = compareTree

// Go's rule, from tryJumpTable in cmd/compile/internal/walk/switch.go: a jump
// table when there are at least minCases clauses and the span of the case
// values is no more than minDensity times the clause count. These are compiler
// internals and can move, which is the other reason to check the result rather
// than assume it. If a toolchain bump changes them, this reports a switch that
// no longer compiles the way it reads.
const (
	minCases   = 8
	minDensity = 4
)

// switchLowering returns the shape the compiler will give the dispatch switch,
// along with the clause count and value span it decides from.
//
// The clause count is not the number of case clauses in the source. Go flattens
// each case to one clause per value, sorts them, and merges runs of consecutive
// values that share a body, so a folded family of sixteen opcodes counts once.
// Merging only ever joins values from the same clause, because two clauses jump
// to two labels however alike their bodies look.
func (g *generator) switchLowering(src []byte) (l lowering, clauses, width int) {
	byName := make(map[string]int, 256)
	for code := range 256 {
		if spec := g.specs[code]; spec.Defined {
			byName[spec.Name] = code
		}
	}

	owner := map[int]int{} // opcode value -> index of the clause handling it
	for i, vals := range g.switchCases(src) {
		for _, name := range vals {
			code, ok := byName[name]
			if !ok {
				abortf("the dispatch has a case for %q, which is not an opcode any fork defines", name)
			}
			owner[code] = i
		}
	}
	if len(owner) == 0 {
		abortf("found no cases in the dispatch switch, so its lowering cannot be worked out")
	}

	lo, hi := 256, -1
	for code := range owner {
		lo, hi = min(lo, code), max(hi, code)
	}
	// Walk the values in order, counting maximal runs that stay in one clause.
	for code, prev := lo, -2; code <= hi; code++ {
		i, ok := owner[code]
		if !ok {
			prev = -2
			continue
		}
		if p, was := owner[prev]; !was || p != i {
			clauses++
		}
		prev = code
	}

	width = hi - lo + 1
	if clauses >= minCases && width <= clauses*minDensity {
		return jumpTable, clauses, width
	}
	return compareTree, clauses, width
}

// switchCases returns the opcode names in each case clause of the dispatch
// switch, in source order, skipping the default. It reads the formatted output
// for the same reason callSites does, which is that this is the file that ships.
func (g *generator) switchCases(src []byte) [][]string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, generatedFile, src, 0)
	if err != nil {
		abortf("parsing the generated dispatch to find its cases: %v", err)
	}
	var out [][]string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Name.Name != dispatchFunc {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			clause, ok := n.(*ast.CaseClause)
			if !ok || clause.List == nil { // a nil List is the default case
				return true
			}
			vals := make([]string, 0, len(clause.List))
			for _, e := range clause.List {
				id, ok := e.(*ast.Ident)
				if !ok {
					abortf("the dispatch has a case label that is not a plain opcode name, so its lowering cannot be worked out")
				}
				vals = append(vals, id.Name)
			}
			out = append(out, vals)
			return true
		})
	}
	return out
}

// checkLowering stops generation if the switch would not compile to the shape
// it is meant to. The message carries the margin, because how far the change
// moved it, and which way, is the useful thing to know when this trips.
func (g *generator) checkLowering(src []byte) {
	got, clauses, width := g.switchLowering(src)
	if got == wantLowering {
		return
	}
	fix := fmt.Sprintf("widen the span past %d, or fold cases together until there are fewer than %d clauses",
		clauses*minDensity, (width+minDensity-1)/minDensity)
	if wantLowering == jumpTable {
		fix = fmt.Sprintf("narrow the span to %d or less, or split cases apart until there are at least %d clauses",
			clauses*minDensity, (width+minDensity-1)/minDensity)
	}
	abortf("the dispatch switch would compile to %s, not %s: %d clauses spanning %d opcode values, "+
		"and Go takes a jump table when the span is at most %d times the clauses. To keep %s, %s",
		got, wantLowering, clauses, width, minDensity, wantLowering, fix)
}
