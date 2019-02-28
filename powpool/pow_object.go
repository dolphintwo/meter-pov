// Copyright (c) 2018 The VeChainThor developers

// Distributed under the GNU Lesser General Public License v3.0 software license, see the accompanying
// file LICENSE or <https://www.gnu.org/licenses/lgpl-3.0.html>

package powpool

import (
	"time"

	"github.com/vechain/thor/thor"
)

type powObject struct {
	blockInfo PowBlockInfo
	timeAdded int64
}

func NewPowObject(powBlockInfo *PowBlockInfo) *powObject {
	po := &powObject{
		blockInfo: *powBlockInfo,
		timeAdded: time.Now().UnixNano(),
	}
	return po
}

// HashID returns the Hash of powBlkHdr only, as the key of powObject
func (p *powObject) HashID() thor.Bytes32 {
	return p.blockInfo.HeaderHash
}

func (p *powObject) Height() uint32 {
	return p.blockInfo.PowHeight
}

func (p *powObject) Beneficiary() thor.Address {
	return p.blockInfo.Beneficiary
}