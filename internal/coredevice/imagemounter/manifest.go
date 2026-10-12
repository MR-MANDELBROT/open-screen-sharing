package imagemounter

import (
	"bytes"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	"howett.net/plist"
)

const manifestName = "BuildManifest.plist"

// DeveloperDiskImage update_ddi.py publishes these fixed names
// (DeveloperDiskImage 6eae353, update_ddi.py:60-74). DDI 27A9266e instead
// uses 022-20899-111 and 022-20886-111 asset paths in its BuildManifest.plist
// (BuildManifest.plist, DDI 27A9266e).
var fixedNames = map[string]string{
	"PersonalizedDMG":            "Image.dmg",
	"LoadableTrustCache":         "Image.dmg.trustcache",
	"Cryptex1,GenericDmg":        "Image.dmg",
	"Cryptex1,GenericTrustCache": "Image.dmg.trustcache",
	"Cryptex1,CryptexInfoPlist":  "Image.dmg.cryptex_info",
	"Cryptex1,GenericVolume":     "Image.dmg.root_hash",
}

// DDI build 27A9266e uses Info.Variant
// "iOS Customer Developer Disk Image Cryptex" (BuildManifest.plist,
// DDI 27A9266e).
const cryptexVariantSuffix = "Developer Disk Image Cryptex"

type manifest struct {
	BuildIdentities []buildIdentity `plist:"BuildIdentities"`
}

type buildIdentity map[string]any

func loadManifest(dir string) (*manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return nil, E.Cause(err, "imagemounter")
	}
	return parseManifest(data)
}

func parseManifest(data []byte) (*manifest, error) {
	var m manifest
	if _, err := plist.Unmarshal(data, &m); err != nil {
		return nil, E.Cause(err, "imagemounter: decode ", manifestName)
	}
	return &m, nil
}

func (m *manifest) personalizedIdentity(board, chip uint64) (buildIdentity, error) {
	identity := common.Find(m.BuildIdentities, func(identity buildIdentity) bool {
		identityBoard, boardOK := numberValue(identity["ApBoardID"])
		identityChip, chipOK := numberValue(identity["ApChipID"])
		if !boardOK || !chipOK || identityBoard != board || identityChip != chip {
			return false
		}
		_, hasImage := identity.entry("PersonalizedDMG")
		return hasImage
	})
	if identity == nil {
		return nil, E.New("imagemounter: the Developer Disk Image does not list this device (board 0x", strconv.FormatUint(board, 16), ", chip 0x", strconv.FormatUint(chip, 16), ")")
	}
	return identity, nil
}

// All PersonalizedDMG identities in DDI build 27A9266e name the same
// image and trust-cache paths (BuildManifest.plist, DDI 27A9266e).
func (m *manifest) anyPersonalizedIdentity() (buildIdentity, error) {
	identity := common.Find(m.BuildIdentities, func(identity buildIdentity) bool {
		_, hasImage := identity.entry("PersonalizedDMG")
		return hasImage
	})
	if identity == nil {
		return nil, E.New("imagemounter: the build manifest has no personalized image")
	}
	return identity, nil
}

func (m *manifest) cryptexIdentity() (buildIdentity, error) {
	identity := common.Find(m.BuildIdentities, func(identity buildIdentity) bool {
		info, _ := identity["Info"].(map[string]any)
		variant, _ := info["Variant"].(string)
		return strings.HasSuffix(variant, cryptexVariantSuffix)
	})
	if identity == nil {
		return nil, E.New("imagemounter: the build manifest has no cryptex")
	}
	return identity, nil
}

func (i buildIdentity) manifestEntries() map[string]any {
	entries, _ := i["Manifest"].(map[string]any)
	return entries
}

func (i buildIdentity) entry(key string) (map[string]any, bool) {
	entry, ok := i.manifestEntries()[key].(map[string]any)
	return entry, ok
}

// DDI build 27A9266e has SHA-384 digests for its Cryptex assets and 122 of
// its 140 PersonalizedDMG identities; the 18 identities with SHA-1 digests
// have ApChipID 0x7000, 0x7001, 0x8000, 0x8001 or 0x8003 (A8, A8X, A9, A9X)
// (BuildManifest.plist, DDI 27A9266e).
func (i buildIdentity) digest(key string) ([]byte, error) {
	entry, ok := i.entry(key)
	if !ok {
		return nil, E.New("imagemounter: the build identity has no ", key)
	}
	digest, _ := entry["Digest"].([]byte)
	if len(digest) != sha512.Size384 {
		return nil, E.New("imagemounter: ", key, " has no SHA-384 digest")
	}
	return digest, nil
}

var errMissingFile = E.New("imagemounter: missing file")

type resolvedFile struct {
	key    string
	path   string
	digest []byte
}

func (i buildIdentity) names(key string) []string {
	entry, _ := i.entry(key)
	info, _ := entry["Info"].(map[string]any)
	names := make([]string, 0, 2)
	if path, _ := info["Path"].(string); path != "" && fs.ValidPath(path) {
		names = append(names, path)
	}
	if name, ok := fixedNames[key]; ok && !slices.Contains(names, name) {
		names = append(names, name)
	}
	return names
}

func (i buildIdentity) resolve(dir, key string) (resolvedFile, error) {
	digest, err := i.digest(key)
	if err != nil {
		return resolvedFile{}, err
	}
	for _, name := range i.names(key) {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if _, err := os.Stat(path); err == nil {
			return resolvedFile{key: key, path: path, digest: digest}, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return resolvedFile{}, E.Cause(err, "imagemounter")
		}
	}
	return resolvedFile{}, E.Extend(errMissingFile, key, " in ", dir)
}

func (i buildIdentity) readFile(dir, key string) ([]byte, error) {
	file, err := i.resolve(dir, key)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(file.path)
	if err != nil {
		return nil, E.Cause(err, "imagemounter")
	}
	sum := sha512.Sum384(data)
	if !bytes.Equal(sum[:], file.digest) {
		return nil, file.mismatch(sum[:])
	}
	return data, nil
}

func (f resolvedFile) verify() error {
	file, err := os.Open(f.path)
	if err != nil {
		return E.Cause(err, "imagemounter")
	}
	defer file.Close()
	hash := sha512.New384()
	if _, err = io.Copy(hash, file); err != nil {
		return E.Cause(err, "imagemounter: read ", f.path)
	}
	if sum := hash.Sum(nil); !bytes.Equal(sum, f.digest) {
		return f.mismatch(sum)
	}
	return nil
}

func (f resolvedFile) mismatch(sum []byte) error {
	return &mismatchError{path: f.path, sum: sum, want: f.digest}
}

type mismatchError struct {
	path      string
	sum, want []byte
}

func (e *mismatchError) Error() string {
	return fmt.Sprintf("imagemounter: %s does not match the build manifest (SHA-384 %s, want %s)",
		e.path, hex.EncodeToString(e.sum[:min(8, len(e.sum))]), hex.EncodeToString(e.want[:min(8, len(e.want))]))
}

func parseNumber(text string) (uint64, bool) {
	base := 10
	if trimmed, ok := strings.CutPrefix(strings.ToLower(text), "0x"); ok {
		text, base = trimmed, 16
	}
	value, err := strconv.ParseUint(text, base, 64)
	return value, err == nil
}
