// Copyright (c) 2018 The VeChainThor developers

// Distributed under the GNU Lesser General Public License v3.0 software license, see the accompanying
// file LICENSE or <https://www.gnu.org/licenses/lgpl-3.0.html>

package packer

import (
	"github.com/pkg/errors"
	"github.com/dfinlab/meter/block"
	"github.com/dfinlab/meter/builtin"
	"github.com/dfinlab/meter/chain"
	"github.com/dfinlab/meter/poa"
	"github.com/dfinlab/meter/runtime"
	"github.com/dfinlab/meter/state"
	"github.com/dfinlab/meter/meter"
	"github.com/dfinlab/meter/xenv"
)

var (
	GlobPackerInst *Packer
)

// Packer to pack txs and build new blocks.
type Packer struct {
	chain          *chain.Chain
	stateCreator   *state.Creator
	nodeMaster     meter.Address
	beneficiary    *meter.Address
	targetGasLimit uint64
}

func GetGlobPackerInst() *Packer {
	return GlobPackerInst
}

func SetGlobPackerInst(p *Packer) bool {
	GlobPackerInst = p
	return true
}

// New create a new Packer instance.
// The beneficiary is optional, it defaults to endorsor if not set.
func New(
	chain *chain.Chain,
	stateCreator *state.Creator,
	nodeMaster meter.Address,
	beneficiary *meter.Address) *Packer {

	p := &Packer{
		chain,
		stateCreator,
		nodeMaster,
		beneficiary,
		0,
	}

	SetGlobPackerInst(p)
	return p
}

// Schedule schedule a packing flow to pack new block upon given parent and clock time.
func (p *Packer) Schedule(parent *block.Header, nowTimestamp uint64) (flow *Flow, err error) {
	state, err := p.stateCreator.NewState(parent.StateRoot())
	if err != nil {
		return nil, errors.Wrap(err, "state")
	}

	var (
		endorsement = builtin.Params.Native(state).Get(meter.KeyProposerEndorsement)
		authority   = builtin.Authority.Native(state)
		candidates  = authority.Candidates(endorsement, meter.MaxBlockProposers)
		proposers   = make([]poa.Proposer, 0, len(candidates))
		beneficiary meter.Address
	)
	if p.beneficiary != nil {
		beneficiary = *p.beneficiary
	}

	for _, c := range candidates {
		if p.beneficiary == nil && c.NodeMaster == p.nodeMaster {
			// not beneficiary not set, set it to endorsor
			beneficiary = c.Endorsor
		}
		proposers = append(proposers, poa.Proposer{
			Address: c.NodeMaster,
			Active:  c.Active,
		})
	}

	// calc the time when it's turn to produce block
	sched, err := poa.NewScheduler(p.nodeMaster, proposers, parent.Number(), parent.Timestamp())
	if err != nil {
		return nil, err
	}

	newBlockTime := sched.Schedule(nowTimestamp)
	updates, score := sched.Updates(newBlockTime)

	for _, u := range updates {
		authority.Update(u.Address, u.Active)
	}

	rt := runtime.New(
		p.chain.NewSeeker(parent.ID()),
		state,
		&xenv.BlockContext{
			Beneficiary: beneficiary,
			Signer:      p.nodeMaster,
			Number:      parent.Number() + 1,
			Time:        newBlockTime,
			GasLimit:    p.GasLimit(parent.GasLimit()),
			TotalScore:  parent.TotalScore() + score,
		})

	return newFlow(p, parent, rt), nil
}

// Mock create a packing flow upon given parent, but with a designated timestamp.
// It will skip the PoA verification and scheduling, and the block produced by
// the returned flow is not in consensus.
func (p *Packer) Mock(parent *block.Header, targetTime uint64, gasLimit uint64) (*Flow, error) {
	state, err := p.stateCreator.NewState(parent.StateRoot())
	if err != nil {
		return nil, errors.Wrap(err, "state")
	}

	rt := runtime.New(
		p.chain.NewSeeker(parent.ID()),
		state,
		&xenv.BlockContext{
			Beneficiary: p.nodeMaster,
			Signer:      p.nodeMaster,
			Number:      parent.Number() + 1,
			Time:        targetTime,
			GasLimit:    gasLimit,
			TotalScore:  parent.TotalScore() + 1,
		})

	return newFlow(p, parent, rt), nil
}

func (p *Packer) GasLimit(parentGasLimit uint64) uint64 {
	if p.targetGasLimit != 0 {
		return block.GasLimit(p.targetGasLimit).Qualify(parentGasLimit)
	}
	return parentGasLimit
}

// SetTargetGasLimit set target gas limit, the Packer will adjust block gas limit close to
// it as it can.
func (p *Packer) SetTargetGasLimit(gl uint64) {
	p.targetGasLimit = gl
}
