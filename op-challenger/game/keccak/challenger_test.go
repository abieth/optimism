package keccak

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum-optimism/optimism/op-challenger/game/keccak/fetcher"
	"github.com/ethereum-optimism/optimism/op-challenger/game/keccak/matrix"
	keccakTypes "github.com/ethereum-optimism/optimism/op-challenger/game/keccak/types"
	"github.com/ethereum-optimism/optimism/op-service/testlog"
	"github.com/ethereum-optimism/optimism/op-service/txmgr"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/require"
)

func TestChallenge(t *testing.T) {
	preimages := []keccakTypes.LargePreimageMetaData{
		{
			LargePreimageIdent: keccakTypes.LargePreimageIdent{
				Claimant: common.Address{0xff, 0x00},
				UUID:     big.NewInt(0),
			},
		},
		{
			LargePreimageIdent: keccakTypes.LargePreimageIdent{
				Claimant: common.Address{0xff, 0x01},
				UUID:     big.NewInt(1),
			},
		},
		{
			LargePreimageIdent: keccakTypes.LargePreimageIdent{
				Claimant: common.Address{0xff, 0x02},
				UUID:     big.NewInt(2),
			},
		},
	}

	logger := testlog.Logger(t, log.LvlInfo)

	t.Run("SendChallenges", func(t *testing.T) {
		verifier, sender, oracle, challenger := setupChallengerTest(logger)
		verifier.challenges[preimages[1].LargePreimageIdent] = keccakTypes.Challenge{StateMatrix: keccakTypes.StateSnapshot{0x01}}
		verifier.challenges[preimages[2].LargePreimageIdent] = keccakTypes.Challenge{StateMatrix: keccakTypes.StateSnapshot{0x02}}
		err := challenger.Challenge(context.Background(), common.Hash{0xaa}, oracle, preimages)
		require.NoError(t, err)

		// Should send the two challenges before returning
		require.Len(t, sender.sent, 1, "Should send a single batch of transactions")
		for ident, challenge := range verifier.challenges {
			tx, err := oracle.ChallengeTx(ident, challenge)
			require.NoError(t, err)
			require.Contains(t, sender.sent[0], tx)
		}
	})

	t.Run("ReturnErrorWhenSendingFails", func(t *testing.T) {
		verifier, sender, oracle, challenger := setupChallengerTest(logger)
		verifier.challenges[preimages[1].LargePreimageIdent] = keccakTypes.Challenge{StateMatrix: keccakTypes.StateSnapshot{0x01}}
		sender.err = errors.New("boom")
		err := challenger.Challenge(context.Background(), common.Hash{0xaa}, oracle, preimages)
		require.ErrorIs(t, err, sender.err)
	})

	t.Run("LogErrorWhenCreateTxFails", func(t *testing.T) {
		logs := testlog.Capture(logger)

		verifier, _, oracle, challenger := setupChallengerTest(logger)
		verifier.challenges[preimages[1].LargePreimageIdent] = keccakTypes.Challenge{StateMatrix: keccakTypes.StateSnapshot{0x01}}
		oracle.err = errors.New("boom")
		err := challenger.Challenge(context.Background(), common.Hash{0xaa}, oracle, preimages)
		require.NoError(t, err)

		errLog := logs.FindLog(log.LvlError, "Failed to create challenge transaction")
		require.ErrorIs(t, errLog.GetContextValue("err").(error), oracle.err)
	})

	t.Run("LogErrorWhenVerifierFails", func(t *testing.T) {
		logs := testlog.Capture(logger)

		verifier, _, oracle, challenger := setupChallengerTest(logger)
		verifier.challenges[preimages[1].LargePreimageIdent] = keccakTypes.Challenge{StateMatrix: keccakTypes.StateSnapshot{0x01}}
		verifier.err = errors.New("boom")
		err := challenger.Challenge(context.Background(), common.Hash{0xaa}, oracle, preimages)
		require.NoError(t, err)

		errLog := logs.FindLog(log.LvlError, "Failed to verify large preimage")
		require.ErrorIs(t, errLog.GetContextValue("err").(error), verifier.err)
	})

	t.Run("DoNotLogErrValid", func(t *testing.T) {
		logs := testlog.Capture(logger)

		_, _, oracle, challenger := setupChallengerTest(logger)
		// All preimages are valid
		err := challenger.Challenge(context.Background(), common.Hash{0xaa}, oracle, preimages)
		require.NoError(t, err)

		errLog := logs.FindLog(log.LvlError, "Failed to verify large preimage")
		require.Nil(t, errLog)

		dbgLog := logs.FindLog(log.LvlDebug, "Preimage is valid")
		require.NotNil(t, dbgLog)
	})
}

func setupChallengerTest(logger log.Logger) (*stubVerifier, *stubSender, *stubChallengerOracle, *PreimageChallenger) {
	verifier := &stubVerifier{
		challenges: make(map[keccakTypes.LargePreimageIdent]keccakTypes.Challenge),
	}
	sender := &stubSender{}
	oracle := &stubChallengerOracle{}
	metrics := &mockChallengeMetrics{}
	challenger := NewPreimageChallenger(logger, metrics, verifier, sender)
	return verifier, sender, oracle, challenger
}

type mockChallengeMetrics struct{}

func (m *mockChallengeMetrics) RecordPreimageChallenged()      {}
func (m *mockChallengeMetrics) RecordPreimageChallengeFailed() {}

type stubVerifier struct {
	challenges map[keccakTypes.LargePreimageIdent]keccakTypes.Challenge
	err        error
}

func (s *stubVerifier) CreateChallenge(_ context.Context, _ common.Hash, _ fetcher.Oracle, preimage keccakTypes.LargePreimageMetaData) (keccakTypes.Challenge, error) {
	if s.err != nil {
		return keccakTypes.Challenge{}, s.err
	}
	challenge, ok := s.challenges[preimage.LargePreimageIdent]
	if !ok {
		return keccakTypes.Challenge{}, matrix.ErrValid
	}
	return challenge, nil
}

type stubSender struct {
	err  error
	sent [][]txmgr.TxCandidate
}

func (s *stubSender) SendAndWait(_ string, txs ...txmgr.TxCandidate) ([]*types.Receipt, error) {
	if s.err != nil {
		return nil, s.err
	}
	s.sent = append(s.sent, txs)
	return nil, nil
}

type stubChallengerOracle struct {
	stubOracle
	err error
}

func (s *stubChallengerOracle) ChallengeTx(ident keccakTypes.LargePreimageIdent, challenge keccakTypes.Challenge) (txmgr.TxCandidate, error) {
	if s.err != nil {
		return txmgr.TxCandidate{}, s.err
	}
	return txmgr.TxCandidate{
		To:     &ident.Claimant,
		TxData: append(ident.UUID.Bytes(), challenge.StateMatrix.Pack()...),
	}, nil
}
