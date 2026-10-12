package rfb

import (
	"crypto/aes"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/binary"
	"math/big"

	E "github.com/sagernet/sing/common/exceptions"
)

const (
	srpIterations   = 142857
	srpSaltSize     = 32
	srpPrivateSize  = 64
	srpOptions      = "mda=SHA-512,replay_detection,conf+int=ChaCha20-Poly1305,kdf=SALTED-SHA512-PBKDF2"
	srpMessageLimit = 1 << 16
	srpWidth        = 512
)

const srpGroupPrime = "" +
	"FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD129024E088A67CC74020BBEA63B139B22514A08798E3404DD" +
	"EF9519B3CD3A431B302B0A6DF25F14374FE1356D6D51C245E485B576625E7EC6F44C42E9A637ED6B0BFF5CB6F406B7ED" +
	"EE386BFB5A899FA5AE9F24117C4B1FE649286651ECE45B3DC2007CB8A163BF0598DA48361C55D39A69163FA8FD24CF5F" +
	"83655D23DCA3AD961C62F356208552BB9ED529077096966D670C354E4ABC9804F1746C08CA18217C32905E462E36CE3B" +
	"E39E772C180E86039B2783A2EC07A28FB5C55DF06F4C52C9DE2BCBF6955817183995497CEA956AE515D2261898FA0510" +
	"15728E5A8AAAC42DAD33170D04507A33A85521ABDF1CBA64ECFB850458DBEF0A8AEA71575D060C7DB3970F85A6E1E4C7" +
	"ABF5AE8CDB0933D71E8C94E04A25619DCEE3D2261AD2EE6BF12FFA06D98A0864D87602733EC86A64521F2B18177B200C" +
	"BBE117577A615D6C770988C0BAD946E208E24FA074E5AB3143DB5BFCE0FD108E4B82D120A92108011A723C12A787E6D7" +
	"88719A10BDBA5B2699C327186AF4E23C1A946834B6150BDA2583E9CA2AD44CE8DBBBC2DB04DE8EF92E8EFC141FBECAA6" +
	"287C59474E6BC05D99B2964FA090C3A2233BA186515BE7ED1F612970CEE2D7AFB81BDD762170481CD0069127D5B05AA9" +
	"93B4EA988D8FDDC186FFB7DC90A6C08F4DF435C934063199FFFFFFFFFFFFFFFF"

var (
	srpModulus, _ = new(big.Int).SetString(srpGroupPrime, 16)
	srpGenerator  = big.NewInt(5)
	srpMultiplier = new(big.Int).SetBytes(srpHash(srpPad(srpModulus), srpPad(srpGenerator)))
)

type srpVerifier struct {
	salt     []byte
	verifier *big.Int
}

func newSRPVerifier(password string) *srpVerifier {
	salt := randomBytes(srpSaltSize)
	derived := passwordKey(password, salt, srpIterations, 128)
	exponent := new(big.Int).SetBytes(srpHash(salt, srpHash([]byte(":"), derived)))
	return &srpVerifier{salt: salt, verifier: new(big.Int).Exp(srpGenerator, exponent, srpModulus)}
}

func srpPad(value *big.Int) []byte {
	return value.FillBytes(make([]byte, srpWidth))
}

func srpHash(parts ...[]byte) []byte {
	digest := sha512.New()
	for _, part := range parts {
		digest.Write(part)
	}
	return digest.Sum(nil)
}

type srpExchange struct {
	verifier     *srpVerifier
	private      *big.Int
	serverPublic []byte
}

func (v *srpVerifier) start() *srpExchange {
	private := new(big.Int).SetBytes(randomBytes(srpPrivateSize))
	public := new(big.Int).Exp(srpGenerator, private, srpModulus)
	public.Add(public, new(big.Int).Mul(srpMultiplier, v.verifier))
	public.Mod(public, srpModulus)
	return &srpExchange{verifier: v, private: private, serverPublic: srpPad(public)}
}

func (e *srpExchange) challenge() []byte {
	salt := e.verifier.salt
	generator := srpGenerator.Bytes()
	fields := []byte{0}
	fields = binary.BigEndian.AppendUint16(fields, srpWidth)
	fields = append(fields, srpPad(srpModulus)...)
	fields = binary.BigEndian.AppendUint16(fields, uint16(len(generator)))
	fields = append(fields, generator...)
	fields = append(fields, byte(len(salt)))
	fields = append(fields, salt...)
	fields = binary.BigEndian.AppendUint16(fields, uint16(len(e.serverPublic)))
	fields = append(fields, e.serverPublic...)
	fields = binary.BigEndian.AppendUint64(fields, srpIterations)
	fields = binary.BigEndian.AppendUint16(fields, uint16(len(srpOptions)))
	fields = append(fields, srpOptions...)
	return srpEnvelope(fields)
}

func srpEnvelope(fields []byte) []byte {
	message := binary.BigEndian.AppendUint32(nil, uint32(4+len(fields)))
	message = binary.BigEndian.AppendUint32(message, uint32(len(fields)))
	return append(message, fields...)
}

func (e *srpExchange) verify(fields []byte) (serverProof []byte, wrapKey [16]byte, err error) {
	reader := &fieldReader{data: fields}
	clientPublicBytes := reader.take(reader.u16())
	clientProof := reader.take(reader.u8())
	if reader.err != nil {
		return nil, wrapKey, E.Cause(reader.err, "parse the SRP proof")
	}
	if len(clientPublicBytes) == 0 || len(clientPublicBytes) > srpWidth {
		return nil, wrapKey, E.New("SRP client value of ", len(clientPublicBytes), " bytes, want 1 to ", srpWidth)
	}
	clientPublic := new(big.Int).SetBytes(clientPublicBytes)
	if new(big.Int).Mod(clientPublic, srpModulus).Sign() == 0 {
		return nil, wrapKey, E.New("invalid SRP client value")
	}
	clientPadded := srpPad(clientPublic)
	scrambler := new(big.Int).SetBytes(srpHash(clientPadded, e.serverPublic))
	if scrambler.Sign() == 0 {
		return nil, wrapKey, E.New("invalid SRP scrambler")
	}
	base := new(big.Int).Exp(e.verifier.verifier, scrambler, srpModulus)
	base.Mul(base, clientPublic)
	base.Mod(base, srpModulus)
	premaster := new(big.Int).Exp(base, e.private, srpModulus)
	sessionKey := srpHash(srpPad(premaster))
	modulusHash := srpHash(srpPad(srpModulus))
	generatorHash := srpHash(srpPad(srpGenerator))
	subtle.XORBytes(modulusHash, modulusHash, generatorHash)
	expected := srpHash(modulusHash, srpHash(nil), e.verifier.salt, clientPadded, e.serverPublic, sessionKey)
	if subtle.ConstantTimeCompare(expected, clientProof) != 1 {
		return nil, wrapKey, E.New("the viewer's SRP proof does not match")
	}
	key := sha256.Sum256(sessionKey)
	copy(wrapKey[:], key[:16])
	return srpHash(clientPadded, clientProof, sessionKey), wrapKey, nil
}

// ScreenSharing parses the server proof, server IV, session ID and lifetime
// before accepting SRP step 3 (ScreenSharing 776.27, macOS 27.0.1 26A434,
// UUID EC0B2C20-F816-3FCE-822D-F9403C09EB50,
// _srp_client_mech_step 0x1cc13a4e8..0x1cc13a528). Apple
// passwordserver_sasl-215 sends a 16-byte IV, an empty session ID and a
// zero lifetime (passwordserver_sasl 1e1483f, cyrus_sasl/plugins/srp.c:110,
// cyrus_sasl/plugins/srp.c:1986, cyrus_sasl/plugins/srp.c:2076-2091).
func srpVerification(serverProof []byte) []byte {
	fields := append([]byte{byte(len(serverProof))}, serverProof...)
	fields = append(fields, 16)
	fields = append(fields, randomBytes(16)...)
	fields = binary.BigEndian.AppendUint16(fields, 0)
	fields = binary.BigEndian.AppendUint32(fields, 0)
	return srpEnvelope(fields)
}

func rekeyRectangle(wrapKey [16]byte, keys recordKeys) ([]byte, error) {
	block, err := aes.NewCipher(wrapKey[:])
	if err != nil {
		return nil, err
	}
	body := binary.BigEndian.AppendUint32(nil, 1)
	body = append(body, make([]byte, 32)...)
	block.Encrypt(body[4:20], keys.key[:])
	block.Encrypt(body[20:36], keys.iv[:])
	return framebufferUpdate(newRectangle(0, 0, 0, 0, EncodingRekey, body)), nil
}
