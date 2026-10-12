package imagemounter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	E "github.com/sagernet/sing/common/exceptions"
)

type Variant int

const (
	Personalized Variant = iota + 1
	Cryptex
)

func (v Variant) String() string {
	if v == Cryptex {
		return "Cryptex"
	}
	return "Personalized"
}

func (v Variant) keys() []string {
	if v == Cryptex {
		return []string{"Cryptex1,CryptexInfoPlist", "Cryptex1,GenericVolume", "Cryptex1,GenericTrustCache", "Cryptex1,GenericDmg"}
	}
	return []string{"LoadableTrustCache", "PersonalizedDMG"}
}

func (v Variant) identity(m *manifest) (buildIdentity, error) {
	if v == Cryptex {
		return m.cryptexIdentity()
	}
	return m.anyPersonalizedIdentity()
}

func (v Variant) repositoryDirectory() string {
	if v == Cryptex {
		return "Xcode_iOS_DDI_Cryptex"
	}
	return "Xcode_iOS_DDI_Personalized"
}

// DeveloperDiskImage update_ddi.py publishes the variants under
// PersonalizedImages/Xcode_iOS_DDI_{Personalized,Cryptex}
// (DeveloperDiskImage 6eae353, update_ddi.py:42,60-75,260).
const repository = "https://raw.githubusercontent.com/doronz88/DeveloperDiskImage/main/PersonalizedImages"

func RepositoryURL(variant Variant) string {
	return repository + "/" + variant.repositoryDirectory()
}

func Verify(dir string, variant Variant) error {
	manifest, err := loadManifest(dir)
	if err != nil {
		return err
	}
	identity, err := variant.identity(manifest)
	if err != nil {
		return err
	}
	for _, key := range variant.keys() {
		file, err := identity.resolve(dir, key)
		if err != nil {
			return err
		}
		if err = file.verify(); err != nil {
			return err
		}
	}
	return nil
}

func HTTPSource(rawURL string) (Source, error) {
	base, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, E.Cause(err, "imagemounter")
	}
	if base.Scheme != "https" && base.Scheme != "http" || base.Host == "" {
		return nil, E.New("imagemounter: ", strconv.Quote(rawURL), " is not an HTTP or HTTPS URL")
	}
	base.Path = strings.TrimSuffix(strings.TrimSuffix(base.Path, "/"+manifestName), "/") + "/"
	base.RawPath = ""
	return &httpSource{base: base}, nil
}

type httpSource struct {
	base *url.URL
}

func (s *httpSource) String() string { return s.base.String() }

func (s *httpSource) Size(ctx context.Context, name string) (int64, error) {
	response, err := s.request(ctx, http.MethodHead, name)
	if err != nil {
		var status *httpStatusError
		if errors.As(err, &status) {
			return -1, nil
		}
		return 0, err
	}
	response.Body.Close()
	return response.ContentLength, nil
}

func (s *httpSource) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	response, err := s.request(ctx, http.MethodGet, name)
	if err != nil {
		return nil, err
	}
	return response.Body, nil
}

func (s *httpSource) request(ctx context.Context, method, name string) (*http.Response, error) {
	location := s.base.JoinPath(strings.Split(name, "/")...)
	request, err := http.NewRequestWithContext(ctx, method, location.String(), nil)
	if err != nil {
		return nil, E.Cause(err, "imagemounter")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, E.Cause(err, "imagemounter: download")
	}
	switch response.StatusCode {
	case http.StatusOK:
		return response, nil
	case http.StatusNotFound, http.StatusForbidden, http.StatusGone:
		response.Body.Close()
		return nil, &fs.PathError{Op: "download", Path: location.String(), Err: fs.ErrNotExist}
	}
	response.Body.Close()
	return nil, &httpStatusError{location: location.String(), status: response.Status}
}

type httpStatusError struct {
	location string
	status   string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("imagemounter: download %s: HTTP %s", e.location, e.status)
}

func writeFile(path string, write func(file *os.File) error) error {
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return E.Cause(err, "imagemounter")
	}
	defer os.Remove(file.Name())
	err = write(file)
	err = E.Append(err, file.Close(), func(err error) error {
		return E.Cause(err, "imagemounter")
	})
	if err != nil {
		return err
	}
	if err = os.Chmod(file.Name(), 0o644); err != nil {
		return E.Cause(err, "imagemounter")
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return E.Cause(err, "imagemounter")
	}
	return nil
}
