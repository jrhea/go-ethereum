package host

import (
	"math"

	"github.com/Giulio2002/gevm/precompiles"
	"github.com/Giulio2002/gevm/types"
	"github.com/Giulio2002/gevm/vm"
	"github.com/holiman/uint256"
)

func (evm *Evm) SystemCall(caller, to types.Address, input []byte) ExecutionResult {
	evm.host.Block = &evm.Block
	evm.host.Tx = TxEnv{Caller: caller}
	evm.host.Cfg = &evm.Cfg
	evm.host.Journal = evm.Journal

	vm.InitEmbeddedMemory(&evm.rootMemory)
	rootMemory := &evm.rootMemory
	vm.InitEmbeddedInterpreter(&evm.rootInterp, &evm.rootStack)

	if evm.JumpTableCache == nil {
		evm.JumpTableCache = make(map[types.B256][]byte)
	}
	runner := evm.Runner
	if runner == nil {
		runner = vm.DefaultRunner{}
	}
	precompileSet := precompiles.ForSpec(evm.ForkID)
	evm.host.Precompiles = precompileSet
	evm.host.DisablePrecompileFastPath = false
	evm.host.Hooks = nil
	handler := Handler{
		Host:           &evm.host,
		Precompiles:    precompileSet,
		RootMemory:     rootMemory,
		ReturnAlloc:    &evm.ReturnAlloc,
		JumpTableCache: evm.JumpTableCache,
		RootInterp:     &evm.rootInterp,
		RootBytecode:   &evm.rootBytecode,
		Runner:         runner,
	}
	if evm.Hooks != nil {
		handler.hooks = evm.Hooks
		evm.host.Hooks = evm.Hooks
	} else if tr, ok := runner.(*vm.TracingRunner); ok {
		handler.hooks = tr.Hooks
		evm.host.Hooks = tr.Hooks
	}

	callInputs := vm.CallInputs{
		Input:              input,
		ReturnMemoryOffset: vm.MemoryRange{},
		GasLimit:           math.MaxUint64 / 2,
		BytecodeAddress:    to,
		TargetAddress:      to,
		Caller:             caller,
		Value:              vm.NewCallValueTransfer(uint256.Int{}),
		Scheme:             vm.CallSchemeCall,
		IsStatic:           false,
	}
	frameResult := vm.NewFrameResultCall(handler.executeCall(&callInputs, 0, rootMemory))
	interpResult := frameResult.Call.Result

	var output types.Bytes
	if len(interpResult.Output) > 0 {
		output = make(types.Bytes, len(interpResult.Output))
		copy(output, interpResult.Output)
	}
	result := ExecutionResult{
		GasUsed: interpResult.Gas.Used(),
		Output:  output,
	}
	switch {
	case interpResult.Result.IsOk():
		result.Kind = ResultSuccess
		result.Reason = interpResult.Result
	case interpResult.Result.IsRevert():
		result.Kind = ResultRevert
		result.Reason = interpResult.Result
	default:
		result.Kind = ResultHalt
		result.Reason = interpResult.Result
	}
	return result
}
