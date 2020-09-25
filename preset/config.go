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
		CommitteeMinSize: 11,
		CommitteeMaxSize: 50,
		DelegateMaxSize:  100,
		DiscoServer:      "enode://edef07cf4108b1b8be09b34290cda09e490a23b1380cbb20cefc250ebaf2470dd470449eb080c18f48c7f1618d7b17f815729dd89ac51193a2f305cf2dee6182@13.228.91.172:55555",
		DiscoTopic:       "shoal",
	}
)
