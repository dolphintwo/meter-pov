// Copyright (c) 2020 The Meter.io developerslopers

// Distributed under the GNU Lesser General Public License v3.0 software license, see the accompanying
// file LICENSE or <https://www.gnu.org/licenses/lgpl-3.0.html>

package accountlock

import (
	"bytes"

	"github.com/dfinlab/meter/builtin"
	"github.com/dfinlab/meter/meter"
	"github.com/dfinlab/meter/state"
)

func (a *AccountLock) GetCurrentEpoch() uint32 {
	bestBlock := a.chain.BestBlock()
	return uint32(bestBlock.GetBlockEpoch())
}

func (a *AccountLock) IsExceptionalAccount(addr meter.Address, state *state.State) bool {
	// executor is exceptional
	executor := meter.BytesToAddress(builtin.Params.Native(state).Get(meter.KeyExecutorAddress).Bytes())
	if bytes.Compare(addr.Bytes(), executor.Bytes()) == 0 {
		return true
	}

	return false
}
