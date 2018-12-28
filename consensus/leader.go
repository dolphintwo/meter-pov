/*****
 Leader Functionalities:
	if kblock is approved by old committee,
        i)  create group announce
        ii) consensus this group announcement
        iii)first proposer of block (in this proposer.go)
***/

package consensus

import (
	//    "errors"
	"fmt"
	"time"

	//"unsafe"

	//node "github.com/dfinlab/go-zdollar/node"
	"math/rand"

	crypto "github.com/ethereum/go-ethereum/crypto"
	bls "github.com/vechain/thor/crypto/multi_sig"
	cmn "github.com/vechain/thor/libs/common"
)

const (
	// FSM of Committee Leader
	COMMITTEE_LEADER_INIT       = byte(0x01)
	COMMITTEE_LEADER_ANNOUNCED  = byte(0x02)
	COMMITTEE_LEADER_NOTARYSENT = byte(0x03)
	COMMITTEE_LEADER_COMMITED   = byte(0x04)

	THRESHOLD_TIMER_TIMEOUT = 1 * time.Second //wait for reach 2/3 consensus timeout
	// 1s by default
)

type ConsensusLeader struct {
	node_id      uint32
	consensus_id uint32 // unique identifier for this consensus session

	CommitteeID uint32
	Nonce       uint64
	state       byte
	csReactor   *ConsensusReactor //global reactor info

	//signature data
	announceVoterBitArray *cmn.BitArray
	announceVoterIndexs   []int
	announceVoterSig      []bls.Signature
	announceVoterPubKey   []bls.PublicKey
	announceVoterMsgHash  [][32]byte
	announceVoterAggSig   bls.Signature
	announceVoterNum      int

	//
	notaryVoterBitArray *cmn.BitArray
	notaryVoterIndexes  []int
	notaryVoterSig      []bls.Signature
	notaryVoterPubKey   []bls.PublicKey
	notaryVoterMsgHash  [][32]byte
	notaryVoterAggSig   bls.Signature
	notaryVoterNum      int

	announceThresholdTimer *time.Timer // 2/3 voting timer
	notaryThresholdTimer   *time.Timer // notary 2/3 vote timer

	csPeers []*ConsensusPeer // consensus message peers
}

// send consensus message to all connected peers
func (cl *ConsensusLeader) SendMsg(msg *ConsensusMessage) bool {

	if len(cl.csPeers) == 0 {
		cl.csReactor.sendConsensusMsg(msg, nil)
		return true
	}

	for _, p := range cl.csPeers {
		//p.sendConsensusMsg(msg)
		if cl.csReactor.sendConsensusMsg(msg, p) {
			fmt.Println("send consnmessage to %v succesfully", p)
		} else {
			fmt.Println("send consnmessage to %v failed", p)
		}
	}
	return true
}

// Move to the init State
func (cl *ConsensusLeader) MoveInitState(curState byte) bool {
	// should not send move to next round message for leader state machine
	fmt.Println("current state %v, move to state init", curState)
	cl.state = COMMITTEE_LEADER_INIT
	return true
}

//New CommitteeLeader
func NewCommitteeLeader(conR *ConsensusReactor) *ConsensusLeader {
	var cl ConsensusLeader

	// initialize the ConsenusLeader
	//cl.consensus_id = conR.consensus_id
	cl.Nonce = conR.curNonce
	cl.state = COMMITTEE_LEADER_INIT
	cl.csReactor = conR

	// create committee ID
	r := rand.New(rand.NewSource(99))
	cl.CommitteeID = r.Uint32()
	conR.curCommitteeID = cl.CommitteeID

	cl.announceVoterBitArray = cmn.NewBitArray(conR.committeeSize)
	cl.notaryVoterBitArray = cmn.NewBitArray(conR.committeeSize)

	// form topology, we know the 0 is Leader itself
	fmt.Println(conR.curCommittee)
	for _, v := range conR.curCommittee.Validators[1:] {
		// initialize PeerConn
		p := newConsensusPeer(v.NetAddr.IP, v.NetAddr.Port)
		cl.csPeers = append(cl.csPeers, p)
	}
	return &cl
}

// Committee leader create AnnounceCommittee to all peers
func (cl *ConsensusLeader) GenerateAnnounceMsg() bool {

	curHeight := cl.csReactor.curHeight
	curRound := cl.csReactor.curRound

	// curRound must be zero while sending announce
	if curRound != 0 {
		fmt.Println("curRound is %d, expected 0", curRound)
		curRound = 0
	}

	fmt.Println("curHeight = ", curHeight, ", curRound = ", curRound)
	cmnHdr := ConsensusMsgCommonHeader{
		Height:    curHeight,
		Round:     curRound,
		Sender:    crypto.FromECDSAPub(&cl.csReactor.myPubKey),
		Timestamp: time.Now(),
		MsgType:   CONSENSUS_MSG_ANNOUNCE_COMMITTEE,
	}

	paramBytes, _ := cl.csReactor.csCommon.params.ToBytes()

	msg := &AnnounceCommitteeMessage{
		CSMsgCommonHeader: cmnHdr,

		AnnouncerID:   crypto.FromECDSAPub(&cl.csReactor.myPubKey),
		CommitteeID:   cl.CommitteeID,
		CommitteeSize: cl.csReactor.committeeSize,
		Nonce:         cl.Nonce,

		CSParams:       paramBytes,
		CSSystem:       cl.csReactor.csCommon.system.ToBytes(),
		CSLeaderPubKey: cl.csReactor.csCommon.system.PubKeyToBytes(cl.csReactor.csCommon.PubKey),
		KBlockHeight:   0, //TBD, last Kblock Height
		POWBlockHeight: 0, //TBD

		SignOffset: MSG_SIGN_OFFSET_DEFAULT,
		SignLength: MSG_SIGN_LENGTH_DEFAULT,
	}

	fmt.Println("Announce Msg: ", msg.String())
	var m ConsensusMessage = msg
	cl.SendMsg(&m)
	cl.state = COMMITTEE_LEADER_ANNOUNCED

	//timeout function
	announceExpire := func() {
		fmt.Println("reach 2/3 votes of announce expired ...")

		//XXX: Yang: Hack here +2 to pass 2/3
		fmt.Println("total committer", (cl.announceVoterNum), "commtteeSize", cl.csReactor.committeeSize)
		if cl.announceVoterNum != 0 && (cl.announceVoterNum+1) >= (cl.csReactor.committeeSize*2/3) &&
			//fmt.Println("total committer", cl.announceVoterNum, "commtteeSize", cl.csReactor.committeeSize)
			//if cl.announceVoterNum >= (cl.csReactor.committeeSize*2/3) &&
			cl.state == COMMITTEE_LEADER_ANNOUNCED {

			fmt.Println("Committers reach 2/3 of Committee")

			//stop announce Timer
			//cl.announceThresholdTimer.Stop()

			// Aggregate signature here
			cl.announceVoterAggSig = cl.csReactor.csCommon.AggregateSign(cl.announceVoterSig)
			cl.csReactor.UpdateActualCommittee(cl.announceVoterIndexs, cl.announceVoterPubKey, cl.announceVoterBitArray)

			//send out announce notary
			cl.GenerateNotaryAnnounceMsg()
			cl.state = COMMITTEE_LEADER_NOTARYSENT

			//timeout function
			notaryExpire := func() {
				fmt.Println("reach 2/3 vote of notary expired ...")
				fmt.Println("total committer", cl.notaryVoterNum, "commtteeSize", cl.csReactor.committeeSize)
				cl.MoveInitState(cl.state)
			}
			cl.notaryThresholdTimer = time.AfterFunc(THRESHOLD_TIMER_TIMEOUT, notaryExpire)
		} else {
			fmt.Println("did not reach 2/3 committer of announce ...")
			cl.MoveInitState(cl.state)
		}
	}
	cl.announceThresholdTimer = time.AfterFunc(THRESHOLD_TIMER_TIMEOUT, announceExpire)

	return true
}

// After announce vote > 2/3, Leader generate Notary
// Committee leader create NotaryAnnounce to all members
func (cl *ConsensusLeader) GenerateNotaryAnnounceMsg() bool {

	curHeight := cl.csReactor.curHeight
	curRound := cl.csReactor.curRound

	// curRound must be zero while sending announce
	if curRound != 0 {
		fmt.Println("curRound is ", curRound, " expected 0")
		curRound = 0
	}

	cmnHdr := ConsensusMsgCommonHeader{
		Height:    curHeight,
		Round:     curRound,
		Sender:    crypto.FromECDSAPub(&cl.csReactor.myPubKey),
		Timestamp: time.Now(),
		MsgType:   CONSENSUS_MSG_NOTARY_ANNOUNCE,
	}

	msg := &NotaryAnnounceMessage{
		CSMsgCommonHeader: cmnHdr,

		AnnouncerID:   crypto.FromECDSAPub(&cl.csReactor.myPubKey),
		CommitteeID:   cl.CommitteeID,
		CommitteeSize: cl.csReactor.committeeSize,

		SignOffset:             MSG_SIGN_OFFSET_DEFAULT,
		SignLength:             MSG_SIGN_LENGTH_DEFAULT, //uint(unsafe.Sizeof(cmnHdr))
		VoterBitArray:          *cl.announceVoterBitArray,
		VoterAggSignature:      cl.csReactor.csCommon.system.SigToBytes(cl.announceVoterAggSig),
		CommitteeActualSize:    len(cl.csReactor.curActualCommittee),
		CommitteeActualMembers: cl.csReactor.BuildCommitteeInfoFromMember(cl.csReactor.curActualCommittee),
	}

	fmt.Println("NotaryAnnounce Msg: ", msg.String())
	var m ConsensusMessage = msg
	cl.SendMsg(&m)
	cl.state = COMMITTEE_LEADER_NOTARYSENT

	return true
}

// process commitCommittee in response of announce committee
func (cl *ConsensusLeader) ProcessCommitMsg(commitMsg *CommitCommitteeMessage, src *ConsensusPeer) bool {

	// only process Vote in state announced
	if cl.state < COMMITTEE_LEADER_ANNOUNCED {
		fmt.Println("state machine incorrect, expected ANNOUNCED, actual %v", cl.state)
		return false
	}

	// valid the common header first
	/***
	commitMsg, ok := interface{}(commit).(CommitCommitteeMessage)
	if ok != false {
		fmt.Println("Message type is not CommitCommitteeMessage")
		return false
	}
	***/

	ch := commitMsg.CSMsgCommonHeader
	if ch.Height != cl.csReactor.curHeight {
		fmt.Println("Height mismatch!, curHeight ", cl.csReactor.curHeight, " the incoming ", ch.Height)
		return false
	}

	if ch.Round != cl.csReactor.curRound {
		fmt.Println("Round mismatch!, curRound ", cl.csReactor.curRound, " the incoming ", ch.Round)
		return false
	}

	if ch.MsgType != CONSENSUS_MSG_COMMIT_COMMITTEE {
		fmt.Println("MsgType is not CONSENSUS_MSG_COMMIT_COMMITTEE but ", ch.MsgType)
		fmt.Println(ch)
		return false
	}

	// valid the voter index. we can get the index from the publicKey
	senderPubKey, err := crypto.UnmarshalPubkey(ch.Sender)
	if err != nil {
		fmt.Println("ummarshal public key of sender failed ")
		return false
	}
	index := cl.csReactor.GetCommitteeMemberIndex(*senderPubKey)
	if index != commitMsg.CommitterIndex {
		fmt.Println("Voter index mismatch ", index, " vs ", commitMsg.CommitterIndex)
		return false
	}

	//so far so good
	// 1. validate vote signature
	myPubKey := cl.csReactor.myPubKey
	signMsg := cl.csReactor.BuildAnnounceSignMsg(myPubKey, uint32(commitMsg.CommitteeID), uint64(ch.Height), uint32(ch.Round))
	fmt.Println("sign message: ", signMsg)

	// validate the message hash
	msgHash := cl.csReactor.csCommon.Hash256Msg([]byte(signMsg), uint32(MSG_SIGN_OFFSET_DEFAULT), uint32(MSG_SIGN_LENGTH_DEFAULT))
	if msgHash != commitMsg.SignedMessageHash {
		fmt.Println("msgHash mismatch ...")
		return false
	}

	// validate the signature
	sig, err := cl.csReactor.csCommon.system.SigFromBytes(commitMsg.CommitterSignature)
	if err != nil {
		fmt.Println("get signature failed ...")
		return false
	}

	pubKey, err := cl.csReactor.csCommon.system.PubKeyFromBytes(commitMsg.CSCommitterPubKey)
	if err != nil {
		fmt.Println("get PubKey failed ...")
		return false
	}

	//valid := bls.Verify(commitMsg.CommitterSignature, msgHash, commitMsg.CSCommitterPubKey)
	valid := bls.Verify(sig, msgHash, pubKey)
	if valid == false {
		fmt.Println("validate voter signature failed")
		return false
	}

	// 2. add src to bitArray.
	cl.announceVoterNum++
	cl.announceVoterBitArray.SetIndex(index, true)

	// Basic we get the actual committee here, but publish in notary
	cl.announceVoterIndexs = append(cl.announceVoterIndexs, commitMsg.CommitterIndex)
	//cl.announceVoterSig = append(cl.announceVoterSig, commitMsg.CommitterSignature)
	cl.announceVoterSig = append(cl.announceVoterSig, sig)
	//cl.announceVoterPubKey = append(cl.announceVoterPubKey, commitMsg.CSCommitterPubKey)
	cl.announceVoterPubKey = append(cl.announceVoterPubKey, pubKey)
	cl.announceVoterMsgHash = append(cl.announceVoterMsgHash, commitMsg.SignedMessageHash)

	/**** Announce/Commit is special because we want receive the commit as many as possible. Move the 2/3 action to timer expire func
		// 3. if the totoal vote > 2/3, move to NotarySend state
		if cl.announceVoterNum >= (cl.csReactor.committeeSize*2/3) &&
			cl.state == COMMITTEE_LEADER_ANNOUNCED {
			//stop announce Timer
			cl.announceThresholdTimer.Stop()

			//send out notary
			cl.state = COMMITTEE_LEADER_NOTARYSENT

			//timeout function
			notaryExpire := func() {
				fmt.Println("reach 2/3 vote of notary expired ...")
				cl.MoveInitState(cl.state)
			}
			cl.notaryThresholdTimer = time.AfterFunc(THRESHOLD_TIMER_TIMEOUT, notaryExpire)
		}
	****/
	return true
}

// VoteForNotaryMessage MsgSubType is for announce is checked in validator
func (cl *ConsensusLeader) ProcessVoteNotaryAnnounce(vote4NotaryMsg *VoteForNotaryMessage, src *ConsensusPeer) bool {

	// only process Vote Notary in state NotarySent
	if cl.state != COMMITTEE_LEADER_NOTARYSENT {
		fmt.Println("state machine incorrect, expected COMMITTEE_LEADER_NOTARYSENT, actual: ", cl.state)
		return false
	}

	// valid the common header first
	/****
	vote4NotaryMsg, ok := interface{}(vote).(VoteForNotaryMessage)
	if ok != false {
		fmt.Println("Message type is not VoteForNotaryMessage")
		return false
	}
	****/

	ch := vote4NotaryMsg.CSMsgCommonHeader
	if ch.Height != cl.csReactor.curHeight {
		fmt.Println("Height mismatch!, curHeight %d, the incoming %d", cl.csReactor.curHeight, ch.Height)
		return false
	}

	if ch.Round != cl.csReactor.curRound {
		fmt.Println("Round mismatch!, curRound %d, the incoming %d", cl.csReactor.curRound, ch.Round)
		return false
	}

	if ch.MsgType != CONSENSUS_MSG_VOTE_FOR_NOTARY {
		fmt.Println("MsgType is not CONSENSUS_MSG_VOTE_FOR_NOTARY")
		return false
	}

	// valid the voter index. we can get the index from the publicKey
	senderPubKey, err := crypto.UnmarshalPubkey(ch.Sender)
	if err != nil {
		fmt.Println("ummarshal public key of sender failed ")
		return false
	}
	index := cl.csReactor.GetCommitteeMemberIndex(*senderPubKey)
	if index != vote4NotaryMsg.VoterIndex {
		fmt.Println("Voter index mismatch %d vs %d", index, vote4NotaryMsg.VoterIndex)
		return false
	}

	//so far so good
	// 1. validate voter signature
	myPubKey := cl.csReactor.myPubKey
	signMsg := cl.csReactor.BuildNotaryAnnounceSignMsg(myPubKey, uint32(cl.CommitteeID), uint64(ch.Height), uint32(ch.Round))
	fmt.Println("sign message: ", signMsg)

	// validate the message hash
	msgHash := cl.csReactor.csCommon.Hash256Msg([]byte(signMsg), uint32(MSG_SIGN_OFFSET_DEFAULT), uint32(MSG_SIGN_LENGTH_DEFAULT))
	if msgHash != vote4NotaryMsg.SignedMessageHash {
		fmt.Println("msgHash mismatch ...")
		return false
	}

	sig, err := cl.csReactor.csCommon.system.SigFromBytes(vote4NotaryMsg.VoterSignature)
	if err != nil {
		fmt.Println("get signature failed ...")
		return false
	}

	pubKey, err := cl.csReactor.csCommon.system.PubKeyFromBytes(vote4NotaryMsg.CSVoterPubKey)
	if err != nil {
		fmt.Println("get PubKey failed ...")
		return false
	}

	valid := bls.Verify(sig, msgHash, pubKey)
	if valid == false {
		fmt.Println("validate voter signature failed")
		return false
	}

	// 2. add src to bitArray.
	cl.notaryVoterNum++
	cl.notaryVoterBitArray.SetIndex(index, true)

	cl.notaryVoterIndexes = append(cl.notaryVoterIndexes, vote4NotaryMsg.VoterIndex)
	cl.notaryVoterSig = append(cl.notaryVoterSig, sig)
	cl.notaryVoterPubKey = append(cl.notaryVoterPubKey, pubKey)
	cl.notaryVoterMsgHash = append(cl.notaryVoterMsgHash, msgHash)

	// XXX Yang: Hack here +2 to get 2/3
	// 3. if the totoal vote > 2/3, move to Commit state
	if (cl.notaryVoterNum+1) >= cl.csReactor.committeeSize*2/3 &&
		//if cl.notaryVoterNum >= cl.csReactor.committeeSize*2/3 &&
		cl.state == COMMITTEE_LEADER_NOTARYSENT {
		//save all group info as meta data
		cl.state = COMMITTEE_LEADER_COMMITED
		cl.notaryThresholdTimer.Stop()

		//aggregate signature
		// Aggregate signature here
		cl.notaryVoterAggSig = cl.csReactor.csCommon.AggregateSign(cl.notaryVoterSig)

		//Finally, go to init
		cl.MoveInitState(cl.state)

		//Committee is established. Myself is Leader, server as 1st proposer.
		fmt.Println("")
		fmt.Println("====================================================")
		fmt.Println("Committee is established!!! ... #", cl.CommitteeID)
		fmt.Println("Myself is Leader, Let's move to 1st proposer for Round 0.")
		fmt.Println("=====================================================")
		fmt.Println("")

		//Now move to propose the 1st block in round 0
		cl.csReactor.enterConsensusValidator()
		cl.csReactor.csValidator.state = COMMITTEE_VALIDATOR_COMMITSENT
		cl.csReactor.ScheduleProposer(0)

		return true

	} else {
		// not reach 3/2 yet, wait for more
		fmt.Println("Vote for NotaryAnnounce processed ...")
		return true
	}
}
