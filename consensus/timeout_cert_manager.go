// Copyright (c) 2020 The Meter.io developerslopers

// Distributed under the GNU Lesser General Public License v3.0 software license, see the accompanying
// file LICENSE or <https://www.gnu.org/licenses/lgpl-3.0.html>

package consensus

import (
	"sync"

	bls "github.com/dfinlab/meter/crypto/multi_sig"
	cmn "github.com/dfinlab/meter/libs/common"
)

type timeoutID struct {
	Height uint32
	Round  uint32
}

type timeoutVal struct {
	Counter        uint64
	PeerID         []byte
	PeerIndex      uint32
	MsgHash        [32]byte
	SignatureBytes []byte
}

type PMTimeoutCertManager struct {
	sync.RWMutex
	pacemaker *Pacemaker

	cache     map[timeoutID][]*timeoutVal
	bitArrays map[timeoutID]*cmn.BitArray
}

func newPMTimeoutCertManager(pacemaker *Pacemaker) *PMTimeoutCertManager {
	return &PMTimeoutCertManager{
		pacemaker: pacemaker,
		cache:     make(map[timeoutID][]*timeoutVal),
		bitArrays: make(map[timeoutID]*cmn.BitArray),
	}
}

func (tm *PMTimeoutCertManager) collectSignature(newViewMsg *PMNewViewMessage) {
	tm.Lock()
	defer tm.Unlock()

	if newViewMsg.Reason == RoundTimeout {
		index := int(newViewMsg.PeerIndex)
		// append signature only if it doesn't exist
		height := newViewMsg.TimeoutHeight
		round := newViewMsg.TimeoutRound

		var bitArray *cmn.BitArray
		id := timeoutID{Height: height, Round: round}
		bitArray, ok := tm.bitArrays[id]
		if !ok {
			bitArray = cmn.NewBitArray(int(tm.pacemaker.csReactor.committeeSize))
			tm.bitArrays[id] = bitArray
		}

		if bitArray.GetIndex(index) == false {
			bitArray.SetIndex(index, true)
			sig, err := tm.pacemaker.csReactor.csCommon.GetSystem().SigFromBytes(newViewMsg.PeerSignature)
			if err != nil {
				tm.pacemaker.logger.Error("error convert signature", "err", err)
			}
			sig.Free()
			var vals []*timeoutVal
			vals, ok := tm.cache[id]
			if !ok {
				vals = make([]*timeoutVal, 0)
			}
			tm.cache[id] = append(vals, &timeoutVal{
				// TODO: set counter
				Counter:        newViewMsg.TimeoutCounter,
				PeerID:         newViewMsg.PeerID,
				PeerIndex:      newViewMsg.PeerIndex,
				MsgHash:        newViewMsg.SignedMessageHash,
				SignatureBytes: newViewMsg.PeerSignature,
			})
		}
	}
}

func (tm *PMTimeoutCertManager) count(height, round uint32) int {
	tm.RLock()
	defer tm.RUnlock()
	if bitArray, ok := tm.bitArrays[timeoutID{height, round}]; ok {
		return bitArray.Count()
	}
	return 0
}

func (tm *PMTimeoutCertManager) getTimeoutCert(height, round uint32) *PMTimeoutCert {
	id := timeoutID{height, round}
	vals, ok := tm.cache[id]
	if !ok {
		return nil
	}
	bitArray, ok := tm.bitArrays[id]
	if !ok {
		return nil
	}

	var sigs []bls.Signature
	for _, v := range vals {
		sig, err := tm.pacemaker.csReactor.csCommon.GetSystem().SigFromBytes(v.SignatureBytes)
		if err != nil {
			continue
		}
		sigs = append(sigs, sig)
	}
	aggSig := tm.pacemaker.csReactor.csCommon.AggregateSign(sigs)
	// cleanup
	for _, sig := range sigs {
		sig.Free()
	}
	return &PMTimeoutCert{
		TimeoutHeight: height,
		TimeoutRound:  round,
		//TODO: better way?
		TimeoutCounter:  uint32(vals[0].Counter),
		TimeoutBitArray: bitArray,
		TimeoutAggSig:   aggSig,
	}
}

func (tm *PMTimeoutCertManager) cleanup(height, round uint32) {
	tm.Lock()
	defer tm.Unlock()
	for k, _ := range tm.cache {
		if k.Height <= height || k.Round <= k.Round {
			delete(tm.cache, k)
			delete(tm.bitArrays, k)
		}
	}
}
