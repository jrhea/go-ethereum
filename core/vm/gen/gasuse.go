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
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// gasUsers returns the opcode functions in core/vm whose body touches a Gas
// field. The dispatch keeps execution gas in a local, so it settles the frame's
// gas before calling one of these and picks it up again after.
func gasUsers() map[string]bool {
	entries, err := os.ReadDir(vmDir())
	if err != nil {
		abortf("reading core/vm: %v", err)
	}
	fset := token.NewFileSet()
	users := make(map[string]bool)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == generatedFile {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(vmDir(), name), nil, 0)
		if err != nil {
			abortf("parsing %s: %v", name, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "Gas" {
					users[fn.Name.Name] = true
				}
				return true
			})
		}
	}
	return users
}
