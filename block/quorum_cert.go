package block

import (
	"fmt"
	"io"

	cmn "github.com/dfinlab/meter/libs/common"
	"github.com/ethereum/go-ethereum/rlp"
)

type QuorumCert struct {
	QCHeight uint64
	QCRound  uint64
	EpochID  uint64

	VoterBitArray    *cmn.BitArray
	VoterBitArrayStr string
	VoterMsgHash     [][32]byte // [][32]byte
	VoterAggSig      []byte
}

func (qc *QuorumCert) String() string {
	qc.VoterBitArrayStr = "nil-BitArray"
	if qc.VoterBitArray != nil {
		qc.VoterBitArrayStr = qc.VoterBitArray.String()
	}
	if qc != nil {
		return fmt.Sprintf("QuorumCert(Height:%v, Round:%v, EpochID:%v, VoterBitArray:%v, len(VoterMsgHash):%v, len(VoterAggSig):%v)",
			qc.QCHeight, qc.QCRound, qc.EpochID, qc.VoterBitArrayStr, len(qc.VoterMsgHash), len(qc.VoterAggSig))
	}
	return "EMPTY QC"
}

func (qc *QuorumCert) CompactString() string {
	qc.VoterBitArrayStr = "nil-BitArray"
	if qc.VoterBitArray != nil {
		qc.VoterBitArrayStr = qc.VoterBitArray.String()
	}
	if qc != nil {
		hasAggSig := "no"
		if len(qc.VoterAggSig) > 0 {
			hasAggSig = "YES"
		}
		return fmt.Sprintf("QC(H:%v, R:%v, EpochID:%v, VoterBitArray:%v, VoterAggSig:%v)",
			qc.QCHeight, qc.QCRound, qc.EpochID, qc.VoterBitArrayStr, hasAggSig)
	}
	return "EMPTY QC"
}

func (qc *QuorumCert) ToBytes() []byte {
	bytes, _ := rlp.EncodeToBytes(qc)
	return bytes
}

// EncodeRLP implements rlp.Encoder.
func (qc *QuorumCert) EncodeRLP(w io.Writer) error {
	qc.VoterBitArrayStr = "nil-BitArray"
	if qc.VoterBitArray != nil {
		qc.VoterBitArrayStr = qc.VoterBitArray.String()
	}
	return rlp.Encode(w, []interface{}{
		qc.QCHeight,
		qc.QCRound,
		qc.EpochID,
		qc.VoterMsgHash,
		qc.VoterAggSig,
		qc.VoterBitArrayStr,
	})
}

// DecodeRLP implements rlp.Decoder.
func (qc *QuorumCert) DecodeRLP(s *rlp.Stream) error {
	payload := struct {
		QCHeight         uint64
		QCRound          uint64
		EpochID          uint64
		VoterMsgHash     [][32]byte
		VoterAggSig      []byte
		VoterBitArrayStr string
	}{}

	if err := s.Decode(&payload); err != nil {
		return err
	}

	// decode BitArray
	var bitArray *cmn.BitArray
	if payload.VoterBitArrayStr == "nil-BitArray" {
		bitArray = nil
	} else {
		n := len(payload.VoterBitArrayStr)
		bitArray = cmn.NewBitArray(n)
		for i := 0; i < n; i++ {
			if payload.VoterBitArrayStr[i] == 'x' {
				bitArray.SetIndex(i, true)
			}
		}
	}

	*qc = QuorumCert{
		QCHeight:         payload.QCHeight,
		QCRound:          payload.QCRound,
		EpochID:          payload.EpochID,
		VoterMsgHash:     payload.VoterMsgHash,
		VoterAggSig:      payload.VoterAggSig,
		VoterBitArray:    bitArray,
		VoterBitArrayStr: payload.VoterBitArrayStr,
	}
	return nil
}

func GenesisQC() *QuorumCert {
	return &QuorumCert{QCHeight: 0, QCRound: 0, EpochID: 0}
}

//--------------
func QCEncodeBytes(qc *QuorumCert) []byte {
	blockBytes, _ := rlp.EncodeToBytes(qc)
	return blockBytes
}

func QCDecodeFromBytes(bytes []byte) (*QuorumCert, error) {
	qc := QuorumCert{}
	err := rlp.DecodeBytes(bytes, &qc)
	return &qc, err
}
