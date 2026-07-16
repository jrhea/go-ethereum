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

// Package gevmbridge lets geth execute EVM work through Giulio2002/gevm instead
// of geth's native interpreter. gevm is a self-contained transaction executor:
// it reads state through a small read-only Database, journals every write
// internally, and returns the result. This package adapts geth's StateDB to
// that Database, converts geth's block/message context into gevm's, runs
// gevm.Transact, and mirrors the resulting journal back into the StateDB.
//
// It is wired into two entry points, both gated on Enabled:
//   - core/vm/runtime (Execute/Call/Create) — isolated code execution, run
//     fee-less so it mirrors geth's raw evm.Call semantics.
//   - core.ApplyMessage — full transaction execution (fees, refunds, nonce),
//     used by block processing and eth_call.
//
// Caveats (this is an experimental backend, not a consensus-faithful drop-in):
//   - gevm produces a single gas figure; on a 2D-gas/BAL branch geth's block
//     validation may not agree, so it is meant for execution + benchmarking.
//   - The runtime path wraps a raw call in gevm's full Transact pipeline
//     (intrinsic gas + nonce bump), matching how gevm's own benchmarks measure.
//   - Message.SkipNonceChecks isn't honored (gevm always checks the nonce).
package gevmbridge

import (
	"errors"
	"math/big"
	"os"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"

	gevmhost "github.com/Giulio2002/gevm/host"
	gevmspec "github.com/Giulio2002/gevm/spec"
	gevmstate "github.com/Giulio2002/gevm/state"
	gevmtypes "github.com/Giulio2002/gevm/types"
)

// Enabled reports whether gevm is used as the EVM backend. On this branch it
// defaults to ON, so a plain build (and block import) runs through gevm with no
// configuration. Set GETH_GEVM=0 (or false/off/no) to fall back to geth's
// native EVM; callers may also set this directly before execution.
var Enabled = envEnabled()

func envEnabled() bool {
	switch os.Getenv("GETH_GEVM") {
	case "0", "false", "off", "no":
		return false
	default:
		return true
	}
}

// Verify, when set (GETH_GEVM_VERIFY env), makes the ApplyMessage path run each
// transaction through BOTH gevm and geth's native EVM and log any divergence in
// gas or post-state root. The native result is authoritative, so block import
// stays valid while mismatches are surfaced. Debugging aid only — leave it off
// for benchmarking.
var Verify = os.Getenv("GETH_GEVM_VERIFY") != ""

// Errors surfaced by the bridge. Halts and invalid transactions don't map
// cleanly onto geth's fine-grained vm/core errors; callers that only test for
// failure (receipt status, benchmark success) are unaffected.
var (
	ErrGevmHalt      = errors.New("gevm: execution halted")
	ErrGevmInvalidTx = errors.New("gevm: transaction validation failed")
)

// Msg is the bridge's backend-neutral message. It carries everything gevm needs
// without depending on core (which would create an import cycle, since core
// calls into this package).
type Msg struct {
	From          common.Address
	To            *common.Address // nil => CREATE
	Nonce         uint64
	Value         *uint256.Int
	GasLimit      uint64
	GasPrice      *uint256.Int
	GasFeeCap     *uint256.Int
	GasTipCap     *uint256.Int
	Data          []byte
	AccessList    types.AccessList
	BlobHashes    []common.Hash
	BlobGasFeeCap *uint256.Int
	SetCodeAuths  []types.SetCodeAuthorization
}

// Result is the outcome of a bridge execution.
type Result struct {
	ReturnData  []byte
	GasUsed     uint64
	GasRefund   int64
	Err         error           // nil on success; ErrExecutionReverted on revert; ErrGevm* otherwise
	Reverted    bool            // true if the call reverted (return data is the revert reason)
	Invalid     bool            // true if gevm rejected the tx before execution (nonce/balance/...)
	CreatedAddr *common.Address // set for a successful CREATE
}

// RunTx executes msg through gevm against sdb within the given block context and
// chain config, mirrors gevm's resulting state back into sdb, and returns the
// result. When noFees is true the run is fee-less (basefee and gas price forced
// to zero), which mirrors geth's raw evm.Call used by core/vm/runtime.
func RunTx(sdb vm.StateDB, bctx vm.BlockContext, cfg *params.ChainConfig, msg *Msg, noFees bool) *Result {
	rules := cfg.Rules(bctx.BlockNumber, bctx.Random != nil, bctx.Time)
	fork := forkID(rules)

	baseFee := u256FromBig(bctx.BaseFee)
	if noFees {
		baseFee = uint256.Int{}
	}
	blockEnv := gevmhost.BlockEnv{
		Beneficiary:  gevmtypes.Address(bctx.Coinbase),
		Timestamp:    *uint256.NewInt(bctx.Time),
		Number:       u256FromBig(bctx.BlockNumber),
		Difficulty:   u256FromBig(bctx.Difficulty),
		GasLimit:     *uint256.NewInt(bctx.GasLimit),
		BaseFee:      baseFee,
		BlobGasPrice: u256FromBig(bctx.BlobBaseFee),
		SlotNum:      *uint256.NewInt(bctx.SlotNum),
		GetHash: func(n uint64) (gevmtypes.B256, error) {
			if bctx.GetHash == nil {
				return gevmtypes.B256{}, nil
			}
			return gevmtypes.B256(bctx.GetHash(n)), nil
		},
	}
	if bctx.Random != nil {
		r := u256FromHash(*bctx.Random)
		blockEnv.Prevrandao = &r
	}
	cfgEnv := gevmhost.CfgEnv{ChainId: u256FromBig(cfg.ChainID)}

	adapter := &dbAdapter{sdb: sdb, getHash: bctx.GetHash}
	gtx := buildTx(msg, fork, noFees)

	evm := gevmhost.NewEvm(adapter, fork, blockEnv, cfgEnv)
	exec := evm.Transact(&gtx)

	res := &Result{
		GasUsed:   exec.GasUsed,
		GasRefund: exec.GasRefund,
		Invalid:   exec.ValidationError,
	}
	if len(exec.Output) > 0 {
		res.ReturnData = append([]byte(nil), exec.Output...)
	}
	if exec.CreatedAddr != nil {
		a := common.Address(*exec.CreatedAddr)
		res.CreatedAddr = &a
	}
	switch {
	case exec.ValidationError:
		res.Err = ErrGevmInvalidTx
	case exec.Kind == gevmhost.ResultSuccess:
		res.Err = nil
	case exec.Kind == gevmhost.ResultRevert:
		res.Err = vm.ErrExecutionReverted
		res.Reverted = true
	default:
		res.Err = ErrGevmHalt
	}

	// Mirror gevm's journaled writes into geth's StateDB. Skipped for a
	// validation failure, which leaves no committed state change.
	if !exec.ValidationError {
		writeBack(sdb, evm.Journal)
		addLogs(sdb, exec.Logs)
	}
	evm.ReleaseEvm()
	return res
}

// buildTx converts a bridge Msg into a gevm Transaction.
func buildTx(msg *Msg, fork gevmspec.ForkID, noFees bool) gevmhost.Transaction {
	t := gevmhost.Transaction{
		Caller:   gevmtypes.Address(msg.From),
		Input:    gevmtypes.Bytes(msg.Data),
		GasLimit: msg.GasLimit,
		Nonce:    msg.Nonce,
	}
	if msg.Value != nil {
		t.Value = *msg.Value
	}
	if msg.To == nil {
		t.Kind = gevmhost.TxKindCreate
	} else {
		t.Kind = gevmhost.TxKindCall
		t.To = gevmtypes.Address(*msg.To)
	}

	if noFees {
		// Raw call: no fee market, no basefee floor. Legacy type with a zero
		// price satisfies gevm's validation without funding the caller.
		t.TxType = gevmhost.TxTypeLegacy
		return t
	}

	gp := deref(msg.GasPrice)
	feeCap := gp
	if msg.GasFeeCap != nil {
		feeCap = *msg.GasFeeCap
	}
	tipCap := gp
	if msg.GasTipCap != nil {
		tipCap = *msg.GasTipCap
	}
	t.GasPrice = gp
	t.MaxFeePerGas = feeCap
	t.MaxPriorityFeePerGas = tipCap
	t.MaxFeePerBlobGas = deref(msg.BlobGasFeeCap)

	for _, al := range msg.AccessList {
		item := gevmhost.AccessListItem{Address: gevmtypes.Address(al.Address)}
		for _, k := range al.StorageKeys {
			item.StorageKeys = append(item.StorageKeys, u256FromHash(k))
		}
		t.AccessList = append(t.AccessList, item)
	}
	for _, bh := range msg.BlobHashes {
		t.BlobHashes = append(t.BlobHashes, u256FromHash(bh))
	}
	for i := range msg.SetCodeAuths {
		a := &msg.SetCodeAuths[i]
		t.AuthorizationList = append(t.AuthorizationList, gevmhost.Authorization{
			ChainId: a.ChainID,
			Address: gevmtypes.Address(a.Address),
			Nonce:   a.Nonce,
			YParity: a.V,
			R:       gevmtypes.B256(a.R.Bytes32()),
			S:       gevmtypes.B256(a.S.Bytes32()),
		})
	}

	// The original tx type isn't carried on a Message; infer the closest gevm
	// type so intrinsic-gas and validation gates line up for the common cases.
	switch {
	case len(msg.SetCodeAuths) > 0:
		t.TxType = gevmhost.TxTypeEIP7702
	case len(msg.BlobHashes) > 0:
		t.TxType = gevmhost.TxTypeEIP4844
	case fork.IsEnabledIn(gevmspec.London):
		t.TxType = gevmhost.TxTypeEIP1559
	case len(msg.AccessList) > 0:
		t.TxType = gevmhost.TxTypeEIP2930
	default:
		t.TxType = gevmhost.TxTypeLegacy
	}
	return t
}

// writeBack mirrors gevm's final journal state into geth's StateDB: every
// touched account's balance/nonce/code/storage, and self-destructions.
func writeBack(sdb vm.StateDB, j *gevmstate.Journal) {
	// Self-destructed accounts: let geth clear balance + storage and mark them
	// for deletion at Finalise. gevm has already applied EIP-6780 (only these
	// addresses are actually destroyed) and moved any balance to the target,
	// which is mirrored below as a normal account update.
	destroyed := make(map[gevmtypes.Address]struct{}, len(j.SelfdestructedAddresses))
	for _, addr := range j.SelfdestructedAddresses {
		acc := j.State[addr]
		if acc == nil || !acc.IsSelfdestructedLocally() {
			continue
		}
		sdb.SelfDestruct(common.Address(addr))
		destroyed[addr] = struct{}{}
	}

	for _, addr := range j.StateAddresses() {
		if _, ok := destroyed[addr]; ok {
			continue
		}
		acc := j.State[addr]
		if acc == nil {
			continue
		}
		gaddr := common.Address(addr)

		// Balance: apply as a delta since the interface exposes only Add/Sub.
		want := acc.Info.Balance
		cur := *sdb.GetBalance(gaddr)
		if !want.Eq(&cur) {
			if want.Gt(&cur) {
				d := new(uint256.Int).Sub(&want, &cur)
				sdb.AddBalance(gaddr, d, tracing.BalanceChangeUnspecified)
			} else {
				d := new(uint256.Int).Sub(&cur, &want)
				sdb.SubBalance(gaddr, d, tracing.BalanceChangeUnspecified)
			}
		}

		// Nonce.
		if n := acc.Info.Nonce; n != sdb.GetNonce(gaddr) {
			sdb.SetNonce(gaddr, n, tracing.NonceChangeUnspecified)
		}

		// Code: compare by hash so deployments (CREATE, EIP-7702 delegation)
		// and removals (EIP-7702 delegation clear, where gevm leaves Code nil
		// with an empty hash) are both mirrored. An unloaded-but-unchanged
		// code also has Code nil, but its hash matches the StateDB's, so it
		// falls through harmlessly.
		wantHash := common.Hash(acc.Info.CodeHash)
		if wantHash == (common.Hash{}) {
			wantHash = types.EmptyCodeHash
		}
		if curHash := sdb.GetCodeHash(gaddr); wantHash != curHash {
			if len(acc.Info.Code) > 0 {
				sdb.SetCode(gaddr, []byte(acc.Info.Code), tracing.CodeChangeUnspecified)
			} else if wantHash == types.EmptyCodeHash && curHash != (common.Hash{}) {
				// The account's code was removed (delegation clear).
				sdb.SetCode(gaddr, nil, tracing.CodeChangeUnspecified)
			}
		}

		// Storage.
		for k, slot := range acc.Storage {
			key := common.Hash(k.Bytes32())
			val := common.Hash(slot.PresentValue.Bytes32())
			if sdb.GetState(gaddr, key) != val {
				sdb.SetState(gaddr, key, val)
			}
		}
	}
}

// addLogs appends gevm's emitted logs to the StateDB.
func addLogs(sdb vm.StateDB, logs []gevmstate.Log) {
	for i := range logs {
		lg := &logs[i]
		topics := make([]common.Hash, lg.NumTopics)
		for t := 0; t < int(lg.NumTopics); t++ {
			topics[t] = common.Hash(lg.Topics[t])
		}
		sdb.AddLog(&types.Log{
			Address: common.Address(lg.Address),
			Topics:  topics,
			Data:    []byte(lg.Data),
		})
	}
}

// forkID maps geth's chain rules to gevm's fork identifier. Forks beyond gevm's
// knowledge (geth's post-Amsterdam experimental forks) clamp to Amsterdam.
func forkID(r params.Rules) gevmspec.ForkID {
	switch {
	case r.IsAmsterdam || r.IsBogota || r.IsUBT:
		return gevmspec.Amsterdam
	case r.IsOsaka:
		return gevmspec.Osaka
	case r.IsPrague:
		return gevmspec.Prague
	case r.IsCancun:
		return gevmspec.Cancun
	case r.IsShanghai:
		return gevmspec.Shanghai
	case r.IsMerge:
		return gevmspec.Merge
	case r.IsLondon:
		return gevmspec.London
	case r.IsBerlin:
		return gevmspec.Berlin
	case r.IsIstanbul:
		return gevmspec.Istanbul
	case r.IsPetersburg:
		return gevmspec.Petersburg
	case r.IsConstantinople:
		return gevmspec.Constantinople
	case r.IsByzantium:
		return gevmspec.Byzantium
	case r.IsEIP158:
		return gevmspec.SpuriousDragon
	case r.IsEIP150:
		return gevmspec.Tangerine
	case r.IsHomestead:
		return gevmspec.Homestead
	default:
		return gevmspec.Frontier
	}
}

func deref(v *uint256.Int) uint256.Int {
	if v == nil {
		return uint256.Int{}
	}
	return *v
}

func u256FromBig(b *big.Int) uint256.Int {
	if b == nil {
		return uint256.Int{}
	}
	v, overflow := uint256.FromBig(b)
	if overflow {
		return uint256.Int{}
	}
	return *v
}

func u256FromHash(h common.Hash) uint256.Int {
	var v uint256.Int
	v.SetBytes32(h[:])
	return v
}
