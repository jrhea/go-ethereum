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

package core

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/core/vm/gevmbridge"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// TestGevmApplyMessageTransfer exercises the core.ApplyMessage -> gevm path with
// a plain value transfer, checking that gevm's full transaction execution
// (balances, nonce, gas) is mirrored back into geth's StateDB.
func TestGevmApplyMessageTransfer(t *testing.T) {
	prev := gevmbridge.Enabled
	gevmbridge.Enabled = true
	defer func() { gevmbridge.Enabled = prev }()

	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	recipient := common.HexToAddress("0x2222222222222222222222222222222222222222")

	statedb, _ := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	statedb.CreateAccount(sender)
	statedb.AddBalance(sender, uint256.NewInt(1_000_000), tracing.BalanceChangeUnspecified)

	zero := uint64(0)
	cfg := &params.ChainConfig{
		ChainID:                 big.NewInt(1),
		HomesteadBlock:          new(big.Int),
		DAOForkBlock:            new(big.Int),
		EIP150Block:             new(big.Int),
		EIP155Block:             new(big.Int),
		EIP158Block:             new(big.Int),
		ByzantiumBlock:          new(big.Int),
		ConstantinopleBlock:     new(big.Int),
		PetersburgBlock:         new(big.Int),
		IstanbulBlock:           new(big.Int),
		MuirGlacierBlock:        new(big.Int),
		BerlinBlock:             new(big.Int),
		LondonBlock:             new(big.Int),
		ArrowGlacierBlock:       new(big.Int),
		GrayGlacierBlock:        new(big.Int),
		MergeNetsplitBlock:      new(big.Int),
		TerminalTotalDifficulty: big.NewInt(0),
		ShanghaiTime:            &zero,
		CancunTime:              &zero,
	}

	rnd := common.Hash{0x01}
	bctx := vm.BlockContext{
		CanTransfer: CanTransfer,
		Transfer:    Transfer,
		GetHash:     func(uint64) common.Hash { return common.Hash{} },
		Coinbase:    common.Address{0xcc},
		BlockNumber: big.NewInt(1),
		Time:        1000,
		Difficulty:  big.NewInt(0),
		BaseFee:     big.NewInt(0),
		BlobBaseFee: big.NewInt(0),
		GasLimit:    30_000_000,
		Random:      &rnd,
	}
	evm := vm.NewEVM(bctx, statedb, cfg, vm.Config{})

	to := recipient
	gp := NewGasPool(30_000_000)
	res, err := ApplyMessage(evm, &Message{
		From:      sender,
		To:        &to,
		Nonce:     0,
		Value:     uint256.NewInt(1000),
		GasLimit:  21000,
		GasPrice:  uint256.NewInt(0),
		GasFeeCap: uint256.NewInt(0),
		GasTipCap: uint256.NewInt(0),
	}, gp)
	if err != nil {
		t.Fatalf("ApplyMessage: %v", err)
	}
	if res.Failed() {
		t.Fatalf("transaction failed: %v", res.Err)
	}
	if res.UsedGas != 21000 {
		t.Fatalf("gas used = %d, want 21000", res.UsedGas)
	}
	if got := statedb.GetBalance(recipient); got.Cmp(uint256.NewInt(1000)) != 0 {
		t.Fatalf("recipient balance = %v, want 1000", got)
	}
	if got := statedb.GetBalance(sender); got.Cmp(uint256.NewInt(999_000)) != 0 {
		t.Fatalf("sender balance = %v, want 999000", got)
	}
	if n := statedb.GetNonce(sender); n != 1 {
		t.Fatalf("sender nonce = %d, want 1", n)
	}
}
