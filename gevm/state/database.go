package state

import (
	"github.com/Giulio2002/gevm/types"
	"github.com/holiman/uint256"
)

// Database is the EVM database interface for accessing state.
type Database interface {
	// Basic gets basic account information.
	// Returns (info, true, nil) if account exists, (zero, false, nil) if not.
	Basic(address types.Address) (AccountInfo, bool, error)

	// CodeByHash gets account code by its hash.
	CodeByHash(codeHash types.B256) (types.Bytes, error)

	// Code gets account code by address.
	Code(address types.Address) (types.Bytes, error)

	// Storage gets storage value of address at index.
	Storage(address types.Address, index uint256.Int) (uint256.Int, error)

	// HasStorage returns true if the account has any non-empty storage in the DB.
	// Used by EIP-7610 create collision detection.
	HasStorage(address types.Address) (bool, error)

	// BlockHash gets block hash by block number.
	BlockHash(number uint64) (types.B256, error)
}
