package staking

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"math/big"

	"github.com/dfinlab/meter/meter"
	"github.com/ethereum/go-ethereum/rlp"
)

const (
	OP_BOUND     = uint32(1)
	OP_UNBOUND   = uint32(2)
	OP_CANDIDATE = uint32(3)
	OP_QUERY     = uint32(4)
)

const (
	OPTION_CANDIDATES   = uint32(1)
	OPTION_STAKEHOLDERS = uint32(2)
	OPTION_BUCKETS      = uint32(3)
)

// Candidate indicates the structure of a candidate
type StakingBody struct {
	Opcode     uint32
	Version    uint32
	Option     uint32
	HolderAddr meter.Address
	CandAddr   meter.Address
	CandName   []byte
	CandPubKey []byte //ecdsa.PublicKey
	CandIP     []byte
	CandPort   uint16
	StakingID  uuid.UUID // only for unbound, uuid is [16]byte
	Amount     big.Int
	Token      byte // meter or meter gov
}

func StakingEncodeBytes(sb *StakingBody) []byte {
	stakingBytes, _ := rlp.EncodeToBytes(sb)
	return stakingBytes
}

func StakingDecodeFromBytes(bytes []byte) (*StakingBody, error) {
	sb := StakingBody{}
	err := rlp.DecodeBytes(bytes, &sb)
	return &sb, err
}

func (sb *StakingBody) ToString() string {
	return fmt.Sprintf("StakingBody: Opcode=%v, Version=%v, Option=%v, HolderAddr=%v, CandAddr=%v, CandName=%v, CandPubKey=%v, CandIP=%v, CandPort=%v, StakingID=%v, Amount=%v, Token=%v",
		sb.Opcode, sb.Version, sb.Option, sb.HolderAddr, sb.CandAddr, sb.CandName, sb.CandPubKey, sb.CandIP, sb.CandPort, sb.StakingID, sb.Amount, sb.Token)
}

func (sb *StakingBody) BoundHandler(senv *StakingEnviroment, gas uint64) (ret []byte, leftOverGas uint64, err error) {
	staking := senv.GetStaking()
	state := senv.GetState()

	bucket := NewBucket(sb.HolderAddr, sb.CandAddr, &sb.Amount, uint8(sb.Token), uint64(0))
	bucket.Add()
	if stakeholder, ok := StakeholderMap[sb.HolderAddr]; ok {
		stakeholder.AddBucket(bucket)
	} else {
		stakeholder = NewStakeholder(sb.HolderAddr)
		stakeholder.AddBucket(bucket)
		stakeholder.Add()
	}

	switch sb.Token {
	case TOKEN_METER:
		err = staking.BoundAccountMeter(sb.HolderAddr, &sb.Amount, state)
	case TOKEN_METER_GOV:
		err = staking.BoundAccountMeterGov(sb.HolderAddr, &sb.Amount, state)
	default:
		err = errors.New("Invalid token parameter")
	}

	staking.SyncCandidateList(state)
	staking.SyncStakerholderList(state)
	staking.SyncBucketList(state)

	if gas < meter.ClauseGas {
		leftOverGas = 0
	} else {
		leftOverGas = gas - meter.ClauseGas
	}
	return
}

func (sb *StakingBody) UnBoundHandler(senv *StakingEnviroment, gas uint64) (ret []byte, leftOverGas uint64, err error) {
	staking := senv.GetStaking()
	state := senv.GetState()

	if gas < meter.ClauseGas {
		leftOverGas = 0
	} else {
		leftOverGas = gas - meter.ClauseGas
	}

	b := BucketMap[sb.StakingID]
	if b == nil {
		return nil, leftOverGas, errors.New("staking not found")
	}
	if (b.Owner != sb.HolderAddr) || (b.Value.Cmp(&sb.Amount) != 0) || (b.Token != sb.Token) {
		return nil, leftOverGas, errors.New("staking info mismatch")
	}

	// update stake holder
	if holder, ok := StakeholderMap[sb.HolderAddr]; ok {
		buckets := RemoveUuIDFromSlice(holder.Buckets, sb.StakingID)
		if len(buckets) == 0 {
			delete(StakeholderMap, sb.HolderAddr)
		} else {
			StakeholderMap[sb.HolderAddr] = &Stakeholder{
				Holder:     sb.HolderAddr,
				TotalStake: b.Value.Sub(holder.TotalStake, b.Value),
				Buckets:    buckets,
			}
		}
	}

	// update candidate list
	if cand, ok := CandidateMap[b.Candidate]; ok {
		buckets := RemoveUuIDFromSlice(cand.Buckets, b.BucketID)
		if len(buckets) == 0 {
			delete(CandidateMap, b.Candidate)
		} else {
			CandidateMap[b.Candidate] = &Candidate{
				Addr:       cand.Addr,
				Name:       cand.Name,
				PubKey:     cand.PubKey,
				IPAddr:     cand.IPAddr,
				Port:       cand.Port,
				TotalVotes: b.TotalVotes.Sub(cand.TotalVotes, b.TotalVotes),
				Buckets:    buckets,
			}
		}
	}
	delete(BucketMap, sb.StakingID)

	switch sb.Token {
	case TOKEN_METER:
		err = staking.UnboundAccountMeter(sb.HolderAddr, &sb.Amount, state)
	case TOKEN_METER_GOV:
		err = staking.UnboundAccountMeterGov(sb.HolderAddr, &sb.Amount, state)
	default:
		err = errors.New("Invalid token parameter")
	}

	staking.SyncCandidateList(state)
	staking.SyncStakerholderList(state)
	staking.SyncBucketList(state)
	return
}

func (sb *StakingBody) CandidateHandler(senv *StakingEnviroment, gas uint64) (ret []byte, leftOverGas uint64, err error) {
	staking := senv.GetStaking()
	state := senv.GetState()

	candidate := NewCandidate(sb.CandAddr, sb.CandPubKey, sb.CandIP, sb.CandPort)
	candidate.Add()

	if gas < meter.ClauseGas {
		leftOverGas = 0
	} else {
		leftOverGas = gas - meter.ClauseGas
	}

	staking.SyncCandidateList(state)
	staking.SyncStakerholderList(state)
	return
}

func (sb *StakingBody) QueryHandler(senv *StakingEnviroment, gas uint64) (ret []byte, leftOverGas uint64, err error) {
	switch sb.Option {
	case OPTION_CANDIDATES:
		// TODO:
		cs := make([]*Candidate, 0)
		for _, c := range CandidateMap {
			cs = append(cs, c)
		}
		ret, err = json.Marshal(cs)
	case OPTION_STAKEHOLDERS:
		ss := make([]*Stakeholder, 0)
		for _, s := range StakeholderMap {
			ss = append(ss, s)
		}
		ret, err = json.Marshal(ss)

	case OPTION_BUCKETS:
		// TODO:
		bs := make([]*Bucket, 0)
		for _, b := range BucketMap {
			bs = append(bs, b)
		}
		ret, err = json.Marshal(bs)

	default:
		return nil, gas, errors.New("Invalid option parameter")
	}

	if gas < meter.ClauseGas {
		leftOverGas = 0
	} else {
		leftOverGas = gas - meter.ClauseGas
	}
	return
}
