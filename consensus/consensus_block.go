// Copyright (c) 2018 The VeChainThor developers

// Distributed under the GNU Lesser General Public License v3.0 software license, see the accompanying
// file LICENSE or <https://www.gnu.org/licenses/lgpl-3.0.html>

package consensus

import (
	"bytes"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	crypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/pkg/errors"

	"github.com/dfinlab/meter/block"
	"github.com/dfinlab/meter/comm"
	bls "github.com/dfinlab/meter/crypto/multi_sig"
	cmn "github.com/dfinlab/meter/libs/common"
	"github.com/dfinlab/meter/logdb"
	"github.com/dfinlab/meter/meter"
	"github.com/dfinlab/meter/packer"
	"github.com/dfinlab/meter/powpool"
	"github.com/dfinlab/meter/runtime"
	"github.com/dfinlab/meter/state"
	"github.com/dfinlab/meter/tx"
	"github.com/dfinlab/meter/txpool"
	"github.com/dfinlab/meter/xenv"
)

// Process process a block.
func (c *ConsensusReactor) Process(blk *block.Block, nowTimestamp uint64) (*state.Stage, tx.Receipts, error) {
	header := blk.Header()

	if _, err := c.chain.GetBlockHeader(header.ID()); err != nil {
		if !c.chain.IsNotFound(err) {
			return nil, nil, err
		}
	} else {
		// we may already have this blockID. If it is after the best, still accept it
		if header.Number() <= c.chain.BestBlock().Header().Number() {
			return nil, nil, errKnownBlock
		} else {
			c.logger.Debug("continue to process blk ...", "height", header.Number())
		}
	}

	parentHeader, err := c.chain.GetBlockHeader(header.ParentID())
	if err != nil {
		if !c.chain.IsNotFound(err) {
			return nil, nil, err
		}
		return nil, nil, errParentMissing
	}

	state, err := c.stateCreator.NewState(parentHeader.StateRoot())
	if err != nil {
		return nil, nil, err
	}

	stage, receipts, err := c.validate(state, blk, parentHeader, nowTimestamp)
	if err != nil {
		return nil, nil, err
	}

	return stage, receipts, nil
}

func (c *ConsensusReactor) ProcessProposedBlock(parentHeader *block.Header, blk *block.Block, nowTimestamp uint64) (*state.Stage, tx.Receipts, error) {
	header := blk.Header()

	if _, err := c.chain.GetBlockHeader(header.ID()); err != nil {
		if !c.chain.IsNotFound(err) {
			return nil, nil, err
		}
	} else {
		return nil, nil, errKnownBlock
	}

	if parentHeader == nil {
		return nil, nil, errParentHeaderMissing
	}

	state, err := c.stateCreator.NewState(parentHeader.StateRoot())
	if err != nil {
		return nil, nil, err
	}

	stage, receipts, err := c.validate(state, blk, parentHeader, nowTimestamp)
	if err != nil {
		return nil, nil, err
	}

	return stage, receipts, nil
}

func (c *ConsensusReactor) NewRuntimeForReplay(header *block.Header) (*runtime.Runtime, error) {
	signer, err := header.Signer()
	if err != nil {
		return nil, err
	}
	parentHeader, err := c.chain.GetBlockHeader(header.ParentID())
	if err != nil {
		if !c.chain.IsNotFound(err) {
			return nil, err
		}
		return nil, errParentMissing
	}
	state, err := c.stateCreator.NewState(parentHeader.StateRoot())
	if err != nil {
		return nil, err
	}
	if err := c.validateProposer(header, parentHeader, state); err != nil {
		return nil, err
	}

	//TBD: hotstuff, current block's evidence is not available yet. comment out
	// we can validate this evidence with block parent.
	/******
	blk, err := c.chain.GetBlock(header.ID())
	if err != nil {
		return nil, err
	}

	if err := c.validateEvidence(blk.GetBlockEvidence(), blk); err != nil {
		return nil, err
	}
	****/
	return runtime.New(
		c.chain.NewSeeker(header.ParentID()),
		state,
		&xenv.BlockContext{
			Beneficiary: header.Beneficiary(),
			Signer:      signer,
			Number:      header.Number(),
			Time:        header.Timestamp(),
			GasLimit:    header.GasLimit(),
			TotalScore:  header.TotalScore(),
		}), nil
}

func (c *ConsensusReactor) validate(
	state *state.State,
	block *block.Block,
	parentHeader *block.Header,
	nowTimestamp uint64,
) (*state.Stage, tx.Receipts, error) {
	header := block.Header()

	if err := c.validateBlockHeader(header, parentHeader, nowTimestamp); err != nil {
		return nil, nil, err
	}

	//same above reason.
	/****
	if err := c.validateEvidence(block.GetBlockEvidence(), block); err != nil {
		return nil, nil, err
	}
	****/
	if err := c.validateProposer(header, parentHeader, state); err != nil {
		return nil, nil, err
	}

	if err := c.validateBlockBody(block); err != nil {
		return nil, nil, err
	}

	stage, receipts, err := c.verifyBlock(block, state)
	if err != nil {
		return nil, nil, err
	}

	return stage, receipts, nil
}

func (c *ConsensusReactor) validateBlockHeader(header *block.Header, parent *block.Header, nowTimestamp uint64) error {
	if header.Timestamp() <= parent.Timestamp() {
		return consensusError(fmt.Sprintf("block timestamp behind parents: parent %v, current %v", parent.Timestamp(), header.Timestamp()))
	}

	if header.Timestamp() > nowTimestamp+meter.BlockInterval {
		return errFutureBlock
	}

	if !block.GasLimit(header.GasLimit()).IsValid(parent.GasLimit()) {
		return consensusError(fmt.Sprintf("block gas limit invalid: parent %v, current %v", parent.GasLimit(), header.GasLimit()))
	}

	if header.GasUsed() > header.GasLimit() {
		return consensusError(fmt.Sprintf("block gas used exceeds limit: limit %v, used %v", header.GasLimit(), header.GasUsed()))
	}

	if header.TotalScore() <= parent.TotalScore() {
		return consensusError(fmt.Sprintf("block total score invalid: parent %v, current %v", parent.TotalScore(), header.TotalScore()))
	}

	return nil
}

func (c *ConsensusReactor) validateEvidence(ev *block.Evidence, blk *block.Block) error {
	// skip the validation if the 'skip-signature-check' flag is configured
	if c.config.SkipSignatureCheck {
		return nil
	}

	header := blk.Header()

	// find out the block which has the committee info
	// Normally we store the committee info in the first of Mblock after Kblock
	lastKBlock := header.LastKBlockHeight()
	var b *block.Block
	if (lastKBlock + 1) == header.Number() {
		b = blk
	} else {
		var err error
		b, err = c.chain.GetTrunkBlock(lastKBlock + 1)
		if err != nil {
			c.logger.Error("get committee info block error")
			return consensusError(fmt.Sprintf("get committee info block failed: %v", err))
		}
	}
	c.logger.Info("get committeeinfo from block", "height", b.Header().Number())

	system := c.csCommon.GetSystem()
	//params := c.csCommon.GetParams()
	//pairing := c.csCommon.GetPairing()

	// committee members
	cis, err := b.GetCommitteeInfo()
	if err != nil {
		c.logger.Error("decode committee info block error")
		return consensusError(fmt.Sprintf("decode committee info block failed: %v", err))
	}

	cms := c.BuildCommitteeMemberFromInfo(system, cis)
	if len(cms) == 0 {
		c.logger.Error("get committee members error")
		return consensusError(fmt.Sprintf("get committee members failed: %v", err))
	}

	//validate voting signature
	voteSig, err := system.SigFromBytes(ev.VotingSig)
	if err != nil {
		c.logger.Error("Sig from bytes error")
		return consensusError(fmt.Sprintf("voting signature from bytes failed: %v", err))
	}

	voteBA := ev.VotingBitArray
	voteCSPubKeys := []bls.PublicKey{}
	for _, cm := range cms {
		if voteBA.GetIndex(cm.CSIndex) == true {
			voteCSPubKeys = append(voteCSPubKeys, cm.CSPubKey)
		}
	}

	/*****
	fmt.Println("VoterMsgHash", cmn.ByteSliceToByte32(ev.VotingMsgHash))
	for _, p := range voteCSPubKeys {
		fmt.Println("pubkey:::", system.PubKeyToBytes(p))
	}
	fmt.Println("aggrsig", system.SigToBytes(voteSig), "system", system.ToBytes())
	*****/

	voteValid, err := bls.AggregateVerify(voteSig, cmn.ByteSliceToByte32(ev.VotingMsgHash), voteCSPubKeys)
	if (err != nil) || (voteValid != true) {
		c.logger.Error("voting signature validate error")
		return consensusError(fmt.Sprintf("voting signature validate error"))
	}

	//validate notarize signature
	notarizeSig, err := system.SigFromBytes(ev.NotarizeSig)
	if err != nil {
		c.logger.Error("Notarize Sig from bytes error")
		return consensusError(fmt.Sprintf("notarize signature from bytes failed: %v", err))
	}
	notarizeBA := ev.NotarizeBitArray
	notarizeCSPubKeys := []bls.PublicKey{}

	for _, cm := range cms {
		if notarizeBA.GetIndex(cm.CSIndex) == true {
			notarizeCSPubKeys = append(notarizeCSPubKeys, cm.CSPubKey)
		}
	}
	noterizeValid, err := bls.AggregateVerify(notarizeSig, cmn.ByteSliceToByte32(ev.NotarizeMsgHash), notarizeCSPubKeys)
	if (err != nil) || (noterizeValid != true) {
		c.logger.Error("notarize signature validate error")
		return consensusError(fmt.Sprintf("notarize signature validate error"))
	}
	return nil
}

func (c *ConsensusReactor) validateProposer(header *block.Header, parent *block.Header, st *state.State) error {
	_, err := header.Signer()
	if err != nil {
		return consensusError(fmt.Sprintf("block signer unavailable: %v", err))
	}
	// fmt.Println("signer", signer)
	return nil
}

func (c *ConsensusReactor) validateBlockBody(blk *block.Block) error {
	header := blk.Header()
	txs := blk.Transactions()
	if header.TxsRoot() != txs.RootHash() {
		return consensusError(fmt.Sprintf("block txs root mismatch: want %v, have %v", header.TxsRoot(), txs.RootHash()))
	}

	for _, tx := range txs {
		signer, err := tx.Signer()
		if err != nil {
			return consensusError(fmt.Sprintf("tx signer unavailable: %v", err))
		}

		// transaction critiers:
		// 1. no signature (no signer)
		// 2. only located in kblock.
		if signer.IsZero() {
			if blk.Header().BlockType() != block.BLOCK_TYPE_K_BLOCK {
				return consensusError(fmt.Sprintf("tx signer unavailable"))
			}
		}

		switch {
		case tx.ChainTag() != c.chain.Tag():
			return consensusError(fmt.Sprintf("tx chain tag mismatch: want %v, have %v", c.chain.Tag(), tx.ChainTag()))
		case header.Number() < tx.BlockRef().Number():
			return consensusError(fmt.Sprintf("tx ref future block: ref %v, current %v", tx.BlockRef().Number(), header.Number()))
		case tx.IsExpired(header.Number()):
			return consensusError(fmt.Sprintf("tx expired: ref %v, current %v, expiration %v", tx.BlockRef().Number(), header.Number(), tx.Expiration()))
		case tx.HasReservedFields():
			return consensusError(fmt.Sprintf("tx reserved fields not empty"))
		}
	}

	return nil
}

func (c *ConsensusReactor) verifyBlock(blk *block.Block, state *state.State) (*state.Stage, tx.Receipts, error) {
	var totalGasUsed uint64
	txs := blk.Transactions()
	receipts := make(tx.Receipts, 0, len(txs))
	processedTxs := make(map[meter.Bytes32]bool)
	header := blk.Header()
	signer, _ := header.Signer()
	rt := runtime.New(
		c.chain.NewSeeker(header.ParentID()),
		state,
		&xenv.BlockContext{
			Beneficiary: header.Beneficiary(),
			Signer:      signer,
			Number:      header.Number(),
			Time:        header.Timestamp(),
			GasLimit:    header.GasLimit(),
			TotalScore:  header.TotalScore(),
		})

	findTx := func(txID meter.Bytes32) (found bool, reverted bool, err error) {
		if reverted, ok := processedTxs[txID]; ok {
			return true, reverted, nil
		}
		meta, err := c.chain.GetTransactionMeta(txID, header.ParentID())
		if err != nil {
			if c.chain.IsNotFound(err) {
				return false, false, nil
			}
			return false, false, err
		}
		return true, meta.Reverted, nil
	}
	for _, tx := range txs {
		// Mint transaction critiers:
		// 1. no signature (no signer)
		// 2. only located in 1st transaction in kblock.
		signer, err := tx.Signer()
		if err != nil {
			return nil, nil, consensusError(fmt.Sprintf("tx signer unavailable: %v", err))
		}

		if signer.IsZero() {
			//TBD: check to addresses in clauses
			if blk.Header().BlockType() != block.BLOCK_TYPE_K_BLOCK {
				return nil, nil, consensusError(fmt.Sprintf("tx signer unavailable"))
			}
		}

		// check if tx existed
		if found, _, err := findTx(tx.ID()); err != nil {
			return nil, nil, err
		} else if found {
			return nil, nil, consensusError("tx already exists")
		}

		// check depended tx
		if dep := tx.DependsOn(); dep != nil {
			found, reverted, err := findTx(*dep)
			if err != nil {
				return nil, nil, err
			}
			if !found {
				return nil, nil, consensusError("tx dep broken")
			}

			if reverted {
				return nil, nil, consensusError("tx dep reverted")
			}
		}

		receipt, err := rt.ExecuteTransaction(tx)
		if err != nil {
			return nil, nil, err
		}

		totalGasUsed += receipt.GasUsed
		receipts = append(receipts, receipt)
		processedTxs[tx.ID()] = receipt.Reverted
	}

	if header.GasUsed() != totalGasUsed {
		return nil, nil, consensusError(fmt.Sprintf("block gas used mismatch: want %v, have %v", header.GasUsed(), totalGasUsed))
	}

	receiptsRoot := receipts.RootHash()
	if header.ReceiptsRoot() != receiptsRoot {
		return nil, nil, consensusError(fmt.Sprintf("block receipts root mismatch: want %v, have %v", header.ReceiptsRoot(), receiptsRoot))
	}

	if err := rt.Seeker().Err(); err != nil {
		return nil, nil, errors.WithMessage(err, "chain")
	}

	stage := state.Stage()
	stateRoot, err := stage.Hash()
	if err != nil {
		return nil, nil, err
	}

	if blk.Header().StateRoot() != stateRoot {
		return nil, nil, consensusError(fmt.Sprintf("block state root mismatch: want %v, have %v", header.StateRoot(), stateRoot))
	}

	return stage, receipts, nil
}

//-----------------------------------------------------------
//---------------block store new wrappers routines ----------

//

func (conR *ConsensusReactor) CommitteeInfoCompare(cm1, cm2 []block.CommitteeInfo) bool {
	if len(cm1) != len(cm2) {
		conR.logger.Error("committee size mismatch", "len(cm1)", len(cm1), "len(cm2)", len(cm2))
		return false
	}
	var i int
	for _, cm := range cm1 {
		fmt.Println("MY CM: ", cm)
	}
	for _, cm := range cm2 {
		fmt.Println("GOT CM: ", cm)
	}
	for i = 0; i < len(cm1); i++ {
		if (cm1[i].Name != cm2[i].Name) || (bytes.Equal(cm1[i].PubKey, cm2[i].PubKey) == false) ||
			(bytes.Equal(cm1[i].CSPubKey, cm2[i].CSPubKey) == false) {
			conR.logger.Error("committee member mismatch", "i", i)
			fmt.Println(cm1[i])
			fmt.Println(cm2[i])
			return false
		}
	}
	return true
}

//build block committee info part
func (conR *ConsensusReactor) BuildCommitteeInfoFromMember(system *bls.System, cms []CommitteeMember) []block.CommitteeInfo {
	cis := []block.CommitteeInfo{}
	for _, cm := range cms {
		ci := block.NewCommitteeInfo(cm.Name, crypto.FromECDSAPub(&cm.PubKey), cm.NetAddr,
			system.PubKeyToBytes(cm.CSPubKey), uint32(cm.CSIndex))
		cis = append(cis, *ci)
	}
	return (cis)
}

//de-serialize the block committee info part
func (conR *ConsensusReactor) BuildCommitteeMemberFromInfo(system *bls.System, cis []block.CommitteeInfo) []CommitteeMember {
	cms := []CommitteeMember{}
	for _, ci := range cis {
		cm := NewCommitteeMember()

		pubKey, err := crypto.UnmarshalPubkey(ci.PubKey)
		if err != nil {
			panic(err)
		}
		cm.Name = ci.Name
		cm.PubKey = *pubKey
		cm.NetAddr = ci.NetAddr

		CSPubKey, err := system.PubKeyFromBytes(ci.CSPubKey)
		if err != nil {
			panic(err)
		}
		cm.CSPubKey = CSPubKey
		cm.CSIndex = int(ci.CSIndex)

		cms = append(cms, *cm)
	}
	return (cms)
}

//build block committee info part
func (conR *ConsensusReactor) MakeBlockCommitteeInfo(system *bls.System, cms []CommitteeMember) []block.CommitteeInfo {
	cis := []block.CommitteeInfo{}

	for _, cm := range cms {
		ci := block.NewCommitteeInfo(cm.Name, crypto.FromECDSAPub(&cm.PubKey), cm.NetAddr,
			system.PubKeyToBytes(cm.CSPubKey), uint32(cm.CSIndex))
		cis = append(cis, *ci)
	}
	return (cis)
}

// MBlock Routine
//=================================================

type BlockType int

const (
	KBlockType        BlockType = 1
	MBlockType        BlockType = 2
	StopCommitteeType BlockType = 255 //special message to stop pacemake, not a block
)

// proposed block info
type ProposedBlockInfo struct {
	ProposedBlock *block.Block
	Stage         *state.Stage
	Receipts      *tx.Receipts
	txsToRemoved  func() bool
	txsToReturned func() bool
	CheckPoint    int
	BlockType     BlockType
}

func (pb *ProposedBlockInfo) String() string {
	if pb == nil {
		return "ProposedBlockInfo(nil)"
	}
	if pb.ProposedBlock != nil {
		return "ProposedBlockInfo[block: " + pb.ProposedBlock.String() + "]"
	} else {
		return "ProposedBlockInfo[block: nil]"
	}
}

// Build MBlock
func (conR *ConsensusReactor) BuildMBlock(parentBlock *block.Block) *ProposedBlockInfo {
	best := parentBlock
	now := uint64(time.Now().Unix())
	/*
		TODO: better check this, comment out temporarily
		if conR.curHeight != int64(best.Header().Number()) {
			conR.logger.Error("Proposed block parent is not current best block")
			return nil
		}
	*/

	startTime := mclock.Now()
	pool := txpool.GetGlobTxPoolInst()
	if pool == nil {
		conR.logger.Error("get tx pool failed ...")
		panic("get tx pool failed ...")
		return nil
	}

	txs := pool.Executables()
	conR.logger.Debug("get the executables from txpool", "size", len(txs))
	var txsInBlk []*tx.Transaction
	txsToRemoved := func() bool {
		for _, tx := range txsInBlk {
			pool.Remove(tx.ID())
		}
		return true
	}
	txsToReturned := func() bool {
		for _, tx := range txsInBlk {
			pool.Add(tx)
		}
		return true
	}

	p := packer.GetGlobPackerInst()
	if p == nil {
		conR.logger.Error("get packer failed ...")
		panic("get packer failed")
		return nil
	}

	gasLimit := p.GasLimit(best.Header().GasLimit())
	flow, err := p.Mock(best.Header(), now, gasLimit)
	if err != nil {
		conR.logger.Error("mock packer", "error", err)
		return nil
	}

	//create checkPoint before build block
	state, err := conR.stateCreator.NewState(best.Header().StateRoot())
	if err != nil {
		conR.logger.Error("revert state failed ...", "error", err)
		return nil
	}
	checkPoint := state.NewCheckpoint()

	for _, tx := range txs {
		if err := flow.Adopt(tx); err != nil {
			if packer.IsGasLimitReached(err) {
				break
			}
			if packer.IsTxNotAdoptableNow(err) {
				continue
			}
			txsInBlk = append(txsInBlk, tx)
		}
	}

	newBlock, stage, receipts, err := flow.Pack(&conR.myPrivKey, block.BLOCK_TYPE_M_BLOCK, conR.lastKBlockHeight)
	if err != nil {
		conR.logger.Error("build block failed", "error", err)
		return nil
	}

	execElapsed := mclock.Now() - startTime
	conR.logger.Info("MBlock built", "height", newBlock.Header().Number(), "Txs", len(newBlock.Txs), "txsInBlk", len(txsInBlk), "elapseTime", execElapsed)
	return &ProposedBlockInfo{newBlock, stage, &receipts, txsToRemoved, txsToReturned, checkPoint, MBlockType}
}

func (conR *ConsensusReactor) BuildKBlock(parentBlock *block.Block, data *block.KBlockData, rewards []powpool.PowReward) *ProposedBlockInfo {
	best := parentBlock
	now := uint64(time.Now().Unix())
	/*
		TODO: better check this, comment out temporarily
		if conR.curHeight != int64(best.Header().Number()) {
			conR.logger.Warn("Proposed block parent is not current best block")
			return nil
		}
	*/

	conR.logger.Info("Start to build KBlock", "nonce", data.Nonce)
	startTime := mclock.Now()

	// build miner meter reward
	txs := conR.GetKBlockRewardTxs(rewards)
	lastKBlockHeight := parentBlock.Header().LastKBlockHeight()
	stats, err := conR.calcStatistics(lastKBlockHeight, parentBlock.Header().Number())
	if err != nil {
		// TODO: do something about this
		conR.logger.Info("no slash statistics need to info")
	} else {
		statsTx := conR.BuildStatisticsTx(stats)
		txs = append(txs, statsTx)
	}

	if tx := conR.TryBuildAuctionTxs(uint64(best.Header().Number()+1), uint64(best.GetBlockEpoch()+1)); tx != nil {
		txs = append(txs, tx)
	}

	pool := txpool.GetGlobTxPoolInst()
	if pool == nil {
		conR.logger.Error("get tx pool failed ...")
		panic("get tx pool failed ...")
		return nil
	}
	txsToRemoved := func() bool {
		return true
	}
	txsToReturned := func() bool {
		return true
	}

	p := packer.GetGlobPackerInst()
	if p == nil {
		conR.logger.Warn("get packer failed ...")
		panic("get packer failed")
		return nil
	}

	gasLimit := p.GasLimit(best.Header().GasLimit())
	flow, err := p.Mock(best.Header(), now, gasLimit)
	if err != nil {
		conR.logger.Warn("mock packer", "error", err)
		return nil
	}

	//create checkPoint before build block
	state, err := conR.stateCreator.NewState(best.Header().StateRoot())
	if err != nil {
		conR.logger.Error("revert state failed ...", "error", err)
		return nil
	}
	checkPoint := state.NewCheckpoint()

	for _, tx := range txs {
		if err := flow.Adopt(tx); err != nil {
			if packer.IsGasLimitReached(err) {
				break
			}
			if packer.IsTxNotAdoptableNow(err) {
				continue
			}
		}
	}

	newBlock, stage, receipts, err := flow.Pack(&conR.myPrivKey, block.BLOCK_TYPE_K_BLOCK, conR.lastKBlockHeight)
	if err != nil {
		conR.logger.Error("build block failed...", "error", err)
		return nil
	}

	//serialize KBlockData
	newBlock.SetKBlockData(*data)

	execElapsed := mclock.Now() - startTime
	conR.logger.Info("KBlock built", "height", conR.curHeight, "elapseTime", execElapsed)
	return &ProposedBlockInfo{newBlock, stage, &receipts, txsToRemoved, txsToReturned, checkPoint, KBlockType}
}

func (conR *ConsensusReactor) BuildStopCommitteeBlock(parentBlock *block.Block) *ProposedBlockInfo {
	best := parentBlock
	now := uint64(time.Now().Unix())

	startTime := mclock.Now()
	pool := txpool.GetGlobTxPoolInst()
	if pool == nil {
		conR.logger.Error("get tx pool failed ...")
		panic("get tx pool failed ...")
		return nil
	}

	p := packer.GetGlobPackerInst()
	if p == nil {
		conR.logger.Error("get packer failed ...")
		panic("get packer failed")
		return nil
	}

	txsToRemoved := func() bool {
		// Kblock does not need to clean up txs now
		return true
	}
	txsToReturned := func() bool {
		return true
	}

	gasLimit := p.GasLimit(best.Header().GasLimit())
	flow, err := p.Mock(best.Header(), now, gasLimit)
	if err != nil {
		conR.logger.Error("mock packer", "error", err)
		return nil
	}

	newBlock, stage, receipts, err := flow.Pack(&conR.myPrivKey, block.BLOCK_TYPE_S_BLOCK, conR.lastKBlockHeight)
	if err != nil {
		conR.logger.Error("build block failed", "error", err)
		return nil
	}

	execElapsed := mclock.Now() - startTime
	conR.logger.Info("Stop Committee Block built", "height", conR.curHeight, "elapseTime", execElapsed)
	return &ProposedBlockInfo{newBlock, stage, &receipts, txsToRemoved, txsToReturned, 0, StopCommitteeType}
}

//=================================================
// handle KBlock info info message from node

type RecvKBlockInfo struct {
	Height           int64
	LastKBlockHeight uint32
	Nonce            uint64
	Epoch            uint64
}

func (conR *ConsensusReactor) HandleRecvKBlockInfo(ki RecvKBlockInfo) error {
	best := conR.chain.BestBlock()

	if ki.Height != int64(best.Header().Number()) {
		conR.logger.Info("kblock info is ignored ...", "received hight", ki.Height, "my best", best.Header().Number())
		return nil
	}

	if best.Header().BlockType() != block.BLOCK_TYPE_K_BLOCK {
		conR.logger.Info("best block is not kblock")
		return nil
	}

	// can only handle kblock info when pacemaker stopped
	if conR.csPacemaker.IsStopped() == false {
		time.AfterFunc(1*time.Second, func() {
			conR.schedulerQueue <- func() { conR.RcvKBlockInfoQueue <- ki }
		})
		conR.csPacemaker.Stop()
		conR.logger.Info("pacemaker is not fully stopped, wait for another sec ...")
		return nil
	}

	conR.logger.Info("received KBlock ...", "height", ki.Height, "lastKBlockHeight", ki.LastKBlockHeight, "nonce", ki.Nonce, "epoch", ki.Epoch)

	// Now handle this nonce. Exit the committee if it is still in.
	conR.exitCurCommittee()

	// update last kblock height with current Height sine kblock is handled
	conR.UpdateLastKBlockHeight(best.Header().Number())

	// run new one.
	conR.UpdateCurDelegates()
	conR.ConsensusHandleReceivedNonce(ki.Height, ki.Nonce, ki.Epoch, false)
	return nil
}

func (conR *ConsensusReactor) HandleKBlockData(kd block.KBlockData) {
	conR.kBlockData = &kd
}

//========================================================
func (conR *ConsensusReactor) PreCommitBlock(blkInfo *ProposedBlockInfo) bool {
	blk := blkInfo.ProposedBlock
	stage := blkInfo.Stage
	receipts := blkInfo.Receipts

	height := uint64(blk.Header().Number())

	// TODO: temporary remove
	// if conR.csPacemaker.blockLocked.Height != height+1 {
	// conR.logger.Error(fmt.Sprintf("finalizeCommitBlock(H:%v): Invalid height. bLocked Height:%v, curRround: %v", height, conR.csPacemaker.blockLocked.Height, conR.curRound))
	// return false
	// }
	conR.logger.Debug("Try to pre-commit block", "block", blk.Oneliner())

	// similar to node.processBlock
	// startTime := mclock.Now()

	if _, err := stage.Commit(); err != nil {
		conR.logger.Error("failed to commit state", "err", err)
		return false
	}

	/*****
		batch := logdb.GetGlobalLogDBInstance().Prepare(blk.Header())
		for i, tx := range blk.Transactions() {
			origin, _ := tx.Signer()
			txBatch := batch.ForTransaction(tx.ID(), origin)
			for _, output := range (*(*receipts)[i]).Outputs {
				txBatch.Insert(output.Events, output.Transfers)
			}
		}

		if err := batch.Commit(); err != nil {
			conR.logger.Error("commit logs failed ...", "err", err)
			return false
		}
	******/
	// fmt.Println("Calling AddBlock from consensus_block.PrecommitBlock, newblock=", blk.Header().ID())
	fork, err := conR.chain.AddBlock(blk, *receipts, false)
	if err != nil {
		conR.logger.Warn("add block failed ...", "err", err)
		return false
	}

	// unlike processBlock, we do not need to handle fork
	if fork != nil {
		//panic(" chain is in forked state, something wrong")
		//return false
		// process fork????
		if len(fork.Branch) > 0 {
			out := fmt.Sprintf("Fork Happened ... fork(Ancestor=%s, Branch=%s), bestBlock=%s", fork.Ancestor.ID().String(), fork.Branch[0].ID().String(), conR.chain.BestBlock().Header().ID().String())
			conR.logger.Warn(out)
			panic(out)
		}
	}

	// now only Mblock remove the txs from txpool
	blkInfo.txsToRemoved()

	// commitElapsed := mclock.Now() - startTime

	blocksCommitedCounter.Inc()
	conR.logger.Info("block precommited", "height", height, "id", blk.Header().ID())
	return true
}

// finalize the block with its own QC
func (conR *ConsensusReactor) FinalizeCommitBlock(blkInfo *ProposedBlockInfo, bestQC *block.QuorumCert) error {
	blk := blkInfo.ProposedBlock
	//stage := blkInfo.Stage
	receipts := blkInfo.Receipts

	// TODO: temporary remove
	// if conR.csPacemaker.blockLocked.Height != height+1 {
	// conR.logger.Error(fmt.Sprintf("finalizeCommitBlock(H:%v): Invalid height. bLocked Height:%v, curRround: %v", height, conR.csPacemaker.blockLocked.Height, conR.curRound))
	// return false
	// }
	conR.logger.Debug("Try to finalize block", "block", blk.Oneliner())

	/*******************
	// similar to node.processBlock
	startTime := mclock.Now()

	if _, err := stage.Commit(); err != nil {
		conR.logger.Error("failed to commit state", "err", err)
		return false
	}

	*****/
	batch := logdb.GetGlobalLogDBInstance().Prepare(blk.Header())
	for i, tx := range blk.Transactions() {
		origin, _ := tx.Signer()
		txBatch := batch.ForTransaction(tx.ID(), origin)
		for _, output := range (*(*receipts)[i]).Outputs {
			txBatch.Insert(output.Events, output.Transfers)
		}
	}

	if err := batch.Commit(); err != nil {
		conR.logger.Error("commit logs failed ...", "err", err)
		return err
	}
	// fmt.Println("Calling AddBlock from consensus_block.FinalizeCommitBlock, newBlock=", blk.Header().ID())
	if blk.Header().Number() <= conR.chain.BestBlock().Header().Number() {
		return errKnownBlock
	}
	fork, err := conR.chain.AddBlock(blk, *receipts, true)
	if err != nil {
		conR.logger.Warn("add block failed ...", "err", err)
		return err
	}

	// unlike processBlock, we do not need to handle fork
	if fork != nil {
		//panic(" chain is in forked state, something wrong")
		//return false
		// process fork????
		if len(fork.Branch) > 0 {
			out := fmt.Sprintf("Fork Happened ... fork(Ancestor=%s, Branch=%s), bestBlock=%s", fork.Ancestor.ID().String(), fork.Branch[0].ID().String(), conR.chain.BestBlock().Header().ID().String())
			conR.logger.Warn(out)
			panic(out)
		}
	}
	/*****

		// now only Mblock remove the txs from txpool
		blkInfo.txsToRemoved()

		commitElapsed := mclock.Now() - startTime

		blocksCommitedCounter.Inc()
	******/
	// Save bestQC
	conR.chain.SetBestQCCandidate(bestQC)
	conR.chain.UpdateBestQC()

	// XXX: broadcast the new block to all peers
	comm.GetGlobCommInst().BroadcastBlock(blk)
	// successfully added the block, update the current hight of consensus
	conR.logger.Info("Block committed", "height", blk.Header().Number(), "id", blk.Header().ID())
	fmt.Println(blk.String())
	conR.UpdateHeight(int64(conR.chain.BestBlock().Header().Number()))

	return nil
}
