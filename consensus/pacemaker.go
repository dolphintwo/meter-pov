package consensus

import (
	//bls "github.com/dfinlab/meter/crypto/multi_sig"
	//cmn "github.com/dfinlab/meter/libs/common"
	"time"
)

const (
	TIME_ROUND_INTVL_DEF = int(15)

	// TODO: define message struct separately to include common head
	//pacemake message type
	PACEMAKER_MSG_PROPOSAL = byte(1)
	PACEMAKER_MSG_VOTE     = byte(2)
	PACEMAKER_MSG_NEWVIEW  = byte(3)

	//round state machine
	PACEMAKER_ROUND_STATE_INIT          = byte(1)
	PACEMAKER_ROUND_STATE_PROPOSE_RCVD  = byte(2) // validator omly
	PACEMAKER_ROUND_STATE_PROPOSE_SNT   = byte(3) // proposer only
	PACEMAKER_ROUND_STATE_MAJOR_REACHED = byte(4) // proposer only
	PACEMAKER_ROUND_STATE_COMMITTED     = byte(5)
	PACEMAKER_ROUND_STATE_DECIDED       = byte(6)
)

var (
	genericQC = &QuorumCert{}

	qc0 = QuorumCert{
		QCHeight: 0,
		QCRound:  0,
		QCNode:   nil,
	}

	b0 = pmBlock{
		Height:  0,
		Round:   0,
		Parent:  nil,
		Justify: &qc0,
	}
)

type QuorumCert struct {
	//QCHieght/QCround must be the same with QCNode.Height/QCnode.Round
	QCHeight uint64
	QCRound  uint64
	QCNode   *pmBlock

	// FIXME: put evidence in QC
	//signature data , slice signature and public key must be match
	/*******
	proposalVoterBitArray *cmn.BitArray
	proposalVoterSig      []bls.Signature
	proposalVoterPubKey   []bls.PublicKey
	proposalVoterMsgHash  [][32]byte
	proposalVoterAggSig   bls.Signature
	proposalVoterNum      uint32
	**********/
}

type pmBlock struct {
	Height uint64
	Round  uint64

	Parent  *pmBlock
	Justify *QuorumCert

	// derived
	Decided    bool
	RoundState PMState

	// FIXME: put block info in pmBlock
	// local copy of proposed block
	/**********
	ProposedBlockInfo ProposedBlockInfo //data structure
	ProposedBlock     []byte            // byte slice block
	ProposedBlockType byte
	************/
}

type Pacemaker struct {
	csReactor *ConsensusReactor //global reactor info

	// Determines the time interval for a round interval
	timeRoundInterval int
	// Highest round that a block was committed
	highestCommittedRound int
	// Highest round known certified by QC.
	highestQCRound int
	// Current round (current_round - highest_qc_round determines the timeout).
	// Current round is basically max(highest_qc_round, highest_received_tc, highest_local_tc) + 1
	// update_current_round take care of updating current_round and sending new round event if
	// it changes
	currentRound int
	proposalMap  map[uint64]*pmBlock
	sigCounter   map[uint64]int

	lastVotingHeight uint64
	QCHigh           *QuorumCert

	blockLeaf     *pmBlock
	blockExecuted *pmBlock
	blockLocked   *pmBlock

	block           *pmBlock
	blockPrime      *pmBlock
	blockPrimePrime *pmBlock

	roundTimeOutCounter uint32
	//roundTimerStop      chan bool
}

func NewPaceMaker(conR *ConsensusReactor) *Pacemaker {
	p := &Pacemaker{
		csReactor:         conR,
		timeRoundInterval: TIME_ROUND_INTVL_DEF,
	}

	p.proposalMap = make(map[uint64]*pmBlock, 1000) // TODO:better way?
	p.sigCounter = make(map[uint64]int, 1000)
	//TBD: blockLocked/Executed/Leaf to genesis(b0). QCHigh to qc of genesis
	return p
}

func (p *Pacemaker) CreateLeaf(parent *pmBlock, qc *QuorumCert, height uint64, round uint64) *pmBlock {
	b := &pmBlock{
		Height:  height,
		Round:   round,
		Parent:  parent,
		Justify: qc,
	}

	return b
}

// b_exec  b_lock   b <- b' <- b"  b*
func (p *Pacemaker) Update(bnew *pmBlock) error {

	//now pipeline full, roll this pipeline first
	p.blockPrimePrime = bnew.Justify.QCNode
	p.blockPrime = p.blockPrimePrime.Justify.QCNode
	p.block = p.blockPrime.Justify.QCNode

	// pre-commit phase on b"
	p.UpdateQCHigh(bnew.Justify)

	if p.blockPrime.Height > p.blockLocked.Height {
		p.blockLocked = p.blockPrime // commit phase on b'
	}

	/* commit requires direct parent */
	if (p.blockPrimePrime.Parent != p.blockPrime) ||
		(p.blockPrime.Parent != p.block) {
		return nil
	}

	commitReady := []*pmBlock{}
	for b := p.block; b.Height > p.blockExecuted.Height; b = b.Parent {
		commitReady = append(commitReady, b)
	}
	p.OnCommit(commitReady)

	p.blockExecuted = p.block // decide phase on b
	return nil
}

// TBD: how to emboy b.cmd
func (p *Pacemaker) Execute(b *pmBlock) error {
	p.csReactor.logger.Info("Exec cmd:", "height", b.Height, "round", b.Round)

	return nil
}

func (p *Pacemaker) OnCommit(commitReady []*pmBlock) error {
	for _, b := range commitReady {
		p.csReactor.logger.Info("Committed Block", "Height = ", b.Height, "round", b.Round)
		p.Execute(b) //b.cmd
		//FIXME: write block to db
	}
	return nil
}

func (p *Pacemaker) OnReceiveProposal(bnew *pmBlock) error {
	if (bnew.Height > p.lastVotingHeight) &&
		(p.IsExtendedFromBLocked(bnew) || bnew.Justify.QCHeight > p.blockLocked.Height) {
		p.lastVotingHeight = bnew.Height

		if int(bnew.Round) > p.currentRound {
			p.currentRound = int(bnew.Round)
		}

		// stop previous round timer
		//close(p.roundTimerStop)

		// send vote message to leader
		p.sendMsg(bnew.Round, PACEMAKER_MSG_VOTE, genericQC, bnew)

		/***********
		// start the round timer
		p.roundTimerStop = make(chan bool)
		go func() {
			count := 0
			for {
				select {
				case <-time.After(time.Second * 5 * time.Duration(count)):
					p.currentRound++
					count++
					p.sendMsg(uint64(p.currentRound), PACEMAKER_MSG_NEWVIEW, p.QCHigh, nil)
				case <-p.roundTimerStop:
					return
				}
			}
		}()
		***********/
	}

	p.Update(bnew)
	return nil
}

func (p *Pacemaker) OnReceiveVote(b *pmBlock) error {
	//TODO: signature handling
	p.sigCounter[b.Round]++
	//if MajorityTwoThird(p.sigCounter[b.Round], p.csReactor.committeeSize) == false {
	if p.sigCounter[b.Round] < p.csReactor.committeeSize {
		// not reach 2/3
		p.csReactor.logger.Info("not reach majority", "count", p.sigCounter[b.Round], "committeeSize", p.csReactor.committeeSize)
		return nil
	} else {
		p.csReactor.logger.Info("reach majority", "count", p.sigCounter[b.Round], "committeeSize", p.csReactor.committeeSize)
	}

	//reach 2/3 majority, trigger the pipeline cmd
	qc := &QuorumCert{
		QCHeight: b.Height,
		QCRound:  b.Round,
		QCNode:   b,
	}
	p.OnRecieveNewView(qc)

	return nil
}

func (p *Pacemaker) OnPropose(b *pmBlock, qc *QuorumCert, height uint64, round uint64) *pmBlock {
	bnew := p.CreateLeaf(b, qc, height+1, round)

	// TODO: create slot in proposalMap directly, instead of sendmsg to self.
	//send proposal to all include myself
	p.broadcastMsg(round, PACEMAKER_MSG_PROPOSAL, genericQC, bnew)

	return bnew
}

// **************
/****
func (p *Pacemaker) GetProposer(height int64, round int) {
	return
}
****/

func (p *Pacemaker) UpdateQCHigh(qc *QuorumCert) bool {
	updated := false
	if qc.QCHeight > p.QCHigh.QCHeight {
		p.QCHigh = qc
		p.blockLeaf = p.QCHigh.QCNode
		updated = true
	}

	return updated
}

func (p *Pacemaker) OnBeat(height uint64, round uint64) {

	if p.csReactor.amIRoundProproser(round) {
		p.csReactor.logger.Info("OnBeat: I am round proposer", "round=", round)
		bleaf := p.OnPropose(p.blockLeaf, p.QCHigh, height, round)
		if bleaf == nil {
			panic("Propose failed")
		}
		p.blockLeaf = bleaf
	} else {
		p.csReactor.logger.Info("OnBeat: I am NOT round proposer", "round=", round)
	}

}

func (p *Pacemaker) OnNextSyncView(nextRound uint64) error {
	// send new round msg to next round proposer
	p.sendMsg(nextRound, PACEMAKER_MSG_NEWVIEW, p.QCHigh, nil)
	return nil
}

func (p *Pacemaker) OnRecieveNewView(qc *QuorumCert) error {
	changed := p.UpdateQCHigh(qc)

	if changed == true {
		if qc.QCHeight > p.blockLocked.Height {
			if p.csReactor.amIRoundProproser(qc.QCRound+1) == true {
				time.AfterFunc(1*time.Second, func() {
					p.csReactor.schedulerQueue <- func() { p.OnBeat(qc.QCHeight, qc.QCRound+1) }
				})
			} else {
				time.AfterFunc(1*time.Second, func() {
					p.OnNextSyncView(qc.QCRound + 1)
				})
			}
		}
	}
	return nil
}

//=========== Routines ==================================

//Committee Leader triggers
func (p *Pacemaker) Start(height uint64, round uint64) {

	//initiation to height/round
	b0.Height = height
	b0.Round = round
	b0.Justify.QCHeight = height
	b0.Justify.QCRound = round
	b0.Justify.QCNode = &b0

	b0.Height = height
	b0.Round = round
	b0.Justify.QCHeight = height
	b0.Justify.QCRound = round
	b0.Justify.QCNode = &b0

	b0.Height = height
	b0.Round = round
	b0.Justify.QCHeight = height
	b0.Justify.QCRound = round
	b0.Justify.QCNode = &b0

	// now assign b_lock b_exec, b_leaf qc_high
	p.block = &b0
	p.blockLocked = &b0
	p.blockExecuted = &b0
	p.blockLeaf = &b0
	p.proposalMap[height] = &b0
	p.QCHigh = &qc0

	p.blockPrime = nil
	p.blockPrimePrime = nil

	p.OnBeat(height, round)
}

//actions of commites/receives kblock, stop pacemake to next committee
// all proposal txs need to be reclaimed before stop
func (p *Pacemaker) Stop() {}
