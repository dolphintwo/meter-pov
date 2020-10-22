package probe

import (
	"bytes"
	"net/http"
	"runtime"
	"strings"

	"github.com/dfinlab/meter/api/utils"
	"github.com/dfinlab/meter/chain"
	"github.com/dfinlab/meter/consensus"
	"github.com/dfinlab/meter/powpool"
	"github.com/dfinlab/meter/script/staking"
	"github.com/dfinlab/meter/txpool"
)

type Probe struct {
	Cons          *consensus.ConsensusReactor
	ComplexPubkey string
	Chain         *chain.Chain
	Version       string
	Network       Network
}

func (p *Probe) HandleProbe(w http.ResponseWriter, r *http.Request) {
	name := ""
	pubkeyMatch := false
	delegateList, _ := staking.GetInternalDelegateList()
	for _, d := range delegateList {
		registeredPK := string(d.PubKey)
		trimedPK := strings.TrimSpace(registeredPK)
		if strings.Compare(trimedPK, p.ComplexPubkey) == 0 {
			name = string(d.Name)
			pubkeyMatch = (bytes.Compare(d.PubKey, []byte(p.ComplexPubkey)) == 0)
			break
		}
	}
	txs := txpool.GetGlobTxPoolInst()
	pows := powpool.GetGlobPowPoolInst()
	bestBlock, _ := convertBlock(p.Chain.BestBlock())
	bestQC, _ := convertQC(p.Chain.BestQC())
	bestQCCandidate, _ := convertQC(p.Chain.BestQCCandidate())
	qcHigh, _ := convertQC(p.Cons.GetQCHigh())
	result := ProbeResult{
		Name:               name,
		PubKey:             p.ComplexPubkey,
		PubKeyValid:        pubkeyMatch,
		Version:            p.Version,
		BestBlock:          bestBlock,
		BestQC:             bestQC,
		BestQCCandidate:    bestQCCandidate,
		QCHigh:             qcHigh,
		IsCommitteeMember:  p.Cons.IsCommitteeMember(),
		IsPacemakerRunning: p.Cons.IsPacemakerRunning(),
		TxPool:             &TxPool{Executables: txs.ExecutablesCount(), Total: txs.TotalCount()},
		PowPool:            &PowPool{Total: pows.TotalCount()},
		ChainCache:         &ChainCache{RawBlocks: p.Chain.RawBlockCount(), Receipts: p.Chain.ReceiptCount(), Roots: p.Chain.RootCount(), Tries: p.Chain.TrieCount()},
		ProposalMap:        p.Cons.GetProposalMapCount(),
	}

	utils.WriteJSON(w, result)
}

func (p *Probe) HandleProbeMem(w http.ResponseWriter, r *http.Request) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	unit := uint64(1024 * 1024)
	result := ProbeMemResult{
		TotalAlloc:   m.TotalAlloc / unit,
		Sys:          m.Sys / unit,
		Frees:        m.Sys / unit,
		HeapAlloc:    m.HeapAlloc / unit,
		HeapSys:      m.HeapSys / unit,
		HeapIdle:     m.HeapIdle / unit,
		HeapReleased: m.HeapReleased / unit,
		HeapInuse:    m.HeapInuse / unit,
		HeapObjects:  m.HeapObjects,
		StackInuse:   m.StackInuse / unit,
		StackSys:     m.StackSys / unit,
		MSpanInuse:   m.MSpanInuse / unit,
		MSpanSys:     m.MSpanSys / unit,
		MCacheInuse:  m.MCacheInuse / unit,
		MCacheSys:    m.MCacheSys / unit,
	}
	utils.WriteJSON(w, result)
}

func (p *Probe) HandlePubkey(w http.ResponseWriter, r *http.Request) {
	utils.WriteJSON(w, p.ComplexPubkey)
}

func (p *Probe) HandleVersion(w http.ResponseWriter, r *http.Request) {
	utils.WriteJSON(w, p.Version)
}

func (p *Probe) HandlePeers(w http.ResponseWriter, r *http.Request) {
	utils.WriteJSON(w, p.Network.PeersStats())
}
