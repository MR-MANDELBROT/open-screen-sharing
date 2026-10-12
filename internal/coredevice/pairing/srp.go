package pairing

import (
	"crypto/rand"
	"crypto/sha512"
	"crypto/subtle"
	"math/big"
	"strings"

	E "github.com/sagernet/sing/common/exceptions"
)

// idevice pair-setup uses SRP with G_3072, SHA-512 and user "Pair-Setup"
// (idevice 3854a5d, idevice/src/remote_pairing/responder.rs:125-126, 262-263).

const srpUsername = "Pair-Setup"

var (
	srpPrime = mustParseHex(strings.Join([]string{
		"FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD129024E088A67CC74",
		"020BBEA63B139B22514A08798E3404DDEF9519B3CD3A431B302B0A6DF25F1437",
		"4FE1356D6D51C245E485B576625E7EC6F44C42E9A637ED6B0BFF5CB6F406B7ED",
		"EE386BFB5A899FA5AE9F24117C4B1FE649286651ECE45B3DC2007CB8A163BF05",
		"98DA48361C55D39A69163FA8FD24CF5F83655D23DCA3AD961C62F356208552BB",
		"9ED529077096966D670C354E4ABC9804F1746C08CA18217C32905E462E36CE3B",
		"E39E772C180E86039B2783A2EC07A28FB5C55DF06F4C52C9DE2BCBF695581718",
		"3995497CEA956AE515D2261898FA051015728E5A8AAAC42DAD33170D04507A33",
		"A85521ABDF1CBA64ECFB850458DBEF0A8AEA71575D060C7DB3970F85A6E1E4C7",
		"ABF5AE8CDB0933D71E8C94E04A25619DCEE3D2261AD2EE6BF12FFA06D98A0864",
		"D87602733EC86A64521F2B18177B200CBBE117577A615D6C770988C0BAD946E2",
		"08E24FA074E5AB3143DB5BFCE0FD108E4B82D120A93AD2CAFFFFFFFFFFFFFFFF",
	}, ""))
	srpGenerator = big.NewInt(5)
	srpLength    = (srpPrime.BitLen() + 7) / 8
	srpK         = new(big.Int).SetBytes(srpHash(srpPrime.Bytes(), srpPad(srpGenerator)))
)

func mustParseHex(s string) *big.Int {
	value, loaded := new(big.Int).SetString(s, 16)
	if !loaded {
		panic("invalid hex " + s)
	}
	return value
}

func srpHash(parts ...[]byte) []byte {
	hash := sha512.New()
	for _, part := range parts {
		hash.Write(part)
	}
	return hash.Sum(nil)
}

func srpPad(value *big.Int) []byte {
	return value.FillBytes(make([]byte, srpLength))
}

func srpPrivateKey(salt []byte, pin string) *big.Int {
	return new(big.Int).SetBytes(srpHash(salt, srpHash([]byte(srpUsername+":"+pin))))
}

func srpVerifier(salt []byte, pin string) *big.Int {
	return new(big.Int).Exp(srpGenerator, srpPrivateKey(salt, pin), srpPrime)
}

func srpScrambler(clientPublic, serverPublic *big.Int) *big.Int {
	return new(big.Int).SetBytes(srpHash(srpPad(clientPublic), srpPad(serverPublic)))
}

func srpClientProof(salt, clientPublic, serverPublic, key []byte) []byte {
	hashN := new(big.Int).SetBytes(srpHash(srpPrime.Bytes()))
	hashG := new(big.Int).SetBytes(srpHash(srpGenerator.Bytes()))
	return srpHash(hashN.Xor(hashN, hashG).Bytes(), srpHash([]byte(srpUsername)), salt, clientPublic, serverPublic, key)
}

func srpServerProof(clientPublic, clientProof, key []byte) []byte {
	return srpHash(clientPublic, clientProof, key)
}

type srpServer struct {
	salt     []byte
	verifier *big.Int
	private  *big.Int
	public   *big.Int
}

func newSRPServer(pin string) *srpServer {
	salt := make([]byte, 16)
	rand.Read(salt)
	server := &srpServer{salt: salt, verifier: srpVerifier(salt, pin)}
	kv := new(big.Int).Mul(srpK, server.verifier)
	// idevice retries b until its public key occupies all 384 bytes
	// (idevice 3854a5d, idevice/src/remote_pairing/responder.rs:275-282).
	privateBytes := make([]byte, 32)
	for {
		rand.Read(privateBytes)
		server.private = new(big.Int).SetBytes(privateBytes)
		server.public = new(big.Int).Exp(srpGenerator, server.private, srpPrime)
		server.public.Add(server.public, kv).Mod(server.public, srpPrime)
		if len(server.public.Bytes()) == srpLength {
			return server
		}
	}
}

func (s *srpServer) PublicKey() []byte {
	return s.public.Bytes()
}

func (s *srpServer) Verify(clientPublicBytes, clientProof []byte) (key []byte, serverProof []byte, err error) {
	clientPublic := new(big.Int).SetBytes(clientPublicBytes)
	if clientPublic.Sign() == 0 || clientPublic.Cmp(srpPrime) >= 0 {
		return nil, nil, E.New("pairing: invalid SRP public key of ", len(clientPublicBytes), " bytes")
	}
	u := srpScrambler(clientPublic, s.public)
	premaster := new(big.Int).Exp(s.verifier, u, srpPrime)
	premaster.Mul(premaster, clientPublic).Mod(premaster, srpPrime)
	premaster.Exp(premaster, s.private, srpPrime)
	for _, premasterBytes := range [][]byte{premaster.Bytes(), srpPad(premaster)} {
		key = srpHash(premasterBytes)
		expected := srpClientProof(s.salt, clientPublicBytes, s.public.Bytes(), key)
		if subtle.ConstantTimeCompare(expected, clientProof) == 1 {
			return key, srpServerProof(clientPublicBytes, clientProof, key), nil
		}
	}
	return nil, nil, E.New("pairing: SRP proof mismatch, the setup code is probably wrong")
}
