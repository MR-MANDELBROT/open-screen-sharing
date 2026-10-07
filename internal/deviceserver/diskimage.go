package deviceserver

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/nekohasekai/open-screen-sharing/internal/coredevice"
	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/imagemounter"
)

var errRemounted = errors.New("the Developer Disk Image is mounted now; reconnecting to reach its services")

// DDI 27A9266e has a Cryptex identity without ApBoardID or ApChipID; its personalized identities include both.
var diskImageVariants = []imagemounter.Variant{imagemounter.Cryptex, imagemounter.Personalized}

func (s *Server) prepareDiskImage(ctx context.Context, device *coredevice.Device, images *imagemounter.Client) error {
	if device.HasService(displayService) {
		s.relisted = false
		return nil
	}
	statusContext, cancel := context.WithTimeout(ctx, requestTimeout)
	status, err := images.Status(statusContext)
	cancel()
	if err != nil {
		return fmt.Errorf("read the Developer Disk Image's state: %w", err)
	}
	if status.Mounted() {
		if !s.relisted {
			s.relisted = true
			return errRemounted
		}
		return nil
	}
	if !status.DeveloperMode {
		return newError(ProblemDeveloperModeOff, "Developer Mode is off; turn it on in Settings › Privacy & Security › Developer Mode")
	}
	directory := s.options.DiskImageDirectory
	if directory == "" {
		return newError(ProblemNoDiskImage, "the Developer Disk Image is not mounted, and there is no directory for its files")
	}
	var failure error
	variants := diskImageVariants
	if status.CryptexError != nil {
		// The Cryptex variant uses com.apple.security.cryptexd.remote; the personalized variant uses mobile_image_mounter.
		s.logger.Warn("cryptexd failed; trying the personalized disk image first", "error", status.CryptexError)
		failure = fmt.Errorf("cryptexd: %w", status.CryptexError)
		variants = []imagemounter.Variant{imagemounter.Personalized, imagemounter.Cryptex}
	}
	found, hasCopy := false, false
	for _, variant := range variants {
		files, err := s.diskImageFiles(ctx, directory, variant)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.logger.Info("disk image files", "variant", variant, "error", err)
			failure = errors.Join(failure, err)
			hasCopy = hasCopy || !errors.Is(err, fs.ErrNotExist)
			continue
		}
		found = true
		s.logger.Info("mounting the Developer Disk Image", "variant", variant, "directory", files)
		if variant == imagemounter.Cryptex {
			err = images.MountCryptex(ctx, files)
		} else {
			err = images.MountPersonalized(ctx, files)
		}
		switch {
		case err == nil, errors.Is(err, imagemounter.ErrAlreadyMounted):
			s.logger.Info("mounted the Developer Disk Image", "variant", variant)
			s.relisted = true
			return errRemounted
		case errors.Is(err, imagemounter.ErrDeveloperModeOff):
			return &Error{Problem: ProblemDeveloperModeOff, Err: err}
		case ctx.Err() != nil:
			return ctx.Err()
		}
		s.logger.Warn("mount the Developer Disk Image", "variant", variant, "error", err)
		failure = errors.Join(failure, fmt.Errorf("mount %v: %w", variant, err))
	}
	if !found && !s.options.DownloadDiskImage {
		if !hasCopy {
			return newError(ProblemNoDiskImage, "the Developer Disk Image is not mounted, and there is no copy of it to mount")
		}
		return &Error{Problem: ProblemNoDiskImage, Err: fmt.Errorf("the Developer Disk Image is not mounted, and %s has no usable copy: %w", directory, failure)}
	}
	return &Error{Problem: ProblemDiskImage, Err: failure}
}

func (s *Server) diskImageFiles(ctx context.Context, directory string, variant imagemounter.Variant) (string, error) {
	if imagemounter.Verify(directory, variant) == nil {
		return directory, nil
	}
	own := filepath.Join(directory, variant.String())
	err := imagemounter.Verify(own, variant)
	if err == nil {
		return own, nil
	}
	if !s.options.DownloadDiskImage {
		return "", fmt.Errorf("%v: %w", variant, err)
	}
	source, err := imagemounter.HTTPSource(imagemounter.RepositoryURL(variant))
	if err != nil {
		return "", err
	}
	s.logger.Info("downloading the Developer Disk Image", "variant", variant, "directory", own)
	_, err = imagemounter.Import(ctx, source, directory, func(received, _ int64) {
		s.notify(func(observer Observer) { observer.DiskImageProgress(received) })
	})
	if err != nil {
		return "", fmt.Errorf("download %v: %w", variant, err)
	}
	if err = imagemounter.Verify(own, variant); err != nil {
		return "", fmt.Errorf("download %v: %w", variant, err)
	}
	return own, nil
}
