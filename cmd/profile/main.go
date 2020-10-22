package main

import (
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/dfinlab/meter/consensus"
	bls "github.com/dfinlab/meter/crypto/multi_sig"
)

func main() {
	for true {
		common := consensus.NewBlsCommon()
		system := common.GetSystem()
		pubs := make([]bls.PublicKey, 0)
		privs := make([]bls.PrivateKey, 0)
		signs := make([]bls.Signature, 0)

		msgHash := sha256.Sum256([]byte("This is a message to be signed"))
		for i := 0; i < 11; i++ {
			pub, priv, err := bls.GenKeys(*system)
			// pubB64 := base64.StdEncoding.EncodeToString(system.PubKeyToBytes(pub))
			// privB64 := base64.StdEncoding.EncodeToString(system.PrivKeyToBytes(priv))
			// fmt.Println("PubKey: ", pubB64, "PrivKey:", privB64)

			if err != nil {
				fmt.Println("Error happened:", err)
				continue
			}
			pubs = append(pubs, pub)
			privs = append(privs, priv)
			sign := bls.Sign(msgHash, priv)
			// signB64 := base64.StdEncoding.EncodeToString(system.SigToBytes(sign))
			// fmt.Println("#", i, " : Signature: ", signB64)
			signs = append(signs, sign)
		}

		aggSign, err := bls.Aggregate(signs, *system)
		if err != nil {
			fmt.Println("Error during aggregate: ", err)
		}
		msgHashs := make([][sha256.Size]byte, 0)
		for i := 0; i < len(pubs); i++ {
			msgHashs = append(msgHashs, msgHash)
		}
		verified, err := bls.AggregateVerify(aggSign, msgHashs, pubs)
		if !verified {
			fmt.Println("NOT VERIFIED")
		} else {
			fmt.Println("VERIFIED")
		}

		// clean up
		common.Free()
		aggSign.Free()
		for _, p := range pubs {
			p.Free()
		}
		for _, p := range privs {
			p.Free()
		}
		for _, sign := range signs {
			sign.Free()
		}
		time.Sleep(2)
	}
}
