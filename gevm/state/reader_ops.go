package state

import (
	"github.com/holiman/uint256"

	"github.com/Giulio2002/gevm/types"
)

// Basic forwards to the underlying Database. Returns (zero, false, nil) when no
// DB is attached (e.g. spec tests that pre-populate the journal directly).
func (j *Journal) Basic(address types.Address) (AccountInfo, bool, error) {
	if j.DB == nil {
		return AccountInfo{}, false, nil
	}
	return j.DB.Basic(address)
}

// CodeByHash forwards to the underlying Database.
func (j *Journal) CodeByHash(codeHash types.B256) (types.Bytes, error) {
	if j.DB == nil {
		return nil, nil
	}
	return j.DB.CodeByHash(codeHash)
}

// ReadCode forwards to the underlying Database's Code(address) read.
func (j *Journal) ReadCode(address types.Address) (types.Bytes, error) {
	if j.DB == nil {
		return nil, nil
	}
	return j.DB.Code(address)
}

// Storage forwards to the underlying Database.
func (j *Journal) Storage(address types.Address, index uint256.Int) (uint256.Int, error) {
	if j.DB == nil {
		return uint256.Int{}, nil
	}
	return j.DB.Storage(address, index)
}

// HasStorage forwards to the underlying Database.
func (j *Journal) HasStorage(address types.Address) (bool, error) {
	if j.DB == nil {
		return false, nil
	}
	return j.DB.HasStorage(address)
}

// BlockHash forwards to the underlying Database.
func (j *Journal) BlockHash(number uint64) (types.B256, error) {
	if j.DB == nil {
		return types.B256Zero, nil
	}
	return j.DB.BlockHash(number)
}
