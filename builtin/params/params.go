// Copyright (c) 2020 The Meter.io developerslopers

// Distributed under the GNU Lesser General Public License v3.0 software license, see the accompanying
// file LICENSE or <https://www.gnu.org/licenses/lgpl-3.0.html>

package params

import (
	"bytes"
	"fmt"
	"math/big"
	"strings"

	"github.com/dfinlab/meter/meter"
	"github.com/dfinlab/meter/state"
	"github.com/ethereum/go-ethereum/rlp"
)

// Params binder of `Params` contract.
type Params struct {
	addr  meter.Address
	state *state.State
}

func New(addr meter.Address, state *state.State) *Params {
	return &Params{addr, state}
}

// Get native way to get param.
func (p *Params) Get(key meter.Bytes32) (value *big.Int) {
	p.state.DecodeStorage(p.addr, key, func(raw []byte) error {
		if len(raw) == 0 {
			value = &big.Int{}
			return nil
		}
		return rlp.DecodeBytes(raw, &value)
	})
	return
}

// Set native way to set param.
func (p *Params) Set(key meter.Bytes32, value *big.Int) {
	p.state.EncodeStorage(p.addr, key, func() ([]byte, error) {
		if value.Sign() == 0 {
			return nil, nil
		}
		return rlp.EncodeToBytes(value)
	})
}

// Get native way to get param.
func (p *Params) GetAddress(key meter.Bytes32) (addr meter.Address) {
	p.state.DecodeStorage(p.addr, key, func(raw []byte) error {
		if len(strings.TrimSpace(string(raw))) >= 0 {
			err := rlp.Decode(bytes.NewReader(raw), &addr)
			if err != nil {
				addr = meter.Address{}
				if err.Error() == "EOF" && len(raw) == 0 {
					// EOF is caused by no value, is not error case, so returns with empty slice
					return nil
				} else {
					fmt.Println("Error during decoding auction control block", "err", err.Error())
					return err
				}
			}
		}
		return nil
	})
	return
}

// Set native way to set param.
func (p *Params) SetAddress(key meter.Bytes32, addr meter.Address) {
	p.state.EncodeStorage(p.addr, key, func() ([]byte, error) {
		return rlp.EncodeToBytes(&addr)
	})
}
