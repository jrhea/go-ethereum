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

package core

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

func TestFloorDataGas(t *testing.T) {
	addr1 := common.HexToAddress("0x1111111111111111111111111111111111111111")
	addr2 := common.HexToAddress("0x2222222222222222222222222222222222222222")
	key1 := common.HexToHash("0xaa")
	key2 := common.HexToHash("0xbb")

	tests := []struct {
		name       string
		amsterdam  bool
		data       []byte
		accessList types.AccessList
		want       uint64
	}{
		{
			name: "pre-amsterdam/empty",
			want: params.TxGas,
		},
		{
			name: "pre-amsterdam/zero-bytes-only",
			data: bytes.Repeat([]byte{0x00}, 100),
			// 100 zero tokens * 10 cost = 1000
			want: params.TxGas + 100*params.TxCostFloorPerToken,
		},
		{
			name: "pre-amsterdam/non-zero-bytes-only",
			data: bytes.Repeat([]byte{0xff}, 100),
			// 100 nz * 4 tokens * 10 cost = 4000
			want: params.TxGas + 100*params.TxTokenPerNonZeroByte*params.TxCostFloorPerToken,
		},
		{
			name: "pre-amsterdam/mixed",
			data: append(bytes.Repeat([]byte{0x00}, 50), bytes.Repeat([]byte{0xff}, 50)...),
			// 50 zero + 50*4 nz = 250 tokens * 10 = 2500
			want: params.TxGas + (50+50*params.TxTokenPerNonZeroByte)*params.TxCostFloorPerToken,
		},
		{
			name: "pre-amsterdam/access-list-ignored",
			data: bytes.Repeat([]byte{0xff}, 10),
			accessList: types.AccessList{
				{Address: addr1, StorageKeys: []common.Hash{key1, key2}},
			},
			// pre-amsterdam: floor calculation does not include access list
			want: params.TxGas + 10*params.TxTokenPerNonZeroByte*params.TxCostFloorPerToken,
		},
		{
			name:      "amsterdam/empty",
			amsterdam: true,
			// EIP-2780 anchors the floor to the reduced base cost.
			want: params.TxBaseCost2780,
		},
		{
			name:      "amsterdam/data-only",
			amsterdam: true,
			data:      bytes.Repeat([]byte{0x00}, 1024),
			// post-amsterdam: every byte = 4 tokens regardless of value
			want: params.TxBaseCost2780 + 1024*params.TxTokenPerNonZeroByte*params.TxCostFloorPerToken7976,
		},
		{
			name:      "amsterdam/data-non-zero",
			amsterdam: true,
			data:      bytes.Repeat([]byte{0xff}, 1024),
			// same as zero data post-amsterdam
			want: params.TxBaseCost2780 + 1024*params.TxTokenPerNonZeroByte*params.TxCostFloorPerToken7976,
		},
		{
			name:      "amsterdam/access-list-addresses-only",
			amsterdam: true,
			accessList: types.AccessList{
				{Address: addr1},
				{Address: addr2},
			},
			// 2 * 20 bytes * 4 tokens/byte * 16 cost/token
			want: params.TxBaseCost2780 + 2*common.AddressLength*params.TxTokenPerNonZeroByte*params.TxCostFloorPerToken7976,
		},
		{
			name:      "amsterdam/access-list-with-storage-keys",
			amsterdam: true,
			accessList: types.AccessList{
				{Address: addr1, StorageKeys: []common.Hash{key1, key2}},
			},
			// 1 addr * 20 * 4 + 2 keys * 32 * 4 = 80 + 256 = 336 tokens * 16
			want: params.TxBaseCost2780 + (1*common.AddressLength+2*common.HashLength)*params.TxTokenPerNonZeroByte*params.TxCostFloorPerToken7976,
		},
		{
			name:      "amsterdam/mixed",
			amsterdam: true,
			data:      bytes.Repeat([]byte{0xff}, 100),
			accessList: types.AccessList{
				{Address: addr1, StorageKeys: []common.Hash{key1}},
				{Address: addr2, StorageKeys: []common.Hash{key1, key2}},
			},
			// data: 100*4 = 400; addrs: 2*20*4 = 160; keys: 3*32*4 = 384; total = 944 * 16
			want: params.TxBaseCost2780 + (100*params.TxTokenPerNonZeroByte+2*common.AddressLength*params.TxTokenPerNonZeroByte+3*common.HashLength*params.TxTokenPerNonZeroByte)*params.TxCostFloorPerToken7976,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rules := params.Rules{IsAmsterdam: tt.amsterdam}
			got, err := FloorDataGas(rules, addr1, &addr1, new(uint256.Int), tt.data, tt.accessList)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("gas mismatch: got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestIntrinsicGas(t *testing.T) {
	addr1 := common.HexToAddress("0x1111111111111111111111111111111111111111")
	addr2 := common.HexToAddress("0x2222222222222222222222222222222222222222")
	key1 := common.HexToHash("0xaa")
	key2 := common.HexToHash("0xbb")

	const (
		amsterdamAddressCost    = uint64(common.AddressLength) * params.TxCostFloorPerToken7976 * params.TxTokenPerNonZeroByte // 1280
		amsterdamStorageKeyCost = uint64(common.HashLength) * params.TxCostFloorPerToken7976 * params.TxTokenPerNonZeroByte    // 2048
	)

	tests := []struct {
		name        string
		data        []byte
		accessList  types.AccessList
		authList    []types.SetCodeAuthorization
		creation    bool
		isHomestead bool
		isEIP2028   bool
		isEIP3860   bool
		isAmsterdam bool
		value       *uint256.Int
		want        vm.GasCosts
	}{
		{
			name: "frontier/empty-call",
			want: vm.GasCosts{RegularGas: params.TxGas},
		},
		{
			name:        "frontier/contract-creation-pre-homestead",
			creation:    true,
			isHomestead: false,
			// pre-homestead, contract creation still uses TxGas
			want: vm.GasCosts{RegularGas: params.TxGas},
		},
		{
			name:        "homestead/contract-creation",
			creation:    true,
			isHomestead: true,
			want:        vm.GasCosts{RegularGas: params.TxGasContractCreation},
		},
		{
			name: "frontier/non-zero-data",
			data: bytes.Repeat([]byte{0xff}, 100),
			// 100 nz bytes * 68 (frontier)
			want: vm.GasCosts{RegularGas: params.TxGas + 100*params.TxDataNonZeroGasFrontier},
		},
		{
			name:      "istanbul/non-zero-data",
			data:      bytes.Repeat([]byte{0xff}, 100),
			isEIP2028: true,
			// 100 nz bytes * 16 (post-EIP2028)
			want: vm.GasCosts{RegularGas: params.TxGas + 100*params.TxDataNonZeroGasEIP2028},
		},
		{
			name:      "istanbul/zero-data",
			data:      bytes.Repeat([]byte{0x00}, 100),
			isEIP2028: true,
			// 100 zero bytes * 4
			want: vm.GasCosts{RegularGas: params.TxGas + 100*params.TxDataZeroGas},
		},
		{
			name:      "istanbul/mixed-data",
			data:      append(bytes.Repeat([]byte{0x00}, 50), bytes.Repeat([]byte{0xff}, 50)...),
			isEIP2028: true,
			want:      vm.GasCosts{RegularGas: params.TxGas + 50*params.TxDataZeroGas + 50*params.TxDataNonZeroGasEIP2028},
		},
		{
			name:        "shanghai/init-code-word-gas",
			data:        bytes.Repeat([]byte{0x00}, 64), // 2 words
			creation:    true,
			isHomestead: true,
			isEIP2028:   true,
			isEIP3860:   true,
			// TxGasContractCreation + 64 zero bytes * 4 + 2 words * 2
			want: vm.GasCosts{RegularGas: params.TxGasContractCreation + 64*params.TxDataZeroGas + 2*params.InitCodeWordGas},
		},
		{
			name:        "shanghai/init-code-non-multiple-of-32",
			data:        bytes.Repeat([]byte{0x00}, 33), // 2 words (rounded up)
			creation:    true,
			isHomestead: true,
			isEIP2028:   true,
			isEIP3860:   true,
			want:        vm.GasCosts{RegularGas: params.TxGasContractCreation + 33*params.TxDataZeroGas + 2*params.InitCodeWordGas},
		},
		{
			name: "berlin/access-list",
			accessList: types.AccessList{
				{Address: addr1, StorageKeys: []common.Hash{key1, key2}},
				{Address: addr2, StorageKeys: []common.Hash{key1}},
			},
			isEIP2028: true,
			// 2 addrs * 2400 + 3 keys * 1900
			want: vm.GasCosts{RegularGas: params.TxGas + 2*params.TxAccessListAddressGas + 3*params.TxAccessListStorageKeyGas},
		},
		{
			name: "amsterdam/access-list-extra-cost",
			accessList: types.AccessList{
				{Address: addr1, StorageKeys: []common.Hash{key1, key2}},
				{Address: addr2, StorageKeys: []common.Hash{key1}},
			},
			isEIP2028:   true,
			isAmsterdam: true,
			// EIP-2780: zero-value call base is TxBaseCost + ColdAccountAccess
			// (15,000). Plus base access-list charge + EIP-7981 extra.
			want: vm.GasCosts{RegularGas: params.TxBaseCost2780 + params.ColdAccountAccess2780 +
				2*params.TxAccessListAddressGasAmsterdam + 3*params.TxAccessListStorageKeyGasAmsterdam +
				2*amsterdamAddressCost + 3*amsterdamStorageKeyCost},
		},
		{
			name: "prague/auth-list",
			authList: []types.SetCodeAuthorization{
				{Address: addr1},
				{Address: addr2},
				{Address: addr1},
			},
			isEIP2028: true,
			// 3 auths * 25000 (pre-Amsterdam: CallNewAccountGas per auth tuple)
			want: vm.GasCosts{RegularGas: params.TxGas + 3*params.CallNewAccountGas},
		},
		{
			name:        "amsterdam/contract-creation-empty",
			creation:    true,
			isHomestead: true,
			isEIP2028:   true,
			isAmsterdam: true,
			// EIP-2780: creation regular gas is TxBaseCost + CreateAccess (23,000),
			// and account-creation cost is charged as state gas.
			want: vm.GasCosts{
				RegularGas: params.TxBaseCost2780 + params.CreateAccess2780,
				StateGas:   params.AccountCreationSize * params.CostPerStateByte,
			},
		},
		{
			name:        "amsterdam/contract-creation-init-code",
			data:        bytes.Repeat([]byte{0x00}, 64), // 2 words of init code
			creation:    true,
			isHomestead: true,
			isEIP2028:   true,
			isEIP3860:   true, // Shanghai gates init-code word gas
			isAmsterdam: true,
			want: vm.GasCosts{
				RegularGas: params.TxBaseCost2780 + params.CreateAccess2780 +
					64*params.TxDataZeroGas + 2*params.InitCodeWordGas,
				StateGas: params.AccountCreationSize * params.CostPerStateByte,
			},
		},
		{
			name: "amsterdam/contract-creation-with-access-list",
			data: bytes.Repeat([]byte{0xff}, 32), // 1 word of non-zero init code
			accessList: types.AccessList{
				{Address: addr1, StorageKeys: []common.Hash{key1}},
			},
			creation:    true,
			isHomestead: true,
			isEIP2028:   true,
			isEIP3860:   true,
			isAmsterdam: true,
			want: vm.GasCosts{
				RegularGas: params.TxBaseCost2780 + params.CreateAccess2780 +
					32*params.TxDataNonZeroGasEIP2028 + 1*params.InitCodeWordGas +
					1*params.TxAccessListAddressGasAmsterdam + 1*params.TxAccessListStorageKeyGasAmsterdam +
					1*amsterdamAddressCost + 1*amsterdamStorageKeyCost,
				StateGas: params.AccountCreationSize * params.CostPerStateByte,
			},
		},
		{
			name: "amsterdam/combined",
			data: bytes.Repeat([]byte{0xff}, 100),
			accessList: types.AccessList{
				{Address: addr1, StorageKeys: []common.Hash{key1}},
			},
			authList: []types.SetCodeAuthorization{
				{Address: addr2},
			},
			isEIP2028:   true,
			isAmsterdam: true,
			// EIP-8037 splits the auth-tuple charge into regular + state gas, with
			// the values finalized by EIP-8038:
			//   regular: ACCOUNT_WRITE (8,000) + REGULAR_PER_AUTH_BASE_COST (7,500) per auth
			//   state:   (AuthorizationCreationSize + AccountCreationSize) * CostPerStateByte per auth
			want: vm.GasCosts{
				RegularGas: params.TxBaseCost2780 + params.ColdAccountAccess2780 +
					100*params.TxDataNonZeroGasEIP2028 +
					1*params.TxAccessListAddressGasAmsterdam + 1*params.TxAccessListStorageKeyGasAmsterdam +
					1*amsterdamAddressCost + 1*amsterdamStorageKeyCost +
					1*(params.AccountWriteAmsterdam+params.RegularPerAuthBaseCost),
				StateGas: 1 * (params.AuthorizationCreationSize + params.AccountCreationSize) * params.CostPerStateByte,
			},
		},
		{
			name:        "amsterdam/value-transfer-call",
			isEIP2028:   true,
			isAmsterdam: true,
			value:       uint256.NewInt(1),
			// EIP-2780: TxBaseCost + ColdAccountAccess + TransferLogCost + TxValueCost = 21,000.
			want: vm.GasCosts{RegularGas: params.TxBaseCost2780 + params.ColdAccountAccess2780 +
				params.TransferLogCost2780 + params.TxValueCost2780},
		},
		{
			name:        "amsterdam/value-bearing-contract-creation",
			creation:    true,
			isHomestead: true,
			isEIP2028:   true,
			isAmsterdam: true,
			value:       uint256.NewInt(1),
			// EIP-2780: TxBaseCost + CreateAccess + TransferLogCost = 24,756, plus account-creation state gas.
			want: vm.GasCosts{
				RegularGas: params.TxBaseCost2780 + params.CreateAccess2780 + params.TransferLogCost2780,
				StateGas:   params.AccountCreationSize * params.CostPerStateByte,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rules := params.Rules{
				IsHomestead: tt.isHomestead,
				IsIstanbul:  tt.isEIP2028,
				IsShanghai:  tt.isEIP3860,
				IsAmsterdam: tt.isAmsterdam,
			}
			var to *common.Address
			if !tt.creation {
				to = &addr1
			}
			got, err := IntrinsicGas(tt.data, tt.accessList, tt.authList,
				common.Address{}, to, tt.value, rules, params.CostPerStateByte)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("gas mismatch: got %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestTransactionToMessageEffectiveGasPrice verifies that the uint256-domain
// effective gas price computed by TransactionToMessage matches the original
// big.Int algorithm, including the boundary cases around the fee cap and the
// case where baseFee+gasTipCap overflows 256 bits.
func TestTransactionToMessageEffectiveGasPrice(t *testing.T) {
	signer := types.LatestSignerForChainID(big.NewInt(1))
	key, _ := crypto.GenerateKey()
	to := common.HexToAddress("0x000000000000000000000000000000000000dead")

	// maxU256 is 2^256-1, the largest value representable as a uint256.
	maxU256 := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))

	// wantGasPrice mirrors the original big.Int algorithm and acts as the oracle.
	wantGasPrice := func(tx *types.Transaction, baseFee *big.Int) *big.Int {
		if baseFee == nil {
			return tx.GasPrice()
		}
		egp := new(big.Int).Add(baseFee, tx.GasTipCap())
		if egp.Cmp(tx.GasFeeCap()) > 0 {
			egp = tx.GasFeeCap()
		}
		return egp
	}

	tests := []struct {
		name    string
		txdata  types.TxData
		baseFee *big.Int
	}{
		{
			name:    "dynamic/below-cap",
			txdata:  &types.DynamicFeeTx{ChainID: big.NewInt(1), GasTipCap: big.NewInt(2e9), GasFeeCap: big.NewInt(30e9), Gas: 21000, To: &to, Value: big.NewInt(7)},
			baseFee: big.NewInt(1e9), // baseFee+tip = 3 gwei < 30 gwei cap
		},
		{
			name:    "dynamic/capped-by-feecap",
			txdata:  &types.DynamicFeeTx{ChainID: big.NewInt(1), GasTipCap: big.NewInt(2e9), GasFeeCap: big.NewInt(3e9), Gas: 21000, To: &to, Value: big.NewInt(7)},
			baseFee: big.NewInt(10e9), // baseFee+tip = 12 gwei > 3 gwei cap
		},
		{
			name:    "dynamic/exactly-at-cap",
			txdata:  &types.DynamicFeeTx{ChainID: big.NewInt(1), GasTipCap: big.NewInt(2e9), GasFeeCap: big.NewInt(3e9), Gas: 21000, To: &to, Value: big.NewInt(7)},
			baseFee: big.NewInt(1e9), // baseFee+tip = 3 gwei == cap
		},
		{
			name:    "dynamic/no-basefee",
			txdata:  &types.DynamicFeeTx{ChainID: big.NewInt(1), GasTipCap: big.NewInt(2e9), GasFeeCap: big.NewInt(3e9), Gas: 21000, To: &to, Value: big.NewInt(7)},
			baseFee: nil,
		},
		{
			name:    "dynamic/sum-overflows-256-bits",
			txdata:  &types.DynamicFeeTx{ChainID: big.NewInt(1), GasTipCap: maxU256, GasFeeCap: big.NewInt(3e9), Gas: 21000, To: &to, Value: big.NewInt(7)},
			baseFee: big.NewInt(1e9), // baseFee+tip wraps in uint256; must still cap to feecap
		},
		{
			name:    "legacy/with-basefee",
			txdata:  &types.LegacyTx{Nonce: 1, GasPrice: big.NewInt(5e9), Gas: 21000, To: &to, Value: big.NewInt(7)},
			baseFee: big.NewInt(1e9),
		},
		{
			name:    "legacy/no-basefee",
			txdata:  &types.LegacyTx{Nonce: 1, GasPrice: big.NewInt(5e9), Gas: 21000, To: &to, Value: big.NewInt(7)},
			baseFee: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx, err := types.SignNewTx(key, signer, tt.txdata)
			if err != nil {
				t.Fatalf("sign tx: %v", err)
			}
			msg, err := TransactionToMessage(tx, signer, tt.baseFee)
			if err != nil {
				t.Fatalf("TransactionToMessage: %v", err)
			}
			want := wantGasPrice(tx, tt.baseFee)
			if msg.GasPrice.ToBig().Cmp(want) != 0 {
				t.Fatalf("effective gas price: got %v, want %v", msg.GasPrice, want)
			}
			// The effective gas price must be an independent object, never an alias
			// of the fee cap, even when it is capped to the fee cap value.
			if tt.baseFee != nil && msg.GasPrice == msg.GasFeeCap {
				t.Fatalf("effective gas price aliases the fee cap object")
			}
		})
	}
}

func BenchmarkTransactionToMessage(b *testing.B) {
	signer := types.LatestSignerForChainID(big.NewInt(1))
	key, _ := crypto.GenerateKey()
	to := common.HexToAddress("0x000000000000000000000000000000000000dead")
	baseFee := big.NewInt(1000000000) // 1 gwei

	sign := func(txdata types.TxData) *types.Transaction {
		tx, err := types.SignNewTx(key, signer, txdata)
		if err != nil {
			b.Fatal(err)
		}
		return tx
	}
	benches := []struct {
		name string
		tx   *types.Transaction
	}{
		{"dynamic-fee", sign(&types.DynamicFeeTx{ChainID: big.NewInt(1), GasTipCap: big.NewInt(2e9), GasFeeCap: big.NewInt(3e9), Gas: 21000, To: &to, Value: big.NewInt(7)})},
		{"legacy", sign(&types.LegacyTx{GasPrice: big.NewInt(3e9), Gas: 21000, To: &to, Value: big.NewInt(7)})},
		{"blob", sign(&types.BlobTx{ChainID: uint256.NewInt(1), GasTipCap: uint256.NewInt(2e9), GasFeeCap: uint256.NewInt(3e9), BlobFeeCap: uint256.NewInt(10), BlobHashes: []common.Hash{{}}, Gas: 21000, To: to, Value: uint256.NewInt(7)})},
	}
	for _, bb := range benches {
		b.Run(bb.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := TransactionToMessage(bb.tx, signer, baseFee); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
