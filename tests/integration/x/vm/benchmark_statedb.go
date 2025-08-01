package vm

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	utiltx "github.com/cosmos/evm/testutil/tx"
)

func BenchmarkCreateAccountNew(b *testing.B) {
	suite := KeeperTestSuite{}
	suite.SetupTest()
	vmdb := suite.StateDB()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		addr := utiltx.GenerateAddress()
		b.StartTimer()
		vmdb.CreateAccount(addr)
	}
}

func BenchmarkCreateAccountExisting(b *testing.B) {
	suite := KeeperTestSuite{}
	suite.SetupTest()
	vmdb := suite.StateDB()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		vmdb.CreateAccount(suite.Keyring.GetAddr(0))
	}
}

func BenchmarkAddBalance(b *testing.B) {
	suite := KeeperTestSuite{}
	suite.SetupTest()
	vmdb := suite.StateDB()

	amt := uint256.NewInt(10)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		vmdb.AddBalance(suite.Keyring.GetAddr(0), amt, tracing.BalanceChangeUnspecified)
	}
}

func BenchmarkSetCode(b *testing.B) {
	suite := KeeperTestSuite{}
	suite.SetupTest()
	vmdb := suite.StateDB()

	hash := crypto.Keccak256Hash([]byte("code")).Bytes()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		vmdb.SetCode(suite.Keyring.GetAddr(0), hash)
	}
}

func BenchmarkSetState(b *testing.B) {
	suite := KeeperTestSuite{}
	suite.SetupTest()
	vmdb := suite.StateDB()

	hash := crypto.Keccak256Hash([]byte("topic")).Bytes()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		vmdb.SetCode(suite.Keyring.GetAddr(0), hash)
	}
}

func BenchmarkAddLog(b *testing.B) {
	suite := KeeperTestSuite{}
	suite.SetupTest()
	vmdb := suite.StateDB()

	topic := crypto.Keccak256Hash([]byte("topic"))
	txHash := crypto.Keccak256Hash([]byte("tx_hash"))
	blockHash := crypto.Keccak256Hash([]byte("block_hash"))

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		vmdb.AddLog(&ethtypes.Log{
			Address:     suite.Keyring.GetAddr(0),
			Topics:      []common.Hash{topic},
			Data:        []byte("data"),
			BlockNumber: 1,
			TxHash:      txHash,
			TxIndex:     1,
			BlockHash:   blockHash,
			Index:       1,
			Removed:     false,
		})
	}
}

func BenchmarkSnapshot(b *testing.B) {
	suite := KeeperTestSuite{}
	suite.SetupTest()
	vmdb := suite.StateDB()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		target := vmdb.Snapshot()
		require.Equal(b, i, target)
	}

	for i := b.N - 1; i >= 0; i-- {
		require.NotPanics(b, func() {
			vmdb.RevertToSnapshot(i)
		})
	}
}

func BenchmarkSubBalance(b *testing.B) {
	suite := KeeperTestSuite{}
	suite.SetupTest()
	vmdb := suite.StateDB()

	amt := uint256.NewInt(10)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		vmdb.SubBalance(suite.Keyring.GetAddr(0), amt, tracing.BalanceChangeUnspecified)
	}
}

func BenchmarkSetNonce(b *testing.B) {
	suite := KeeperTestSuite{}
	suite.SetupTest()
	vmdb := suite.StateDB()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		vmdb.SetNonce(suite.Keyring.GetAddr(0), 1, tracing.NonceChangeUnspecified)
	}
}

func BenchmarkAddRefund(b *testing.B) {
	suite := KeeperTestSuite{}
	suite.SetupTest()
	vmdb := suite.StateDB()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		vmdb.AddRefund(1)
	}
}

func BenchmarkSuicide(b *testing.B) {
	suite := KeeperTestSuite{}
	suite.SetupTest()
	vmdb := suite.StateDB()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		addr := utiltx.GenerateAddress()
		vmdb.CreateAccount(addr)
		b.StartTimer()

		vmdb.SelfDestruct(addr)
	}
}
