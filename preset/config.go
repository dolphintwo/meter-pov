// Copyright (c) 2020 The Meter.io developerslopers

// Distributed under the GNU Lesser General Public License v3.0 software license, see the accompanying
// file LICENSE or <https://www.gnu.org/licenses/lgpl-3.0.html>

package preset

type PresetConfig struct {
	CommitteeMinSize int
	CommitteeMaxSize int
	DelegateMaxSize  int
	DiscoServer      string
	DiscoTopic       string
}

var (
	ShoalPresetConfig = &PresetConfig{
		CommitteeMinSize: 15,
		CommitteeMaxSize: 50,
		DelegateMaxSize:  100,
		DiscoServer:      "enode://3011a0740181881c7d4033a83a60f69b68f9aedb0faa784133da84394120ffe9a1686b2af212ffad16fbba88d0ff302f8edb05c99380bd904cbbb96ee4ca8cfb@54.184.14.94:55555",
		DiscoTopic:       "shoal",
	}
)
