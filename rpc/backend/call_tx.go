package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	rpctypes "github.com/cosmos/evm/rpc/types"
	evmtypes "github.com/cosmos/evm/x/vm/types"

	errorsmod "cosmossdk.io/errors"

	"github.com/cosmos/cosmos-sdk/client/flags"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// Resend accepts an existing transaction and a new gas price and limit. It will remove
// the given transaction from the pool and reinsert it with the new gas price and limit.
func (b *Backend) Resend(args evmtypes.TransactionArgs, gasPrice *hexutil.Big, gasLimit *hexutil.Uint64) (common.Hash, error) {
	if args.Nonce == nil {
		return common.Hash{}, fmt.Errorf("missing transaction nonce in transaction spec")
	}

	args, err := b.SetTxDefaults(args)
	if err != nil {
		return common.Hash{}, err
	}

	// The signer used should always be the 'latest' known one because we expect
	// signers to be backwards-compatible with old transactions.
	cfg := b.ChainConfig()
	if cfg == nil {
		cfg = evmtypes.DefaultChainConfig(b.EvmChainID.Uint64()).EthereumConfig(nil)
	}

	signer := ethtypes.LatestSigner(cfg)

	matchTx := args.ToTransaction().AsTransaction()

	// Before replacing the old transaction, ensure the _new_ transaction fee is reasonable.
	price := matchTx.GasPrice()
	if gasPrice != nil {
		price = gasPrice.ToInt()
	}
	gas := matchTx.Gas()
	if gasLimit != nil {
		gas = uint64(*gasLimit)
	}
	if err := rpctypes.CheckTxFee(price, gas, b.RPCTxFeeCap()); err != nil {
		return common.Hash{}, err
	}

	pending, err := b.PendingTransactions()
	if err != nil {
		return common.Hash{}, err
	}

	for _, tx := range pending {
		// FIXME does Resend api possible at all?  https://github.com/evmos/ethermint/issues/905
		p, err := evmtypes.UnwrapEthereumMsg(tx, common.Hash{})
		if err != nil {
			// not valid ethereum tx
			continue
		}

		pTx := p.AsTransaction()

		wantSigHash := signer.Hash(matchTx)
		pFrom, err := ethtypes.Sender(signer, pTx)
		if err != nil {
			continue
		}

		if pFrom == *args.From && signer.Hash(pTx) == wantSigHash {
			// Match. Re-sign and send the transaction.
			if gasPrice != nil && (*big.Int)(gasPrice).Sign() != 0 {
				args.GasPrice = gasPrice
			}
			if gasLimit != nil && *gasLimit != 0 {
				args.Gas = gasLimit
			}

			return b.SendTransaction(args) // TODO: this calls SetTxDefaults again, refactor to avoid calling it twice
		}
	}

	return common.Hash{}, fmt.Errorf("transaction %#x not found", matchTx.Hash())
}

// SendRawTransaction send a raw Ethereum transaction.
func (b *Backend) SendRawTransaction(data hexutil.Bytes) (common.Hash, error) {
	// RLP decode raw transaction bytes
	tx := &ethtypes.Transaction{}
	if err := tx.UnmarshalBinary(data); err != nil {
		b.Logger.Error("transaction decoding failed", "error", err.Error())
		return common.Hash{}, err
	}

	// check the local node config in case unprotected txs are disabled
	if !b.UnprotectedAllowed() {
		if !tx.Protected() {
			// Ensure only eip155 signed transactions are submitted if EIP155Required is set.
			return common.Hash{}, errors.New("only replay-protected (EIP-155) transactions allowed over RPC")
		}
		if tx.ChainId().Uint64() != b.EvmChainID.Uint64() {
			return common.Hash{}, fmt.Errorf("incorrect chain-id; expected %d, got %d", b.EvmChainID, tx.ChainId())
		}
	}

	ethereumTx := &evmtypes.MsgEthereumTx{}
	if err := ethereumTx.FromSignedEthereumTx(tx, ethtypes.LatestSignerForChainID(b.EvmChainID)); err != nil {
		b.Logger.Error("transaction converting failed", "error", err.Error())
		return common.Hash{}, err
	}

	if err := ethereumTx.ValidateBasic(); err != nil {
		b.Logger.Debug("tx failed basic validation", "error", err.Error())
		return common.Hash{}, err
	}

	baseDenom := evmtypes.GetEVMCoinDenom()

	cosmosTx, err := ethereumTx.BuildTx(b.ClientCtx.TxConfig.NewTxBuilder(), baseDenom)
	if err != nil {
		b.Logger.Error("failed to build cosmos tx", "error", err.Error())
		return common.Hash{}, err
	}

	// Encode transaction by default Tx encoder
	txBytes, err := b.ClientCtx.TxConfig.TxEncoder()(cosmosTx)
	if err != nil {
		b.Logger.Error("failed to encode eth tx using default encoder", "error", err.Error())
		return common.Hash{}, err
	}

	txHash := ethereumTx.AsTransaction().Hash()

	syncCtx := b.ClientCtx.WithBroadcastMode(flags.BroadcastSync)
	rsp, err := syncCtx.BroadcastTx(txBytes)
	if rsp != nil && rsp.Code != 0 {
		err = errorsmod.ABCIError(rsp.Codespace, rsp.Code, rsp.RawLog)
	}
	if err != nil {
		b.Logger.Error("failed to broadcast tx", "error", err.Error())
		return txHash, err
	}

	return txHash, nil
}

// SetTxDefaults populates tx message with default values in case they are not
// provided on the args
func (b *Backend) SetTxDefaults(args evmtypes.TransactionArgs) (evmtypes.TransactionArgs, error) {
	if args.GasPrice != nil && (args.MaxFeePerGas != nil || args.MaxPriorityFeePerGas != nil) {
		return args, errors.New("both gasPrice and (maxFeePerGas or maxPriorityFeePerGas) specified")
	}

	head, _ := b.CurrentHeader() // #nosec G703 -- no need to check error cause we're already checking that head == nil
	if head == nil {
		return args, errors.New("latest header is nil")
	}

	// If user specifies both maxPriorityfee and maxFee, then we do not
	// need to consult the chain for defaults. It's definitely a London tx.
	if args.MaxPriorityFeePerGas == nil || args.MaxFeePerGas == nil {
		// In this clause, user left some fields unspecified.
		if head.BaseFee != nil && args.GasPrice == nil {
			if args.MaxPriorityFeePerGas == nil {
				tip, err := b.SuggestGasTipCap(head.BaseFee)
				if err != nil {
					return args, err
				}
				args.MaxPriorityFeePerGas = (*hexutil.Big)(tip)
			}

			if args.MaxFeePerGas == nil {
				gasFeeCap := new(big.Int).Add(
					(*big.Int)(args.MaxPriorityFeePerGas),
					new(big.Int).Mul(head.BaseFee, big.NewInt(2)),
				)
				args.MaxFeePerGas = (*hexutil.Big)(gasFeeCap)
			}

			if args.MaxFeePerGas.ToInt().Cmp(args.MaxPriorityFeePerGas.ToInt()) < 0 {
				return args, fmt.Errorf("maxFeePerGas (%v) < maxPriorityFeePerGas (%v)", args.MaxFeePerGas, args.MaxPriorityFeePerGas)
			}
		} else {
			if args.MaxFeePerGas != nil || args.MaxPriorityFeePerGas != nil {
				return args, errors.New("maxFeePerGas or maxPriorityFeePerGas specified but london is not active yet")
			}

			if args.GasPrice == nil {
				price, err := b.SuggestGasTipCap(head.BaseFee)
				if err != nil {
					return args, err
				}
				if head.BaseFee != nil {
					// The legacy tx gas price suggestion should not add 2x base fee
					// because all fees are consumed, so it would result in a spiral
					// upwards.
					price.Add(price, head.BaseFee)
				}
				args.GasPrice = (*hexutil.Big)(price)
			}
		}
	} else {
		// Both maxPriorityfee and maxFee set by caller. Sanity-check their internal relation
		if args.MaxFeePerGas.ToInt().Cmp(args.MaxPriorityFeePerGas.ToInt()) < 0 {
			return args, fmt.Errorf("maxFeePerGas (%v) < maxPriorityFeePerGas (%v)", args.MaxFeePerGas, args.MaxPriorityFeePerGas)
		}
	}

	if args.Value == nil {
		args.Value = new(hexutil.Big)
	}
	if args.Nonce == nil {
		// get the nonce from the account retriever
		// ignore error in case tge account doesn't exist yet
		nonce, _ := b.getAccountNonce(*args.From, true, 0, b.Logger) // #nosec G703s
		args.Nonce = (*hexutil.Uint64)(&nonce)
	}

	if args.Data != nil && args.Input != nil && !bytes.Equal(*args.Data, *args.Input) {
		return args, errors.New("both 'data' and 'input' are set and not equal. Please use 'input' to pass transaction call data")
	}

	if args.To == nil {
		// Contract creation
		var input []byte
		if args.Data != nil {
			input = *args.Data
		} else if args.Input != nil {
			input = *args.Input
		}

		if len(input) == 0 {
			return args, errors.New("contract creation without any data provided")
		}
	}

	if args.Gas == nil {
		// For backwards-compatibility reason, we try both input and data
		// but input is preferred.
		input := args.Input
		if input == nil {
			input = args.Data
		}

		callArgs := evmtypes.TransactionArgs{
			From:                 args.From,
			To:                   args.To,
			Gas:                  args.Gas,
			GasPrice:             args.GasPrice,
			MaxFeePerGas:         args.MaxFeePerGas,
			MaxPriorityFeePerGas: args.MaxPriorityFeePerGas,
			Value:                args.Value,
			Data:                 input,
			AccessList:           args.AccessList,
			ChainID:              args.ChainID,
			Nonce:                args.Nonce,
		}

		blockNr := rpctypes.NewBlockNumber(big.NewInt(0))
		estimated, err := b.EstimateGas(callArgs, &blockNr)
		if err != nil {
			return args, err
		}
		args.Gas = &estimated
		b.Logger.Debug("estimate gas usage automatically", "gas", args.Gas)
	}

	if args.ChainID == nil {
		args.ChainID = (*hexutil.Big)(b.EvmChainID)
	}

	return args, nil
}

// EstimateGas returns an estimate of gas usage for the given smart contract call.
func (b *Backend) EstimateGas(args evmtypes.TransactionArgs, blockNrOptional *rpctypes.BlockNumber) (hexutil.Uint64, error) {
	blockNr := rpctypes.EthPendingBlockNumber
	if blockNrOptional != nil {
		blockNr = *blockNrOptional
	}

	bz, err := json.Marshal(&args)
	if err != nil {
		return 0, err
	}

	header, err := b.TendermintBlockByNumber(blockNr)
	if err != nil {
		// the error message imitates geth behavior
		return 0, errors.New("header not found")
	}

	req := evmtypes.EthCallRequest{
		Args:            bz,
		GasCap:          b.RPCGasCap(),
		ProposerAddress: sdk.ConsAddress(header.Block.ProposerAddress),
		ChainId:         b.EvmChainID.Int64(),
	}

	// From ContextWithHeight: if the provided height is 0,
	// it will return an empty context and the gRPC query will use
	// the latest block height for querying.
	res, err := b.QueryClient.EstimateGas(rpctypes.ContextWithHeight(blockNr.Int64()), &req)
	if err != nil {
		return 0, err
	}
	if err = handleRevertError(res.VmError, res.Ret); err != nil {
		return 0, err
	}
	return hexutil.Uint64(res.Gas), nil
}

// DoCall performs a simulated call operation through the evmtypes. It returns the
// estimated gas used on the operation or an error if fails.
func (b *Backend) DoCall(
	args evmtypes.TransactionArgs, blockNr rpctypes.BlockNumber,
) (*evmtypes.MsgEthereumTxResponse, error) {
	bz, err := json.Marshal(&args)
	if err != nil {
		return nil, err
	}
	header, err := b.TendermintBlockByNumber(blockNr)
	if err != nil {
		// the error message imitates geth behavior
		return nil, errors.New("header not found")
	}

	req := evmtypes.EthCallRequest{
		Args:            bz,
		GasCap:          b.RPCGasCap(),
		ProposerAddress: sdk.ConsAddress(header.Block.ProposerAddress),
		ChainId:         b.EvmChainID.Int64(),
	}

	// From ContextWithHeight: if the provided height is 0,
	// it will return an empty context and the gRPC query will use
	// the latest block height for querying.
	ctx := rpctypes.ContextWithHeight(blockNr.Int64())
	timeout := b.RPCEVMTimeout()

	// Setup context so it may be canceled the call has completed
	// or, in case of unmetered gas, setup a context with a timeout.
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}

	// Make sure the context is canceled when the call has completed
	// this makes sure resources are cleaned up.
	defer cancel()

	res, err := b.QueryClient.EthCall(ctx, &req)
	if err != nil {
		return nil, err
	}

	if err = handleRevertError(res.VmError, res.Ret); err != nil {
		return nil, err
	}

	return res, nil
}

// GasPrice returns the current gas price based on Cosmos EVM' gas price oracle.
func (b *Backend) GasPrice() (*hexutil.Big, error) {
	var (
		result *big.Int
		err    error
	)

	head, err := b.CurrentHeader()
	if err != nil {
		return nil, err
	}

	if head.BaseFee != nil {
		result, err = b.SuggestGasTipCap(head.BaseFee)
		if err != nil {
			return nil, err
		}
		result = result.Add(result, head.BaseFee)
	} else {
		result = b.RPCMinGasPrice()
	}

	// return at least GlobalMinGasPrice from FeeMarket module
	minGasPrice, err := b.GlobalMinGasPrice()
	if err != nil {
		return nil, err
	}
	if result.Cmp(minGasPrice) < 0 {
		result = minGasPrice
	}

	return (*hexutil.Big)(result), nil
}

// handleRevertError returns revert related error.
func handleRevertError(vmError string, ret []byte) error {
	if len(vmError) > 0 {
		if vmError != vm.ErrExecutionReverted.Error() {
			return status.Error(codes.Internal, vmError)
		}
		if len(ret) == 0 {
			return errors.New(vmError)
		}
		return evmtypes.NewExecErrorWithReason(ret)
	}
	return nil
}
