package libsharing

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strconv"

	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/imagemounter"
)

func DefaultDiskImageURL() string {
	return imagemounter.RepositoryURL(imagemounter.Cryptex)
}

func DiskImageImported(directory string) bool {
	return imagemounter.Present(directory, imagemounter.Cryptex) || imagemounter.Present(directory, imagemounter.Personalized)
}

type DiskImageImportHandler interface {
	OnProgress(received, total int64)
}

type DiskImageImport struct {
	directory string
	handler   DiskImageImportHandler
	ctx       context.Context
	cancel    context.CancelFunc
}

func NewDiskImageImport(directory string, handler DiskImageImportHandler) (*DiskImageImport, error) {
	if directory == "" || handler == nil {
		return nil, errors.New("a directory and a handler are required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &DiskImageImport{directory: directory, handler: handler, ctx: ctx, cancel: cancel}, nil
}

func (i *DiskImageImport) Download(url string) error {
	source, err := imagemounter.HTTPSource(url)
	if err != nil {
		return err
	}
	return i.run(source)
}

func (i *DiskImageImport) ImportFile(path string) error {
	source, closer, err := imagemounter.OpenFile(path)
	if err != nil {
		return err
	}
	defer closer.Close()
	err = i.run(source)
	if errors.Is(err, imagemounter.ErrNoManifest) {
		return errors.New("no developer disk image found; choose a copy of " + imagemounter.XcodeDirectory + " from a Mac with Xcode")
	}
	return err
}

type MacImportOptions struct {
	Host       string
	Port       int32
	Username   string
	Password   string
	PrivateKey []byte
	Passphrase string
}

func (i *DiskImageImport) ImportFromMac(options *MacImportOptions) error {
	if options == nil {
		return errors.New("no options")
	}
	port := options.Port
	if port == 0 {
		port = 22
	}
	source, err := imagemounter.DialSSH(i.ctx, imagemounter.SSHOptions{
		Address:    net.JoinHostPort(options.Host, strconv.Itoa(int(port))),
		User:       options.Username,
		Password:   options.Password,
		PrivateKey: options.PrivateKey,
		Passphrase: options.Passphrase,
	})
	if err != nil {
		return i.result(err)
	}
	defer source.Close()
	err = i.run(source)
	if errors.Is(err, imagemounter.ErrNoManifest) {
		return errors.New("this Mac has no developer disk image in " + imagemounter.XcodeDirectory + "; install Xcode on it and open Xcode once")
	}
	return err
}

func (i *DiskImageImport) Cancel() {
	i.cancel()
}

func (i *DiskImageImport) run(source imagemounter.Source) error {
	_, err := imagemounter.Import(i.ctx, source, filepath.Clean(i.directory), i.handler.OnProgress)
	return i.result(err)
}

func (i *DiskImageImport) result(err error) error {
	if err != nil && i.ctx.Err() != nil {
		return i.ctx.Err()
	}
	return err
}

func PrivateKeyNeedsPassphrase(key []byte) bool {
	_, err := imagemounter.ParsePrivateKey(key, "")
	return errors.Is(err, imagemounter.ErrPassphraseNeeded)
}
