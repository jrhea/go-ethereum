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

package gevmbridge

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/holiman/uint256"

	gevmstate "github.com/Giulio2002/gevm/state"
	gevmtypes "github.com/Giulio2002/gevm/types"
)

// storageRooter is the optional subset of *state.StateDB used to answer
// HasStorage. The vm.StateDB interface doesn't expose the storage root, so we
// type-assert for it; every real backend (*state.StateDB) implements it.
type storageRooter interface {
	GetStorageRoot(common.Address) common.Hash
}

// dbAdapter presents geth's vm.StateDB to gevm through gevm's read-only
// state.Database interface. gevm journals every write internally against this
// read-through view; the writes are mirrored back into the StateDB after
// execution (see writeBack). Because geth and gevm share the same
// holiman/uint256 and common.Address/Hash are byte-identical to gevm's
// types.Address/B256, all conversions here are free.
type dbAdapter struct {
	sdb     vm.StateDB
	getHash vm.GetHashFunc // block hash provider from the block context (may be nil)
}

// compile-time check that dbAdapter satisfies gevm's Database interface.
var _ gevmstate.Database = (*dbAdapter)(nil)

// Basic returns the account's balance, nonce and code hash. When the account
// doesn't exist gevm substitutes its own default (empty) info, so the returned
// AccountInfo is only consulted when the bool is true.
func (a *dbAdapter) Basic(address gevmtypes.Address) (gevmstate.AccountInfo, bool, error) {
	addr := common.Address(address)
	if !a.sdb.Exist(addr) {
		return gevmstate.AccountInfo{}, false, nil
	}
	return gevmstate.AccountInfo{
		Balance:  *a.sdb.GetBalance(addr),
		Nonce:    a.sdb.GetNonce(addr),
		CodeHash: gevmtypes.B256(a.sdb.GetCodeHash(addr)),
	}, true, nil
}

// CodeByHash is unused by gevm's execution paths (code is always resolved by
// address via Code); return empty rather than error so a stray call is benign.
func (a *dbAdapter) CodeByHash(codeHash gevmtypes.B256) (gevmtypes.Bytes, error) {
	return nil, nil
}

// Code returns the account's bytecode.
func (a *dbAdapter) Code(address gevmtypes.Address) (gevmtypes.Bytes, error) {
	return a.sdb.GetCode(common.Address(address)), nil
}

// Storage returns the value stored at index for the given account.
func (a *dbAdapter) Storage(address gevmtypes.Address, index uint256.Int) (uint256.Int, error) {
	key := common.Hash(index.Bytes32())
	val := a.sdb.GetState(common.Address(address), key)
	var out uint256.Int
	out.SetBytes32(val[:])
	return out, nil
}

// HasStorage reports whether the account has any non-empty storage (EIP-7610
// create-collision detection). Falls back to false if the backend can't answer.
func (a *dbAdapter) HasStorage(address gevmtypes.Address) (bool, error) {
	if sr, ok := a.sdb.(storageRooter); ok {
		root := sr.GetStorageRoot(common.Address(address))
		return root != (common.Hash{}) && root != types.EmptyRootHash, nil
	}
	return false, nil
}

// BlockHash returns the hash of the given block number via the block context's
// GetHash function.
func (a *dbAdapter) BlockHash(number uint64) (gevmtypes.B256, error) {
	if a.getHash == nil {
		return gevmtypes.B256{}, nil
	}
	return gevmtypes.B256(a.getHash(number)), nil
}
