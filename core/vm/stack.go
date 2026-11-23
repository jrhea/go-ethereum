// Copyright 2014 The go-ethereum Authors
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
	"slices"
	"sync"

	"github.com/holiman/uint256"
)

// stackArena is an arena which actual evm stacks use for data storage
type stackArena struct {
	data []uint256.Int
	cur  *Stack // the innermost live stack, whose top is where the next one starts
}

func newArena() *stackArena {
	return stackPool.Get().(*stackArena)
}

// 1025, because in stack() there is a condition check
// for the stack size that would fail if it was set to
// 1024.
const initialStackSize = 1025

var stackPool = sync.Pool{
	New: func() any {
		return &stackArena{
			data: make([]uint256.Int, initialStackSize),
		}
	},
}

func returnStack(arena *stackArena) {
	arena.cur = nil // defensive, not strictly needed as release() pops back to nil
	stackPool.Put(arena)
}

// stack returns an instance of a stack which uses the underlying arena. The instance
// must be released by invoking the (*Stack).release() method
func (sa *stackArena) stack() *Stack {
	top := 0
	if sa.cur != nil {
		top = sa.cur.bottom + sa.cur.size
	}
	// make sure every substack has at least 1024 elements
	if len(sa.data) <= top+1024 {
		// we need to grow the arena
		sa.data = slices.Grow(sa.data, 1024)
		sa.data = sa.data[:cap(sa.data)]
	}
	// A stack keeps the slice it was handed even if a later frame grows the
	// arena. Frames never read each other's region, so the copy left behind in
	// the old backing array is still the one this stack owns.
	s := &Stack{
		data:   (*[1024]uint256.Int)(sa.data[top : top+1024]),
		bottom: top,
		parent: sa.cur,
		inner:  sa,
	}
	sa.cur = s
	return s
}

// newStackForTesting is meant to be used solely for testing. It creates a stack
// backed by a newly allocated arena.
func newStackForTesting() *Stack {
	arena := &stackArena{
		data: make([]uint256.Int, 1025),
	}
	return arena.stack()
}

// Stack is an object for basic stack operations. Items popped to the stack are
// expected to be changed and modified. stack does not take care of adding newly
// initialized objects.
//
// Indexes are masked to the window so the compiler can drop their bounds checks.
// The interpreter checks the depth before every operation, so a mask never wraps.
type Stack struct {
	data   *[1024]uint256.Int // this stack's 1024 slots in the arena
	size   int                // size is the number of elements in this stack
	bottom int                // bottom is the index of the first element of this stack in the arena
	parent *Stack             // the stack of the calling frame, nil for the outermost
	inner  *stackArena
}

// release un-claims the area of the arena which was claimed by the stack.
func (s *Stack) release() {
	s.inner.cur = s.parent
}

// Data returns the underlying uint256.Int array.
func (s *Stack) Data() []uint256.Int {
	return s.data[:s.size]
}

func (s *Stack) push(d *uint256.Int) {
	elem := s.get()
	*elem = *d
}

// get returns a pointer to a newly created element
// on top of the stack
func (s *Stack) get() *uint256.Int {
	elem := &s.data[(s.size)&1023]
	s.size++
	return elem
}

func (s *Stack) pop() uint256.Int {
	s.size--
	return s.data[(s.size)&1023]
}

func (s *Stack) len() int {
	return s.size
}

// drop removes the top element without reading it.
func (s *Stack) drop() {
	s.size--
}

// pop1 removes the top element and returns a pointer to it. The pointer
// stays valid only until the next push or sub call.
func (s *Stack) pop1() *uint256.Int {
	s.size--
	return &s.data[(s.size)&1023]
}

// pop2 removes the top two elements and returns pointers to them. The
// pointers stay valid only until the next push or sub call.
func (s *Stack) pop2() (top, second *uint256.Int) {
	s.size -= 2
	return &s.data[(s.size+1)&1023], &s.data[(s.size)&1023]
}

// pop3 removes the top three elements and returns pointers to them. The
// pointers stay valid only until the next push or sub call.
func (s *Stack) pop3() (top, second, third *uint256.Int) {
	s.size -= 3
	return &s.data[(s.size+2)&1023], &s.data[(s.size+1)&1023], &s.data[(s.size)&1023]
}

// pop4 removes the top four elements and returns pointers to them. The
// pointers stay valid only until the next push or sub call.
func (s *Stack) pop4() (top, second, third, fourth *uint256.Int) {
	s.size -= 4
	return &s.data[(s.size+3)&1023], &s.data[(s.size+2)&1023], &s.data[(s.size+1)&1023], &s.data[(s.size)&1023]
}

// pop1Peek1 removes the top element and returns pointers to it and to the new
// top, the usual operand and write target of a binary operation. The first
// pointer stays valid only until the next push or sub call.
func (s *Stack) pop1Peek1() (top, rest *uint256.Int) {
	s.size--
	return &s.data[(s.size)&1023], &s.data[(s.size-1)&1023]
}

// pop2Peek1 removes the top two elements and returns pointers to them and to
// the new top, for three operand operations. The first two pointers stay
// valid only until the next push or sub call.
func (s *Stack) pop2Peek1() (top, second, rest *uint256.Int) {
	s.size -= 2
	return &s.data[(s.size+1)&1023], &s.data[(s.size)&1023], &s.data[(s.size-1)&1023]
}

func (s *Stack) swap1() {
	s.data[(s.size-2)&1023], s.data[(s.size-1)&1023] = s.data[(s.size-1)&1023], s.data[(s.size-2)&1023]
}
func (s *Stack) swap2() {
	s.data[(s.size-3)&1023], s.data[(s.size-1)&1023] = s.data[(s.size-1)&1023], s.data[(s.size-3)&1023]
}
func (s *Stack) swap3() {
	s.data[(s.size-4)&1023], s.data[(s.size-1)&1023] = s.data[(s.size-1)&1023], s.data[(s.size-4)&1023]
}
func (s *Stack) swap4() {
	s.data[(s.size-5)&1023], s.data[(s.size-1)&1023] = s.data[(s.size-1)&1023], s.data[(s.size-5)&1023]
}
func (s *Stack) swap5() {
	s.data[(s.size-6)&1023], s.data[(s.size-1)&1023] = s.data[(s.size-1)&1023], s.data[(s.size-6)&1023]
}
func (s *Stack) swap6() {
	s.data[(s.size-7)&1023], s.data[(s.size-1)&1023] = s.data[(s.size-1)&1023], s.data[(s.size-7)&1023]
}
func (s *Stack) swap7() {
	s.data[(s.size-8)&1023], s.data[(s.size-1)&1023] = s.data[(s.size-1)&1023], s.data[(s.size-8)&1023]
}
func (s *Stack) swap8() {
	s.data[(s.size-9)&1023], s.data[(s.size-1)&1023] = s.data[(s.size-1)&1023], s.data[(s.size-9)&1023]
}
func (s *Stack) swap9() {
	s.data[(s.size-10)&1023], s.data[(s.size-1)&1023] = s.data[(s.size-1)&1023], s.data[(s.size-10)&1023]
}
func (s *Stack) swap10() {
	s.data[(s.size-11)&1023], s.data[(s.size-1)&1023] = s.data[(s.size-1)&1023], s.data[(s.size-11)&1023]
}
func (s *Stack) swap11() {
	s.data[(s.size-12)&1023], s.data[(s.size-1)&1023] = s.data[(s.size-1)&1023], s.data[(s.size-12)&1023]
}
func (s *Stack) swap12() {
	s.data[(s.size-13)&1023], s.data[(s.size-1)&1023] = s.data[(s.size-1)&1023], s.data[(s.size-13)&1023]
}
func (s *Stack) swap13() {
	s.data[(s.size-14)&1023], s.data[(s.size-1)&1023] = s.data[(s.size-1)&1023], s.data[(s.size-14)&1023]
}
func (s *Stack) swap14() {
	s.data[(s.size-15)&1023], s.data[(s.size-1)&1023] = s.data[(s.size-1)&1023], s.data[(s.size-15)&1023]
}
func (s *Stack) swap15() {
	s.data[(s.size-16)&1023], s.data[(s.size-1)&1023] = s.data[(s.size-1)&1023], s.data[(s.size-16)&1023]
}
func (s *Stack) swap16() {
	s.data[(s.size-17)&1023], s.data[(s.size-1)&1023] = s.data[(s.size-1)&1023], s.data[(s.size-17)&1023]
}

// swap exchanges the top of the stack with the n'th item below it. The generated
// dispatch calls this in place of swap1 through swap16, whose n is baked in.
func (s *Stack) swap(n int) {
	top := s.size - 1
	s.data[(top-n)&1023], s.data[(top)&1023] = s.data[(top)&1023], s.data[(top-n)&1023]
}

func (s *Stack) dup(n int) {
	s.data[(s.size)&1023] = s.data[(s.size-n)&1023]
	s.size++
}

func (s *Stack) peek() *uint256.Int {
	return &s.data[(s.size-1)&1023]
}

// back returns the n'th item in stack
func (s *Stack) back(n int) *uint256.Int {
	return &s.data[(s.size-n-1)&1023]
}
