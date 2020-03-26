package consensus

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/ethereum/go-ethereum/rlp"

	"github.com/dfinlab/meter/block"
	"github.com/dfinlab/meter/co"
	bls "github.com/dfinlab/meter/crypto/multi_sig"
	cmn "github.com/dfinlab/meter/libs/common"
	types "github.com/dfinlab/meter/types"
	"github.com/inconshreveable/log15"
)

const (
	RoundInterval        = 2 * time.Second
	RoundTimeoutInterval = 30 * time.Second // move the timeout from 10 to 30 secs.

	MIN_MBLOCKS_AN_EPOCH = uint64(6)
)

var (
	qcInit pmQuorumCert
	bInit  pmBlock
)

type PMSignature struct {
	index     int64
	msgHash   [32]byte
	signature bls.Signature
}

type Pacemaker struct {
	csReactor   *ConsensusReactor //global reactor info
	proposalMap map[uint64]*pmBlock
	logger      log15.Logger

	// Current round (current_round - highest_qc_round determines the timeout).
	// Current round is basically max(highest_qc_round, highest_received_tc, highest_local_tc) + 1
	// update_current_round take care of updating current_round and sending new round event if
	// it changes
	currentRound     uint64
	lastVotingHeight uint64
	QCHigh           *pmQuorumCert

	blockLeaf     *pmBlock
	blockExecuted *pmBlock
	blockLocked   *pmBlock

	startHeight uint64

	pacemakerMsgCh chan receivedConsensusMessage
	roundTimeoutCh chan PMRoundTimeoutInfo
	stopCh         chan *PMStopInfo
	beatCh         chan *PMBeatInfo

	voterBitArray *cmn.BitArray
	voteSigs      []*PMSignature

	roundTimer         *time.Timer
	timeoutCertManager *PMTimeoutCertManager
	timeoutCert        *PMTimeoutCert
	timeoutCounter     uint64

	pendingList  *PendingList
	msgRelayInfo *PMProposalInfo

	myActualCommitteeIndex int //record my index in actualcommittee
	minMBlocks             uint64
	goes                   co.Goes
	stopped                bool
}

func NewPaceMaker(conR *ConsensusReactor) *Pacemaker {
	p := &Pacemaker{
		csReactor: conR,
		logger:    log15.New("pkg", "pacemaker"),

		pacemakerMsgCh: make(chan receivedConsensusMessage, 128),
		stopCh:         make(chan *PMStopInfo, 2),
		beatCh:         make(chan *PMBeatInfo, 2),
		roundTimeoutCh: make(chan PMRoundTimeoutInfo, 2),
		roundTimer:     nil,
		proposalMap:    make(map[uint64]*pmBlock, 1000), // TODO:better way?
		pendingList:    NewPendingList(),
		msgRelayInfo:   NewPMProposalInfo(),
		timeoutCounter: 0,
		stopped:        false,
	}
	p.timeoutCertManager = newPMTimeoutCertManager(p)
	// p.stopCleanup()
	return p
}

func (p *Pacemaker) CreateLeaf(parent *pmBlock, qc *pmQuorumCert, height uint64, round uint64) *pmBlock {
	parentBlock, err := block.BlockDecodeFromBytes(parent.ProposedBlock)
	if err != nil {
		panic("Error decode the parent block")
	}
	p.logger.Info(fmt.Sprintf("CreateLeaf: height=%v, round=%v, QCHight=%v, QCRound=%v, ParentHeight=%v, ParentRound=%v", height, round, qc.QC.QCHeight, qc.QC.QCRound, parent.Height, parent.Round))
	// after kblock is proposed, we should propose 2 rounds of stopcommitteetype block
	// to finish the pipeline. This mechnism guranttee kblock get into block server.

	// resend the previous kblock as special type to get vote stop message to get vote
	// This proposal will not get into block database
	if parent.ProposedBlockType == KBlockType || parent.ProposedBlockType == StopCommitteeType {
		p.logger.Info(fmt.Sprintf("Proposed Stop pacemaker message: height=%v, round=%v", height, round))
		info, blockBytes := p.proposeStopCommitteeBlock(parentBlock, height, round, qc)
		b := &pmBlock{
			Height:  height,
			Round:   round,
			Parent:  parent,
			Justify: qc,

			ProposedBlockInfo: info,
			SuccessProcessed:  true,
			ProposedBlock:     blockBytes,
			ProposedBlockType: info.BlockType,
		}
		fmt.Print(b.ToString())
		return b
	}

	info, blockBytes := p.proposeBlock(parentBlock, height, round, qc, true)
	p.logger.Info(fmt.Sprintf("Proposed Block:\n%v", info.ProposedBlock.CompactString()))

	b := &pmBlock{
		Height:  height,
		Round:   round,
		Parent:  parent,
		Justify: qc,

		ProposedBlockInfo: info, //save to local
		SuccessProcessed:  true,
		ProposedBlock:     blockBytes,
		ProposedBlockType: info.BlockType,
	}

	// fmt.Print(b.ToString())
	return b
}

// b_exec  b_lock   b <- b' <- b"  b*
func (p *Pacemaker) Update(bnew *pmBlock) error {

	var block, blockPrime, blockPrimePrime *pmBlock
	//now pipeline full, roll this pipeline first
	blockPrimePrime = bnew.Justify.QCNode
	if blockPrimePrime == nil {
		p.logger.Warn("blockPrimePrime is empty, early termination of Update")
		return nil
	}
	blockPrime = blockPrimePrime.Justify.QCNode
	if blockPrime == nil {
		p.logger.Warn("blockPrime is empty, early termination of Update")
		return nil
	}
	block = blockPrime.Justify.QCNode
	if block == nil {
		//bnew Justify is already higher than current QCHigh
		p.UpdateQCHigh(bnew.Justify)
		p.logger.Warn("block is empty, early termination of Update")
		return nil
	}

	p.logger.Debug(fmt.Sprintf("bnew = %v", bnew.ToString()))
	p.logger.Debug(fmt.Sprintf("b\"   = %v", blockPrimePrime.ToString()))
	p.logger.Debug(fmt.Sprintf("b'   = %v", blockPrime.ToString()))
	p.logger.Debug(fmt.Sprintf("b    = %v", block.ToString()))

	// pre-commit phase on b"
	p.UpdateQCHigh(bnew.Justify)

	if blockPrime.Height > p.blockLocked.Height {
		p.blockLocked = blockPrime // commit phase on b'
	}

	/* commit requires direct parent */
	if (blockPrimePrime.Parent != blockPrime) ||
		(blockPrime.Parent != block) {
		return nil
	}

	commitReady := []*pmBlock{}
	for b := block; b.Height > p.blockExecuted.Height; b = b.Parent {
		// XXX: b must be prepended the slice, so we can commit blocks in order
		commitReady = append([]*pmBlock{b}, commitReady...)
	}
	p.OnCommit(commitReady)

	p.blockExecuted = block // decide phase on b
	return nil
}

// TBD: how to emboy b.cmd
func (p *Pacemaker) Execute(b *pmBlock) error {
	// p.csReactor.logger.Info("Exec cmd:", "height", b.Height, "round", b.Round)

	return nil
}

func (p *Pacemaker) OnCommit(commitReady []*pmBlock) error {
	for _, b := range commitReady {
		p.csReactor.logger.Info("OnCommit", "height", b.Height, "round", b.Round)

		// TBD: how to handle this case???
		if b.SuccessProcessed == false {
			p.csReactor.logger.Error("Process this propsoal failed, possible my states are wrong", "height", b.Height, "round", b.Round)
			continue
		}
		// commit the approved block
		bestQC := p.proposalMap[b.Height+1].Justify.QC
		if err := p.csReactor.FinalizeCommitBlock(b.ProposedBlockInfo, bestQC); err != nil {
			p.csReactor.logger.Warn("Commit block failed ...", "error", err)

			// same block can be imported fromm P2P, we consider it as success
			if err.Error() != "block already exists" {
				//revert to checkpoint
				best := p.csReactor.chain.BestBlock()
				state, err := p.csReactor.stateCreator.NewState(best.Header().StateRoot())
				if err != nil {
					panic(fmt.Sprintf("revert the state faild ... %v", err))
				}
				state.RevertTo(b.ProposedBlockInfo.CheckPoint)
			}
		}

		p.Execute(b) //b.cmd

		if b.ProposedBlockType == KBlockType {
			p.csReactor.logger.Info("committed a kblock, stop pacemaker", "height", b.Height, "round", b.Round)
			p.SendKblockInfo(b)
			p.Stop()
		}

		// BUG FIX: normally proposal message are cleaned once it is committed. It is ok because this proposal
		// is not needed any more. Only in one case, if somebody queries the more old message, we can not give.
		// so proposals are kept in this committee and clean all of them at the stopping of pacemaker.
		// remove this pmBlock from map.
		//delete(p.proposalMap, b.Height)
	}

	return nil
}

func (p *Pacemaker) OnPreCommitBlock(b *pmBlock) error {
	// TBD: how to handle this case???
	if b.SuccessProcessed == false {
		p.csReactor.logger.Error("Process this propsoal failed, possible my states are wrong", "height", b.Height, "round", b.Round)
		return errors.New("Process this propsoal failed, precommit skipped")
	}
	if ok := p.csReactor.PreCommitBlock(b.ProposedBlockInfo); ok != true {
		return errors.New("precommit failed")
	}
	// p.csReactor.logger.Info("PreCommitted block", "height", b.Height, "round", b.Round)
	return nil
}

func (p *Pacemaker) OnReceiveProposal(proposalMsg *PMProposalMessage, from types.NetAddress) error {
	msgHeader := proposalMsg.CSMsgCommonHeader
	height := uint64(msgHeader.Height)
	round := uint64(msgHeader.Round)

	if height < p.blockLocked.Height {
		p.logger.Info("recved proposal with height < bLocked.height, ignore ...", "height", height, "bLocked.height", p.blockLocked.Height)
		return nil
	}

	// decode block to get qc
	blk, err := block.BlockDecodeFromBytes(proposalMsg.ProposedBlock)
	if err != nil {
		return errors.New("can not decode proposed block")
	}

	// skip invalid proposal
	if blk.Header().Number() != uint32(height) {
		p.logger.Error("invalid proposal: height mismatch", "proposalHeight", height, "proposedBlockHeight", blk.Header().Number())
		return errors.New("invalid proposal: height mismatch")
	}

	qc := blk.QC
	p.logger.Info("start to handle received proposal ", "height", msgHeader.Height, "round", msgHeader.Round,
		"parentHeight", proposalMsg.ParentHeight, "parentRound", proposalMsg.ParentRound,
		"qc", qc.CompactString(), "ID", blk.Header().ID())

	// address parent
	parent := p.AddressBlock(proposalMsg.ParentHeight, proposalMsg.ParentRound)
	if parent == nil {
		// put this proposal to pending list, and sent out query
		if err := p.pendingProposal(proposalMsg.ParentHeight, proposalMsg.ParentRound, proposalMsg, from); err != nil {
			p.logger.Error("handle pending proposoal failed", "error", err)
		}
		return errors.New("can not address parent")
	}

	// address qcNode
	// TODO: qc should be verified before it is used
	qcNode := p.AddressBlock(qc.QCHeight, qc.QCRound)
	if qcNode == nil {
		p.logger.Warn("OnReceiveProposal: can not address qcNode")

		// put this proposal to pending list, and sent out query
		if err := p.pendingProposal(qc.QCHeight, qc.QCRound, proposalMsg, from); err != nil {
			p.logger.Error("handle pending proposoal failed", "error", err)
		}
		return errors.New("can not address qcNode")
	}

	// we have qcNode, need to check qcNode and blk.QC is referenced the same
	if match, _ := p.BlockMatchQC(qcNode, qc); match == true {
		p.logger.Debug("addressed qcNode ...", "qcHeight", qc.QCHeight, "qcRound", qc.QCRound)
	} else {
		// possible fork !!! TODO: handle?
		p.logger.Error("qcNode doesn't match qc from proposal, potential fork happens...", "qcHeight", qc.QCHeight, "qcRound", qc.QCRound)

		// TBD: How to handle this??
		// if this block does not have Qc yet, revertTo previous
		// if this block has QC, The real one need to be replaced
		// anyway, get the new one.
		// put this proposal to pending list, and sent out query
		if err := p.pendingProposal(qc.QCHeight, qc.QCRound, proposalMsg, from); err != nil {
			p.logger.Error("handle pending proposoal failed", "error", err)
		}
		return errors.New("qcNode doesn't match qc from proposal, potential fork ")
	}

	// create justify node
	justify := newPMQuorumCert(qc, qcNode)

	// revert the proposals if I'm not the round proposer and I received a proposal with a valid TC
	validTimeout := p.verifyTimeoutCert(proposalMsg.TimeoutCert, height, round)
	if validTimeout {
		p.revertTo(height)
	}

	// update the proposalMap only in these this scenario: not tracked
	if _, tracked := p.proposalMap[height]; !tracked {
		p.proposalMap[height] = &pmBlock{
			ProposalMessage:   proposalMsg,
			Height:            height,
			Round:             round,
			Parent:            parent,
			Justify:           justify,
			ProposedBlock:     proposalMsg.ProposedBlock,
			ProposedBlockType: proposalMsg.ProposedBlockType,
		}
	}

	bnew := p.proposalMap[height]
	if ((bnew.Height > p.lastVotingHeight) &&
		(p.IsExtendedFromBLocked(bnew) || bnew.Justify.QC.QCHeight > p.blockLocked.Height)) || validTimeout {

		if validTimeout {
			p.updateCurrentRound(bnew.Round, UpdateOnTimeoutCertProposal)
		} else {
			p.updateCurrentRound(bnew.Round, UpdateOnRegularProposal)
		}

		// parent got QC, pre-commit
		justify := p.proposalMap[bnew.Justify.QC.QCHeight] //Justify.QCNode
		if (justify != nil) && (justify.Height > p.startHeight) {
			p.OnPreCommitBlock(justify)
		}

		if err := p.ValidateProposal(bnew); err != nil {
			p.logger.Error("HELP: Validate Proposal failed", "error", err)
			return err
		}

		msg, _ := p.BuildVoteForProposalMessage(proposalMsg, blk.Header().ID(), blk.Header().TxsRoot(), blk.Header().StateRoot())
		// send vote message to leader
		p.SendConsensusMessage(uint64(proposalMsg.CSMsgCommonHeader.Round), msg, false)
		p.lastVotingHeight = bnew.Height
	}

	p.Update(bnew)
	return nil
}

func (p *Pacemaker) OnReceiveVote(voteMsg *PMVoteForProposalMessage) error {
	msgHeader := voteMsg.CSMsgCommonHeader

	height := uint64(msgHeader.Height)
	round := uint64(msgHeader.Round)
	if round < p.currentRound {
		p.logger.Info("expired voteForProposal message, dropped ...", "currentRound", p.currentRound, "voteRound", round)
	}

	b := p.AddressBlock(height, round)
	if b == nil {
		return errors.New("can not address block")
	}

	err := p.collectVoteSignature(voteMsg)
	if err != nil {
		return err
	}
	voteCount := len(p.voteSigs)
	if MajorityTwoThird(voteCount, p.csReactor.committeeSize) == false {
		// if voteCount < p.csReactor.committeeSize {
		// not reach 2/3
		p.csReactor.logger.Debug("not reach majority", "committeeSize", p.csReactor.committeeSize, "count", voteCount)
		return nil
	} else {
		p.csReactor.logger.Info("reached majority", "committeeSize", p.csReactor.committeeSize, "count", voteCount)
	}

	//reach 2/3 majority, trigger the pipeline cmd
	qc, err := p.generateNewQCNode(b)
	if err != nil {
		return err
	}

	changed := p.UpdateQCHigh(qc)

	if changed == true {
		pmRoleGauge.Set(1)
		// if QC is updated, relay it to the next proposer
		p.OnNextSyncView(qc.QC.QCHeight+1, qc.QC.QCRound+1, HigherQCSeen, nil)

	}
	return nil
}

func (p *Pacemaker) OnPropose(b *pmBlock, qc *pmQuorumCert, height uint64, round uint64) (*pmBlock, error) {
	// clean signature cache
	p.voterBitArray = cmn.NewBitArray(p.csReactor.committeeSize)
	p.voteSigs = make([]*PMSignature, 0)

	bnew := p.CreateLeaf(b, qc, height, round)
	if bnew.Height != height {
		p.logger.Error("proposed height mismatch", "expectedHeight", height, "proposedHeight", bnew.Height)
		return nil, errors.New("proposed height mismatch")
	}

	msg, err := p.BuildProposalMessage(height, round, bnew, p.timeoutCert)
	if err != nil {
		p.logger.Error("could not build proposal message", "err", err)
		return nil, err
	}
	p.timeoutCert = nil

	// create slot in proposalMap directly, instead of sendmsg to self.
	bnew.ProposalMessage = msg
	p.proposalMap[height] = bnew

	//send proposal to all include myself
	p.SendConsensusMessage(round, msg, true)

	return bnew, nil
}

func (p *Pacemaker) UpdateQCHigh(qc *pmQuorumCert) bool {
	updated := false
	oqc := p.QCHigh
	if qc.QC.QCHeight > p.QCHigh.QC.QCHeight {
		p.QCHigh = qc
		p.blockLeaf = p.QCHigh.QCNode
		updated = true
	}
	p.logger.Debug("After update QCHigh", "updated", updated, "from", oqc.ToString(), "to", p.QCHigh.ToString())

	return updated
}

func (p *Pacemaker) OnBeat(height uint64, round uint64, reason beatReason) error {
	if p.QCHigh != nil && p.QCHigh.QC != nil && height <= p.QCHigh.QC.QCHeight && reason == BeatOnTimeout {
		return p.OnTimeoutBeat(height, round, reason)
	}
	p.logger.Info("--------------------------------------------------")
	p.logger.Info(fmt.Sprintf("      OnBeat Round:%v, Height:%v, Reason:%v        ", round, height, reason.String()))
	p.logger.Info("--------------------------------------------------")

	// parent already got QC, pre-commit it
	//b := p.QCHigh.QCNode
	b := p.proposalMap[p.QCHigh.QC.QCHeight]

	if b.Height > p.startHeight {
		p.OnPreCommitBlock(b)
	}

	if reason == BeatOnInit {
		// only reset the round timer at initialization
		p.resetRoundTimer(round, TimerInit)
	}
	p.updateCurrentRound(round, UpdateOnBeat)
	if p.csReactor.amIRoundProproser(round) {
		pmRoleGauge.Set(2)
		p.csReactor.logger.Info("OnBeat: I am round proposer", "round", round)

		bleaf, err := p.OnPropose(p.blockLeaf, p.QCHigh, height, round)
		if err != nil {
			return err
		}
		if bleaf == nil {
			return errors.New("propose failed")
		}

		p.blockLeaf = bleaf
	} else {
		pmRoleGauge.Set(1)
		p.csReactor.logger.Info("OnBeat: I am NOT round proposer", "round", round)
	}
	return nil
}

func (p *Pacemaker) OnTimeoutBeat(height uint64, round uint64, reason beatReason) error {
	p.logger.Info("--------------------------------------------------")
	p.logger.Info(fmt.Sprintf("      OnTimeoutBeat Round:%v, Height:%v, Reason:%v        ", round, height, reason.String()))
	p.logger.Info("--------------------------------------------------")
	// parent already got QC, pre-commit it
	//b := p.QCHigh.QCNode
	parent := p.proposalMap[height-1]
	replaced := p.proposalMap[height]
	if parent == nil {
		p.logger.Error("missing parent proposal", "parentHeight", height-1, "height", height, "round", round)
		return errors.New("missing parent proposal")
	}
	if replaced == nil {
		p.logger.Error("missing qc for proposal", "parentHeight", height-1, "height", height, "round", round)
		return errors.New("missing qc for proposal")
	}

	if reason == BeatOnInit {
		// only reset the round timer at initialization
		p.resetRoundTimer(round, TimerInit)
	}
	if p.csReactor.amIRoundProproser(round) {
		pmRoleGauge.Set(2)
		p.csReactor.logger.Info("OnBeat: I am round proposer", "round", round)

		bleaf, err := p.OnPropose(parent, replaced.Justify, height, round)
		if err != nil {
			return err
		}
		if bleaf == nil {
			return errors.New("propose failed")
		}
	} else {
		pmRoleGauge.Set(1)
		p.csReactor.logger.Info("OnBeat: I am NOT round proposer", "round", round)
	}
	return nil
}

func (p *Pacemaker) OnNextSyncView(nextHeight, nextRound uint64, reason NewViewReason, ti *PMRoundTimeoutInfo) error {
	// send new round msg to next round proposer
	msg, err := p.BuildNewViewMessage(nextHeight, nextRound, p.QCHigh, reason, ti)
	if err != nil {
		p.logger.Error("could not build new view message", "err", err)
	}

	p.SendConsensusMessage(nextRound, msg, false)

	return nil
}

func (p *Pacemaker) OnReceiveNewView(newViewMsg *PMNewViewMessage, from types.NetAddress) error {
	header := newViewMsg.CSMsgCommonHeader

	qc := block.QuorumCert{}
	err := rlp.DecodeBytes(newViewMsg.QCHigh, &qc)
	if err != nil {
		p.logger.Error("can not decode qc from new view message", "err", err)
		return nil
	}

	// drop newview if it is old
	if qc.QCHeight < uint64(p.csReactor.curHeight) {
		p.logger.Error("old newview message, dropped ...", "QCheight", qc.QCHeight)
		return nil
	}

	qcNode := p.AddressBlock(qc.QCHeight, qc.QCRound)
	if qcNode == nil {
		p.logger.Error("can not address qcNode", "err", err)
		// put this newView to pending list, and sent out query
		if err := p.pendingNewView(qc.QCHeight, qc.QCRound, newViewMsg, from); err != nil {
			p.logger.Error("handle pending newViewMsg failed", "error", err)
		}
		return nil
	}

	// now have qcNode, check qcNode and blk.QC is referenced the same
	if match, _ := p.BlockMatchQC(qcNode, &qc); match == true {
		p.logger.Debug("addressed qcNode ...", "qcHeight", qc.QCHeight, "qcRound", qc.QCRound)
	} else {
		// possible fork !!! TODO: handle?
		p.logger.Error("qcNode does not match qc from proposal, potential fork happens...", "qcHeight", qc.QCHeight, "qcRound", qc.QCRound)

		// TBD: How to handle this case??
		// if this block does not have Qc yet, revertTo previous
		// if this block has QC, the real one need to be replaced
		// anyway, get the new one.
		// put this newView to pending list, and sent out query
		if err := p.pendingNewView(qc.QCHeight, qc.QCRound, newViewMsg, from); err != nil {
			p.logger.Error("handle pending newViewMsg failed", "error", err)
		}
		return nil
	}

	pmQC := newPMQuorumCert(&qc, qcNode)

	switch newViewMsg.Reason {
	case RoundTimeout:
		height := uint64(header.Height)
		round := uint64(header.Round)
		epoch := header.EpochID
		if !p.csReactor.amIRoundProproser(round) {
			p.logger.Info("Not round proposer, drops the newView timeout ...", "Height", height, "Round", round, "Epoch", epoch)
			return nil
		}

		qcHeight := qc.QCHeight
		qcRound := qc.QCRound
		qcEpoch := qc.EpochID

		// if I don't have the proposal at specified height, query my peer
		if _, ok := p.proposalMap[qcHeight]; ok != true {
			p.logger.Info("Send PMQueryProposal", "height", qcHeight, "round", qcRound, "epoch", qcEpoch)
			if err := p.sendQueryProposalMsg(qcHeight, qcRound, qcEpoch, from); err != nil {
				p.logger.Warn("send PMQueryProposal message failed", "err", err)
			}
		}

		// if peer's height is lower than me, forward all available proposals to fill the gap
		if qcHeight < p.lastVotingHeight {
			// forward missing proposals to peers who just sent new view message with lower expected height
			name := p.csReactor.GetCommitteeMemberNameByIP(from.IP)
			peers := []*ConsensusPeer{newConsensusPeer(name, from.IP, 8670, p.csReactor.magic)}
			tmpHeight := qcHeight
			var proposal *pmBlock
			var ok bool
			for {
				if proposal, ok = p.proposalMap[tmpHeight]; ok != true {
					break
				}
				// if I haven't got the proposal message, stop right away
				if proposal == nil || proposal.ProposalMessage == nil {
					break
				}
				p.logger.Info("peer missed one proposal, forward to it ... ", "height", tmpHeight, "name", name, "ip", from.IP.String())
				p.SendMessageToPeers(proposal.ProposalMessage, peers)
				tmpHeight++
			}
		}

		// now count the timeout
		p.timeoutCertManager.collectSignature(newViewMsg)
		timeoutCount := p.timeoutCertManager.count(newViewMsg.TimeoutHeight, newViewMsg.TimeoutRound)
		if MajorityTwoThird(timeoutCount, p.csReactor.committeeSize) == false {
			p.logger.Info("not reach majority on timeout", "count", timeoutCount, "timeoutHeight", newViewMsg.TimeoutHeight, "timeoutRound", newViewMsg.TimeoutRound, "timeoutCounter", newViewMsg.TimeoutCounter)
		} else {
			p.logger.Info("reached majority on timeout", "count", timeoutCount, "timeoutHeight", newViewMsg.TimeoutHeight, "timeoutRound", newViewMsg.TimeoutRound, "timeoutCounter", newViewMsg.TimeoutCounter)
			p.timeoutCert = p.timeoutCertManager.getTimeoutCert(newViewMsg.TimeoutHeight, newViewMsg.TimeoutRound)
			p.timeoutCertManager.cleanup(newViewMsg.TimeoutHeight, newViewMsg.TimeoutRound)

			// Schedule OnBeat due to timeout
			p.logger.Info("Received a newview with timeoutCert, scheduleOnBeat now", "height", header.Height, "round", header.Round)
			// Now reach timeout consensus on height/round, check myself states
			if (p.QCHigh.QC.QCHeight + 1) < uint64(header.Height) {
				p.logger.Info("Can not OnBeat due to states lagging", "my QCHeight", p.QCHigh.QC.QCHeight, "timeoutCert Height", header.Height)
				return nil
			}
			p.ScheduleOnBeat(uint64(header.Height), uint64(header.Round), BeatOnTimeout, RoundInterval)
		}

	case HigherQCSeen:
		if uint64(header.Round) <= p.currentRound {
			p.logger.Info("expired newview message, dropped ... ", "currentRound", p.currentRound, "newViewNxtRound", header.Round)
			return nil
		}
		changed := p.UpdateQCHigh(pmQC)
		if changed {
			if qc.QCHeight > p.blockLocked.Height {
				// Schedule OnBeat due to New QC
				p.logger.Info("Received a newview with higher QC, scheduleOnBeat now", "qcHeight", qc.QCHeight, "qcRound", qc.QCRound, "onBeatHeight", qc.QCHeight+1, "onBeatRound", qc.QCRound+1)
				p.ScheduleOnBeat(p.QCHigh.QC.QCHeight+1, qc.QCRound+1, BeatOnHigherQC, RoundInterval)
			}
		}
	}
	return nil
}

//Committee Leader triggers
func (p *Pacemaker) Start(newCommittee bool) {
	pmRoleGauge.Set(0)
	pmRunningGauge.Set(1)
	p.csReactor.chain.UpdateBestQC()
	p.csReactor.chain.UpdateLeafBlock()
	blockQC := p.csReactor.chain.BestQC()
	p.logger.Info(fmt.Sprintf("*** Pacemaker start at height %v, QC:%v, newCommittee:%v",
		blockQC.QCHeight, blockQC.String(), newCommittee))

	var round uint64
	height := blockQC.QCHeight
	if newCommittee != true {
		round = blockQC.QCRound
	} else {
		round = 0
	}

	// acutalcommittee is different in each epoch, save my index here
	p.myActualCommitteeIndex = p.csReactor.GetMyActualCommitteeIndex()

	// Hack here. We do not know it is the first pacemaker from beginning
	// But it is not harmful, the worst case only misses one opportunity to propose kblock.
	if p.csReactor.config.InitCfgdDelegates == false {
		p.minMBlocks = MIN_MBLOCKS_AN_EPOCH
	} else {
		p.minMBlocks = p.csReactor.config.EpochMBlockCount
		p.csReactor.config.InitCfgdDelegates = false // clean off InitCfgdDelegates
	}

	p.startHeight = height
	qcNode := p.AddressBlock(height, round)
	if qcNode == nil {
		p.logger.Warn("Started with empty qcNode")
	}
	qcInit = *newPMQuorumCert(blockQC, qcNode)
	bInit = pmBlock{
		Height:        height,
		Round:         round,
		Parent:        nil,
		Justify:       &qcInit,
		ProposedBlock: p.csReactor.LoadBlockBytes(uint32(height)),
	}

	// now assign b_lock b_exec, b_leaf qc_high
	p.blockLocked = &bInit
	p.blockExecuted = &bInit
	p.blockLeaf = &bInit
	p.proposalMap[height] = &bInit
	if qcInit.QCNode == nil {
		qcInit.QCNode = &bInit
	}
	p.QCHigh = &qcInit

	// channels are always up before the start, drain them first
	for len(p.pacemakerMsgCh) > 0 {
		<-p.pacemakerMsgCh
	}
	for len(p.roundTimeoutCh) > 0 {
		<-p.roundTimeoutCh
	}
	for len(p.beatCh) > 0 {
		<-p.beatCh
	}
	for len(p.stopCh) > 0 {
		<-p.stopCh
	}
	p.pendingList.CleanUp()

	// make sure the above cleared proposal can receive again.
	p.msgRelayInfo.CleanUpFrom(height)
	p.stopped = false

	go p.mainLoop()

	p.ScheduleOnBeat(height+1, round, BeatOnInit, 1*time.Second) //delay 1s
}

func (p *Pacemaker) ScheduleOnBeat(height uint64, round uint64, reason beatReason, d time.Duration) bool {
	// p.updateCurrentRound(round, IncRoundOnBeat)
	time.AfterFunc(d, func() {
		p.beatCh <- &PMBeatInfo{height, round, reason}
	})
	return true
}

func (p *Pacemaker) mainLoop() {
	interruptCh := make(chan os.Signal, 1)
	// signal.Notify(interruptCh, syscall.SIGINT, syscall.SIGTERM)

	for {
		var err error
		if p.stopped {
			p.logger.Warn("Pacemaker fully stopped.")
			return
		}
		select {
		case <-p.stopCh:
			p.logger.Warn("Scheduled stop, exit pacemaker now")
			// clean off chain for next committee.
			p.stopCleanup()
			return
		case ti := <-p.roundTimeoutCh:
			err = p.OnRoundTimeout(ti)
		case b := <-p.beatCh:
			err = p.OnBeat(b.height, b.round, b.reason)
		case m := <-p.pacemakerMsgCh:
			switch m.msg.(type) {
			case *PMProposalMessage:
				err = p.OnReceiveProposal(m.msg.(*PMProposalMessage), m.from)
				if err != nil {
					p.logger.Error("processes proposal fails.", "errors", err)
					// 2 errors indicate linking message to pending list for the first time, does not need to check pending
					if (err.Error() != "can not address parent") && (err.Error() != "can not address qcNode") {
						err = p.checkPendingMessages(uint64(m.msg.(*PMProposalMessage).CSMsgCommonHeader.Height))
					}
				} else {
					err = p.checkPendingMessages(uint64(m.msg.(*PMProposalMessage).CSMsgCommonHeader.Height))
				}
			case *PMVoteForProposalMessage:
				err = p.OnReceiveVote(m.msg.(*PMVoteForProposalMessage))
			case *PMNewViewMessage:
				err = p.OnReceiveNewView(m.msg.(*PMNewViewMessage), m.from)
			case *PMQueryProposalMessage:
				err = p.OnReceiveQueryProposal(m.msg.(*PMQueryProposalMessage))
			default:
				p.logger.Warn("Received an message in unknown type")
			}
		case <-interruptCh:
			p.logger.Warn("Interrupt by user, exit now")
			return
		}
		if err != nil {
			p.logger.Error("Error during handling ", "err", err)
		}
	}
}

func (p *Pacemaker) SendKblockInfo(b *pmBlock) error {
	// clean off chain for next committee.
	blk := b.ProposedBlockInfo.ProposedBlock
	if blk.Header().BlockType() == block.BLOCK_TYPE_K_BLOCK {
		data, _ := blk.GetKBlockData()
		info := RecvKBlockInfo{
			Height:           int64(blk.Header().Number()),
			LastKBlockHeight: blk.Header().LastKBlockHeight(),
			Nonce:            data.Nonce,
			Epoch:            blk.QC.EpochID,
		}
		p.csReactor.RcvKBlockInfoQueue <- info

		p.logger.Info("sent kblock info to reactor", "nonce", info.Nonce, "height", info.Height)
	}
	return nil
}

func (p *Pacemaker) stopCleanup() {

	defer func() {
		p.stopped = true
	}()
	p.stopRoundTimer()
	pmRoleGauge.Set(0)

	// clean up propose map
	for _, b := range p.proposalMap {
		delete(p.proposalMap, b.Height)
	}

	//p.goes.Wait()
	p.currentRound = 0
	pmRoundGauge.Set(float64(p.currentRound))
	p.lastVotingHeight = 0
	p.QCHigh = nil
	p.blockLeaf = nil
	p.blockExecuted = nil
	p.blockLocked = nil

	p.logger.Warn("--- Pacemaker stopped successfully")
	pmRunningGauge.Set(0)
}

func (p *Pacemaker) IsStopped() bool {
	return p.stopped
	// return p.QCHigh == nil && p.blockExecuted == nil && p.blockLocked == nil
}

//actions of commites/receives kblock, stop pacemake to next committee
// all proposal txs need to be reclaimed before stop
func (p *Pacemaker) Stop() {
	chain := p.csReactor.chain
	p.logger.Info(fmt.Sprintf("Pacemaker stop requested. \n  Current BestBlock: %v \n  LeafBlock: %v\n  BestQC: %v\n", chain.BestBlock().Oneliner(), chain.LeafBlock().Oneliner(), chain.BestQC().String()))

	// suicide
	if len(p.stopCh) < cap(p.stopCh) {
		p.stopCh <- &PMStopInfo{}
	}
}

func (p *Pacemaker) OnRoundTimeout(ti PMRoundTimeoutInfo) error {
	p.logger.Warn("Round Time Out", "round", ti.round, "counter", p.timeoutCounter)

	p.updateCurrentRound(p.currentRound+1, UpdateOnTimeout)
	newTi := &PMRoundTimeoutInfo{
		height:  p.QCHigh.QC.QCHeight + 1,
		round:   p.currentRound,
		counter: p.timeoutCounter + 1,
	}
	p.OnNextSyncView(p.QCHigh.QC.QCHeight+1, p.currentRound, RoundTimeout, newTi)
	// p.startRoundTimer(ti.height, ti.round+1, ti.counter+1)
	return nil
}

func (p *Pacemaker) updateCurrentRound(round uint64, reason roundUpdateReason) bool {
	updated := (p.currentRound != round)
	switch reason {
	case UpdateOnRegularProposal:
		if round > p.currentRound {
			updated = true
			p.resetRoundTimer(round, TimerInit)
		}
	case UpdateOnTimeoutCertProposal:
		p.resetRoundTimer(round, TimerInit)
	case UpdateOnTimeout:
		p.resetRoundTimer(round, TimerInc)
	}

	if updated {
		p.currentRound = round
		p.logger.Info("* Current round updated", "to", p.currentRound, "reason", reason.String())
		pmRoundGauge.Set(float64(p.currentRound))
		return true
	}
	return false
}

func (p *Pacemaker) startRoundTimer(round uint64, reason roundTimerUpdateReason) {
	if p.roundTimer == nil {
		switch reason {
		case TimerInit:
			p.timeoutCounter = 0
		case TimerInc:
			p.timeoutCounter++
		}
		p.logger.Info("Start round timer", "round", round, "counter", p.timeoutCounter)
		timeoutInterval := RoundTimeoutInterval * (1 << p.timeoutCounter)
		p.roundTimer = time.AfterFunc(timeoutInterval, func() {
			p.roundTimeoutCh <- PMRoundTimeoutInfo{round: round, counter: p.timeoutCounter}
		})
	}
}

func (p *Pacemaker) stopRoundTimer() bool {
	if p.roundTimer != nil {
		p.logger.Info("Stop round timer", "round", p.currentRound)
		p.roundTimer.Stop()
		p.roundTimer = nil
	}
	return true
}

func (p *Pacemaker) resetRoundTimer(round uint64, reason roundTimerUpdateReason) {
	p.stopRoundTimer()
	p.startRoundTimer(round, reason)
}

func (p *Pacemaker) revertTo(revertHeight uint64) {
	p.logger.Info("Start revert", "revertHeight", revertHeight, "current block-leaf", p.blockLeaf.ToString(), "current QCHigh", p.QCHigh.ToString())
	pivot, pivotExist := p.proposalMap[revertHeight]
	height := revertHeight
	for {
		proposal, exist := p.proposalMap[height]
		if !exist {
			break
		}
		info := proposal.ProposedBlockInfo
		if info == nil {
			p.logger.Warn("Empty block info", "height", height)
		} else {
			// return the txs in precommitted blocks
			info.txsToReturned()
			best := p.csReactor.chain.BestBlock()
			state, err := p.csReactor.stateCreator.NewState(best.Header().StateRoot())
			if err != nil {
				p.logger.Error("revert the state faild ...", "err", err)
			}
			state.RevertTo(info.CheckPoint)
		}
		p.logger.Warn("Deleted from proposalMap:", "blockHeight", height, "block", proposal.ToString())
		delete(p.proposalMap, height)
		height++
	}

	if pivotExist {
		if p.blockLeaf.Height >= pivot.Height {
			p.blockLeaf = pivot.Parent
		}
		if p.QCHigh != nil && p.QCHigh.QCNode != nil && p.QCHigh.QCNode.Height >= pivot.Height {
			p.QCHigh = pivot.Justify
		}
	}
	// First senario : pivot height < b-leaf height
	//           pivot b-leaf                           b-leaf
	//             v     v                                v
	// A --- B --- C --- D     == revert result =>  A --- B
	//  \   / \   / \   /                            \   / \
	//   qcA   qcB   qcC                              qcA  qcB
	//                ^                                     ^
	//              QCHigh                                QCHigh

	// Second senario : pivot height >= b-leaf height, and new QC is not ready
	//                 pivot
	//                 b-leaf                                 b-leaf
	//                   v                                      v
	// A --- B --- C --- D     == revert result =>  A --- B --- C
	//  \   / \   / \   /                            \   / \   / \
	//   qcA   qcB   qcC                              qcA   qcB   qcC
	//                ^                                            ^
	//              QCHigh                                        QCHigh

	// Third senario : pivot height >= b-leaf height, and new QC already established
	//                 pivot
	//                 b-leaf                                 b-leaf
	//                   v                                      v
	// A --- B --- C --- D     == revert result =>  A --- B --- C
	//  \   / \   / \   / \       QCHigh reset       \   / \   /  \
	//   qcA   qcB   qcC  qcD                         qcA   qcB  qcC
	//                     ^                                      ^
	//                   QCHigh                                 QCHigh
	/*
		for h > p.blockLocked.Height {
			p.logger.Info("Revert loop", "block-leaf", p.blockLeaf.ToString(), "parent", p.blockLeaf.Parent.ToString())
			blockHeight := p.blockLeaf.Height
			if h < p.blockLeaf.Height {
				p.blockLeaf
			}
			p.blockLeaf = p.blockLeaf.Parent
			p.logger.Warn("Deleted from proposalMap:", "blockHeight", blockHeight, "block", p.proposalMap[blockHeight].ToString())
			delete(p.proposalMap, blockHeight)
			// FIXME: remove precommited block and release tx
		}
	*/
	p.logger.Info("Reverted !!!", "current block-leaf", p.blockLeaf.ToString(), "current QCHigh", p.QCHigh.ToString())
}

func (p *Pacemaker) OnReceiveQueryProposal(queryMsg *PMQueryProposalMessage) error {
	fromHeight := queryMsg.FromHeight
	toHeight := queryMsg.ToHeight
	queryRound := queryMsg.Round
	returnAddr := queryMsg.ReturnAddr
	p.logger.Info("receives query", "fromHeight", fromHeight, "toHeight", toHeight, "round", queryRound, "returnAddr", returnAddr)

	bestHeight := uint64(p.csReactor.chain.BestBlock().Header().Number())
	lastKBlockHeight := uint64(p.csReactor.chain.BestBlock().Header().LastKBlockHeight() + 1)
	if toHeight <= bestHeight {
		p.logger.Error("query too old", "fromHeight", fromHeight, "toHeight", toHeight, "round", queryRound)
		return errors.New("query too old")
	}
	if fromHeight < lastKBlockHeight {
		fromHeight = lastKBlockHeight
	}
	if fromHeight >= toHeight {
		p.logger.Error("invalid query", "fromHeight", fromHeight, "toHeight", toHeight)
	}

	queryHeight := fromHeight + 1
	name := p.csReactor.GetCommitteeMemberNameByIP(returnAddr.IP)
	peers := []*ConsensusPeer{newConsensusPeer(name, returnAddr.IP, returnAddr.Port, p.csReactor.magic)}
	for queryHeight <= toHeight {
		result := p.proposalMap[queryHeight]
		if result == nil {
			// Oooop!, I do not have it
			p.logger.Error("I dont have the specific proposal", "height", queryHeight, "round", queryRound)
			return errors.New(fmt.Sprintf("I dont have the specific proposal on height %v", queryHeight))
		}

		if result.ProposalMessage == nil {
			p.logger.Error("could not find raw proposal message", "height", queryHeight, "round", queryRound)
			return errors.New("could not find raw proposal message")
		}

		//send
		p.SendMessageToPeers(result.ProposalMessage, peers)

		queryHeight++
	}
	return nil
}
