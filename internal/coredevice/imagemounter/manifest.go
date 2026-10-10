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

	"howett.net/plist"
)

const manifestName = "BuildManifest.plist"

// DeveloperDiskImage update_ddi.py publishes these fixed names. The
// installed DDI 27A9266e instead uses 022-20899-111 and 022-20886-111
// asset paths in its BuildManifest.plist.
var fixedNames = map[string]string{
	"PersonalizedDMG":            "Image.dmg",
	"LoadableTrustCache":         "Image.dmg.trustcache",
	"Cryptex1,GenericDmg":        "Image.dmg",
	"Cryptex1,GenericTrustCache": "Image.dmg.trustcache",
	"Cryptex1,CryptexInfoPlist":  "Image.dmg.cryptex_info",
	"Cryptex1,GenericVolume":     "Image.dmg.root_hash",
}

// DDI build 27A9266e uses Info.Variant
// "iOS Customer Developer Disk Image Cryptex".
const cryptexVariantSuffix = "Developer Disk Image Cryptex"

type manifest struct {
	BuildIdentities []buildIdentity `plist:"BuildIdentities"`
}

type buildIdentity map[string]any

func loadManifest(dir string) (*manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return nil, fmt.Errorf("imagemounter: %w", err)
	}
	return parseManifest(data)
}

func parseManifest(data []byte) (*manifest, error) {
	var m manifest
	if _, err := plist.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("imagemounter: decode %s: %w", manifestName, err)
	}
	return &m, nil
}

func (m *manifest) personalizedIdentity(board, chip uint64) (buildIdentity, error) {
	for _, identity := range m.BuildIdentities {
		identityBoard, boardOK := numberValue(identity["ApBoardID"])
		identityChip, chipOK := numberValue(identity["ApChipID"])
		if boardOK && chipOK && identityBoard == board && identityChip == chip {
			if _, ok := identity.entry("PersonalizedDMG"); ok {
				return identity, nil
			}
		}
	}
	return nil, fmt.Errorf("%w (board %#x, chip %#x)", ErrNoBuildIdentity, board, chip)
}

// All PersonalizedDMG identities in DDI build 27A9266e name the same
// image and trust-cache paths.
func (m *manifest) anyPersonalizedIdentity() (buildIdentity, error) {
	for _, identity := range m.BuildIdentities {
		if _, ok := identity.entry("PersonalizedDMG"); ok {
			return identity, nil
		}
	}
	return nil, errors.New("imagemounter: the build manifest has no personalized image")
}

func (m *manifest) cryptexIdentity() (buildIdentity, error) {
	for _, identity := range m.BuildIdentities {
		info, _ := identity["Info"].(map[string]any)
		variant, _ := info["Variant"].(string)
		if strings.HasSuffix(variant, cryptexVariantSuffix) {
			return identity, nil
		}
	}
	return nil, errors.New("imagemounter: the build manifest has no cryptex")
}

func (i buildIdentity) manifestEntries() map[string]any {
	entries, _ := i["Manifest"].(map[string]any)
	return entries
}

func (i buildIdentity) entry(key string) (map[string]any, bool) {
	entry, ok := i.manifestEntries()[key].(map[string]any)
	return entry, ok
}

// DDI build 27A9266e has SHA-384 digests for its Cryptex assets and most
// PersonalizedDMG identities; the 18 identities with SHA-1 are A8 and A9 boards.
func (i buildIdentity) digest(key string) ([]byte, error) {
	entry, ok := i.entry(key)
	if !ok {
		return nil, fmt.Errorf("imagemounter: the build identity has no %s", key)
	}
	digest, _ := entry["Digest"].([]byte)
	if len(digest) != sha512.Size384 {
		return nil, fmt.Errorf("imagemounter: %s has no SHA-384 digest", key)
	}
	return digest, nil
}

var errMissingFile = errors.New("imagemounter: missing")

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
			return resolvedFile{}, fmt.Errorf("imagemounter: %w", err)
		}
	}
	return resolvedFile{}, fmt.Errorf("%w %s in %s", errMissingFile, key, dir)
}

func (i buildIdentity) readFile(dir, key string) ([]byte, error) {
	file, err := i.resolve(dir, key)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(file.path)
	if err != nil {
		return nil, fmt.Errorf("imagemounter: %w", err)
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
		return fmt.Errorf("imagemounter: %w", err)
	}
	defer file.Close()
	hash := sha512.New384()
	if _, err = io.Copy(hash, file); err != nil {
		return fmt.Errorf("imagemounter: read %s: %w", f.path, err)
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
