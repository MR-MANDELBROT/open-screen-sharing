// The personalized mount follows go-ios ios/imagemounter
// (https://github.com/danielpaulus/go-ios, MIT License, Copyright (c) 2019
// danielpaulus).

package imagemounter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/rsd"
)

// pymobiledevice3 queries the device for an existing personalization
// manifest before requesting a ticket from TSS.
func (c *Client) MountPersonalized(ctx context.Context, dir string) error {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, uploadTimeout)
		defer cancel()
	}
	manifest, err := loadManifest(dir)
	if err != nil {
		return err
	}
	if err = c.checkCanMount(ctx); err != nil {
		return err
	}
	var (
		identifiers map[string]any
		identity    buildIdentity
		image       resolvedFile
		trustCache  []byte
		ticket      []byte
	)
	err = c.withMounter(ctx, func(conn *mounterConn) error {
		var err error
		if identifiers, err = conn.personalizationIdentifiers(); err != nil {
			return err
		}
		board, _ := numberValue(identifiers["BoardId"])
		chip, _ := numberValue(identifiers["ChipID"])
		if identity, err = manifest.personalizedIdentity(board, chip); err != nil {
			return err
		}
		if image, err = identity.resolve(dir, "PersonalizedDMG"); err != nil {
			return err
		}
		if trustCache, err = identity.readFile(dir, "LoadableTrustCache"); err != nil {
			return err
		}
		if err = image.verify(); err != nil {
			return err
		}
		// pymobiledevice3 uses the image SHA-384 for QueryPersonalizationManifest.
		ticket, err = conn.personalizationManifest(image.digest)
		return err
	})
	if err != nil {
		return err
	}
	// go-ios and pymobiledevice3 query the nonce on the connection used to
	// upload and mount the image.
	return c.withMounter(ctx, func(conn *mounterConn) error {
		if ticket == nil {
			c.logger.Info("requesting a Developer Disk Image ticket from Apple")
			var err error
			if ticket, err = c.personalizedTicket(ctx, conn, identity, identifiers); err != nil {
				return err
			}
		}
		if err := conn.upload(image, ticket); err != nil {
			return err
		}
		if err := conn.mount(ticket, trustCache); err != nil {
			return err
		}
		c.logger.Info("mounted the personalized Developer Disk Image")
		return conn.hangUp()
	})
}

func (c *Client) personalizedTicket(ctx context.Context, conn *mounterConn, identity buildIdentity, identifiers map[string]any) ([]byte, error) {
	ecid, err := c.ecid(ctx)
	if err != nil {
		return nil, err
	}
	nonce, err := conn.nonce()
	if err != nil {
		return nil, err
	}
	request, err := personalizedRequest(identity, identifiers, ecid, nonce)
	if err != nil {
		return nil, err
	}
	response, err := sendTSS(ctx, request)
	if err != nil {
		return nil, err
	}
	ticket, ok := response["ApImg4Ticket"].([]byte)
	if !ok {
		return nil, errors.New("imagemounter: TSS response without ApImg4Ticket")
	}
	return ticket, nil
}

func (c *Client) withMounter(ctx context.Context, fn func(conn *mounterConn) error) error {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultRequestTimeout)
		defer cancel()
	}
	conn, err := c.device.DialShim(ctx, mounterService)
	if err != nil {
		return fmt.Errorf("imagemounter: %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	err = fn(&mounterConn{conn: conn})
	if !stop() && err != nil && ctx.Err() != nil {
		return fmt.Errorf("imagemounter: image mounter: %w", ctx.Err())
	}
	return err
}

type mounterConn struct {
	conn *rsd.PlistConn
}

func (m *mounterConn) request(message map[string]any) (map[string]any, error) {
	command, _ := message["Command"].(string)
	if err := m.conn.Send(message); err != nil {
		return nil, fmt.Errorf("imagemounter: send %s: %w", command, err)
	}
	return m.receive(command)
}

func (m *mounterConn) receive(command string) (map[string]any, error) {
	reply, err := m.conn.Receive()
	if err != nil {
		return nil, fmt.Errorf("imagemounter: receive %s reply: %w", command, err)
	}
	if message, ok := reply["Error"]; ok {
		detail, _ := reply["DetailedError"].(string)
		return nil, &DeviceError{Command: command, Message: fmt.Sprint(message), Detail: detail}
	}
	return reply, nil
}

func (m *mounterConn) expectStatus(command string, reply map[string]any, want string) error {
	if status, _ := reply["Status"].(string); status != want {
		return fmt.Errorf("imagemounter: %s: Status %q, want %q", command, reply["Status"], want)
	}
	return nil
}

func (m *mounterConn) developerModeStatus() (bool, error) {
	reply, err := m.request(map[string]any{"Command": "QueryDeveloperModeStatus"})
	if err != nil {
		return false, err
	}
	enabled, ok := reply["DeveloperModeStatus"].(bool)
	if !ok {
		return false, errors.New("imagemounter: QueryDeveloperModeStatus reply without DeveloperModeStatus")
	}
	return enabled, nil
}

func (m *mounterConn) imageMounted(imageType string) (bool, error) {
	reply, err := m.request(map[string]any{"Command": "LookupImage", "ImageType": imageType})
	if err != nil {
		return false, err
	}
	signatures, ok := reply["ImageSignature"].([]any)
	if !ok {
		return false, errors.New("imagemounter: LookupImage reply without ImageSignature")
	}
	return len(signatures) > 0, nil
}

func (m *mounterConn) personalizationIdentifiers() (map[string]any, error) {
	reply, err := m.request(map[string]any{
		"Command":               "QueryPersonalizationIdentifiers",
		"PersonalizedImageType": personalizedDDIType,
	})
	if err != nil {
		return nil, err
	}
	identifiers, ok := reply["PersonalizationIdentifiers"].(map[string]any)
	if !ok {
		return nil, errors.New("imagemounter: QueryPersonalizationIdentifiers reply without PersonalizationIdentifiers")
	}
	return identifiers, nil
}

// pymobiledevice3 opens a new mounter connection after MissingManifestError
// before requesting a TSS ticket and uploading.
func (m *mounterConn) personalizationManifest(digest []byte) ([]byte, error) {
	reply, err := m.request(map[string]any{
		"Command":               "QueryPersonalizationManifest",
		"PersonalizedImageType": personalizedDDIType,
		"ImageType":             personalizedDDIType,
		"ImageSignature":        digest,
	})
	var deviceError *DeviceError
	if errors.As(err, &deviceError) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// pymobiledevice3 uses the returned ImageSignature as the upload and mount ticket.
	ticket, _ := reply["ImageSignature"].([]byte)
	return ticket, nil
}

func (m *mounterConn) nonce() ([]byte, error) {
	reply, err := m.request(map[string]any{
		"Command": "QueryNonce",
		// go-ios sends HostProcessName CoreDeviceService with QueryNonce.
		"HostProcessName":       "CoreDeviceService",
		"PersonalizedImageType": personalizedDDIType,
	})
	if err != nil {
		return nil, err
	}
	nonce, ok := reply["PersonalizationNonce"].([]byte)
	if !ok {
		return nil, errors.New("imagemounter: QueryNonce reply without PersonalizationNonce")
	}
	return nonce, nil
}

func (m *mounterConn) upload(image resolvedFile, ticket []byte) error {
	file, err := os.Open(image.path)
	if err != nil {
		return fmt.Errorf("imagemounter: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("imagemounter: %w", err)
	}
	const command = "ReceiveBytes"
	reply, err := m.request(map[string]any{
		"Command":        command,
		"ImageSignature": ticket,
		"ImageSize":      uint64(info.Size()),
		"ImageType":      personalizedImageType,
	})
	if err != nil {
		return err
	}
	if err = m.expectStatus(command, reply, "ReceiveBytesAck"); err != nil {
		return err
	}
	written, err := io.Copy(m.conn, file)
	if err != nil {
		return fmt.Errorf("imagemounter: upload the image: %w", err)
	}
	if written != info.Size() {
		return fmt.Errorf("imagemounter: uploaded %d of %d bytes", written, info.Size())
	}
	reply, err = m.receive(command)
	if err != nil {
		return err
	}
	return m.expectStatus(command, reply, "Complete")
}

func (m *mounterConn) mount(ticket, trustCache []byte) error {
	const command = "MountImage"
	reply, err := m.request(map[string]any{
		"Command":         command,
		"ImageSignature":  ticket,
		"ImageType":       personalizedImageType,
		"ImageTrustCache": trustCache,
	})
	if err != nil {
		return err
	}
	return m.expectStatus(command, reply, "Complete")
}

// go-ios sends Hangup without reading a reply.
func (m *mounterConn) hangUp() error {
	if err := m.conn.Send(map[string]any{"Command": "Hangup"}); err != nil {
		return fmt.Errorf("imagemounter: send Hangup: %w", err)
	}
	return nil
}
