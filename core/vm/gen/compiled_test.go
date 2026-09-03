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
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// This file checks the compiled dispatch, which is the only place three of its
// properties are visible at all. The source cannot state any of them and the
// generator can only intend them.
//
// Two are about inlining. The dispatch calls its handlers by name, and their
// bodies are far past the compiler's inline budget, so they only come inline
// because the committed profile marks them hot. If the profile stops matching,
// every one of them silently becomes a real call and the switch loses most of
// what it was built for. Nothing fails: the build works and the tests pass.
//
// The third is the switch's lowering, and it is checked here for a reason worth
// stating. The generator already refuses to emit a switch that would compile to
// the wrong shape, but that guard tests a model of Go's rule, written down in
// lowering.go, not Go. If a toolchain bump moves the rule, or the model is
// wrong about a case nobody has written yet, the guard passes and the binary
// quietly gets the shape we measured as 8.7% slower, with CI green. So the
// generator's guard is where the mistake gets caught, and this is where the
// model gets caught being wrong.

// jumpRE pulls the operand off a jump. An indirect jump is how a jump table
// lowering shows itself, as "JMP (R27)" on arm64 or "JMP 0(R12)(R11*8)" on
// amd64, against the direct forms "JMP 0x4711", "JMP 12(PC)" and "JMP sym(SB)".
// Matching on what a direct jump looks like keeps this off the list of
// per-architecture spellings of an indirect one.
var (
	jumpRE   = regexp.MustCompile(`\bJMP\s+(\S+)`)
	directRE = regexp.MustCompile(`^(0x[0-9a-f]+|-?\d+\(PC\))$`)
)

// TestCompiledDispatch builds geth the way it ships and asserts the three
// things about the dispatch that only the binary can answer.
func TestCompiledDispatch(t *testing.T) {
	if testing.Short() {
		t.Skip("builds geth")
	}
	root := filepath.Join(vmDir(), "..", "..")
	bin := filepath.Join(t.TempDir(), "geth")

	// No -pgo flag. The point is the build everyone else gets, and -pgo=auto
	// finds cmd/geth/default.pgo on its own.
	build := exec.Command("go", "build", "-o", bin, "./cmd/geth")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building geth: %v\n%s", err, out)
	}
	dump, err := exec.Command("go", "tool", "objdump", "-s", dispatchFunc, bin).Output()
	if err != nil {
		t.Fatalf("disassembling %s: %v", dispatchFunc, err)
	}

	var handlers, stackCalls, indirect int
	for line := range strings.Lines(string(dump)) {
		if i := strings.Index(line, "CALL "); i >= 0 {
			switch callee := line[i+len("CALL "):]; {
			case strings.Contains(callee, vmPkgPath+".op"):
				handlers++
			case strings.Contains(callee, vmPkgPath+".(*Stack)"):
				stackCalls++
			}
		}
		if m := jumpRE.FindStringSubmatch(line); m != nil {
			if !directRE.MatchString(m[1]) && !strings.HasSuffix(m[1], "(SB)") {
				indirect++
			}
		}
	}

	if handlers != 0 {
		t.Errorf("%d opcode handlers are still calls in the compiled dispatch, so %s is not inlining them. "+
			"Run `go generate ./core/vm/...` and commit the profile it writes", handlers, pgoFile)
	}
	if stackCalls != 0 {
		t.Errorf("%d stack methods are still calls in the compiled dispatch, so the collapsed DUP and SWAP "+
			"cases are paying a call each. Run `go generate ./core/vm/...` and commit the profile it writes", stackCalls)
	}
	got := compareTree
	if indirect > 0 {
		got = jumpTable
	}
	if got != wantLowering {
		t.Errorf("the compiled dispatch has %d indirect jumps, so it is %s, where lowering.go intends %s. "+
			"Generation checks a model of Go's rule rather than Go itself, so that model is now wrong: "+
			"check minCases and minDensity against tryJumpTable in cmd/compile/internal/walk/switch.go",
			indirect, got, wantLowering)
	}
}
