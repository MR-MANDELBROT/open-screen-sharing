package rfb

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"math/big"
	"slices"

	E "github.com/sagernet/sing/common/exceptions"
)

const askGroupPrime = "" +
	"FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD129024E088A67CC74020BBEA63B139B22514A08798E3404DD" +
	"EF9519B3CD3A431B302B0A6DF25F14374FE1356D6D51C245E485B576625E7EC6F44C42E9A637ED6B0BFF5CB6F406B7ED" +
	"EE386BFB5A899FA5AE9F24117C4B1FE649286651ECE45B3DC2007CB8A163BF0598DA48361C55D39A69163FA8FD24CF5F" +
	"83655D23DCA3AD961C62F356208552BB9ED529077096966D670C354E4ABC9804F1746C08CA18217C32905E462E36CE3B" +
	"E39E772C180E86039B2783A2EC07A28FB5C55DF06F4C52C9DE2BCBF6955817183995497CEA956AE515D2261898FA0510" +
	"15728E5A8AACAA68FFFFFFFFFFFFFFFF"

const (
	askGenerator      = 2
	askKeySize        = 256
	askPrivateSize    = 32
	askCredentialSize = 128
)

var askModulus, _ = new(big.Int).SetString(askGroupPrime, 16)

func GrantsAsking(offered []SecurityType) bool {
	return slices.Equal(offered, []SecurityType{SecurityAppleSRP, SecurityAskControl})
}

func (s *ServerSession) askToShare() error {
	private := new(big.Int).SetBytes(randomBytes(askPrivateSize))
	public := new(big.Int).Exp(big.NewInt(askGenerator), private, askModulus)
	message := binary.BigEndian.AppendUint16(nil, askGenerator)
	message = binary.BigEndian.AppendUint16(message, askKeySize)
	message = append(message, askModulus.FillBytes(make([]byte, askKeySize))...)
	message = append(message, public.FillBytes(make([]byte, askKeySize))...)
	_, err := s.conn.Write(message)
	if err != nil {
		return err
	}
	response, err := s.stream.bytes(askCredentialSize + askKeySize)
	if err != nil {
		return E.Cause(err, "read the viewer's request")
	}
	viewerPublic := new(big.Int).SetBytes(response[askCredentialSize:])
	upper := new(big.Int).Sub(askModulus, big.NewInt(1))
	if viewerPublic.Cmp(big.NewInt(1)) <= 0 || viewerPublic.Cmp(upper) >= 0 {
		return E.New("invalid Diffie-Hellman value from the viewer")
	}
	shared := new(big.Int).Exp(viewerPublic, private, askModulus)
	key := md5.Sum(shared.FillBytes(make([]byte, askKeySize)))
	credentials := response[:askCredentialSize]
	err = decryptECB(key[:], credentials)
	if err != nil {
		return err
	}
	name, _, _ := bytes.Cut(credentials[:64], []byte{0})
	s.userName = string(name)
	s.wrapKey = key
	_, err = s.conn.Write([]byte{0, 0, 0, 0})
	return err
}
