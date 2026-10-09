package libsharing

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/coredevice"
	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/lockdown"
	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/vpnpeer"
	"github.com/nekohasekai/open-screen-sharing/internal/deviceserver"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb/media"
)

const (
	DeviceServerStopped    int32 = 0
	DeviceServerConnecting int32 = 1
	DeviceServerPreparing  int32 = 2
	DeviceServerRunning    int32 = 3
	DeviceServerFailed     int32 = 4
)

const (
	DeviceErrorNone             int32 = 0
	DeviceErrorNoTunnel         int32 = 1
	DeviceErrorPairingRejected  int32 = 2
	DeviceErrorDeveloperModeOff int32 = 3
	DeviceErrorNoDiskImage      int32 = 4
	DeviceErrorDiskImage        int32 = 5
	DeviceErrorListen           int32 = 6
	DeviceErrorUnsupported      int32 = 7
	DeviceErrorScreenInUse      int32 = 8
	DeviceErrorPairing          int32 = 9
	DeviceErrorLoopbackAddress  int32 = 10
	DeviceErrorOther            int32 = 100
)

type DeviceServerOptions struct {
	PairingRecord      []byte
	RemotePairingPort  int32
	Port               int32
	NoAuthentication   bool
	Username           string
	Password           string
	DiskImageDirectory string
}

type DeviceServerHandler interface {
	OnStateChanged(state int32, errorCode int32, message string)
	OnDeviceInfo(name string, model string, osVersion string)
	OnViewerConnected(viewerID int64, address string)
	OnViewerDisconnected(viewerID int64, errorCode int32, message string)
	OnStreaming(active bool)
}

const defaultRemotePairingPort = 49152

const vpnProbeTimeout = 2 * time.Second

func DetectLoopbackAddress(remotePairingPort int32) string {
	port := uint16(remotePairingPort)
	if port == 0 {
		port = defaultRemotePairingPort
	}
	address, err := vpnpeer.Find(context.Background(), vpnpeer.Candidates(), port, time.Second)
	if err != nil {
		return ""
	}
	return address.String()
}

type DeviceServer struct {
	pairingPort atomic.Uint32
	port        int32
	server      *deviceserver.Server
}

func NewDeviceServer(options *DeviceServerOptions, handler DeviceServerHandler) (*DeviceServer, error) {
	if options == nil || handler == nil {
		return nil, errors.New("options and handler are required")
	}
	file, err := parsePairingFile(options.PairingRecord)
	if err != nil {
		return nil, err
	}
	var deviceName string
	if file.remote != nil {
		deviceName = file.remote.DeviceName
	}
	name := deviceName
	if name == "" {
		name = "iPhone"
	}
	logger := newLogger()
	connectLogger := logger
	if connectLogger != nil {
		connectLogger = connectLogger.With("component", "device")
	}
	s := &DeviceServer{port: options.Port}
	s.setPairingPort(options.RemotePairingPort)
	server, err := deviceserver.New(deviceserver.Options{
		Connect: func(ctx context.Context) (*coredevice.Device, error) {
			port := uint16(s.pairingPort.Load())
			if file.lockdown != nil {
				port = lockdown.Port
			}
			address, err := vpnpeer.Find(ctx, vpnpeer.Candidates(), port, vpnProbeTimeout)
			if err != nil {
				return nil, err
			}
			if connectLogger != nil {
				connectLogger.Info("device address", "address", address)
			}
			if file.lockdown != nil {
				return coredevice.ConnectLockdown(ctx, coredevice.LockdownOptions{
					Address: address,
					Record:  file.lockdown,
					Logger:  connectLogger,
				})
			}
			return coredevice.ConnectRemotePairing(ctx, coredevice.RemotePairingOptions{
				Address: netip.AddrPortFrom(address, port),
				Record:  file.remote,
				Logger:  connectLogger,
			})
		},
		Logger:   logger,
		Observer: &deviceObserver{handler: handler, deviceName: deviceName},
		Listen:   net.JoinHostPort("::", strconv.Itoa(int(options.Port))),
		RFB: rfb.ServerOptions{
			Name:             name,
			Username:         options.Username,
			Password:         options.Password,
			NoAuthentication: options.NoAuthentication,
			Media: media.ServerStreamConfig{
				VideoPort: uint16(options.Port + 1),
				AudioPort: uint16(options.Port + 2),
			},
		},
		Audio:              true,
		SharedClipboard:    true,
		DiskImageDirectory: options.DiskImageDirectory,
	})
	if err != nil {
		return nil, err
	}
	s.server = server
	return s, nil
}

func (s *DeviceServer) Start() error {
	err := s.server.Start()
	if err != nil {
		return err
	}
	servingDevices.access.Lock()
	servingDevices.ports[s] = s.port
	servingDevices.access.Unlock()
	return nil
}

func (s *DeviceServer) SetRemotePairingPort(port int32) {
	if s.setPairingPort(port) {
		s.server.Retry()
	}
}

func (s *DeviceServer) setPairingPort(port int32) bool {
	if port == 0 {
		port = defaultRemotePairingPort
	}
	return s.pairingPort.Swap(uint32(port)) != uint32(port)
}

func (s *DeviceServer) Close() {
	servingDevices.access.Lock()
	delete(servingDevices.ports, s)
	servingDevices.access.Unlock()
	s.server.Close()
}

type deviceObserver struct {
	handler    DeviceServerHandler
	deviceName string
}

func (o *deviceObserver) StateChanged(state deviceserver.State, err error) {
	code, message := deviceError(err)
	o.handler.OnStateChanged(stateCode(state), code, message)
}

func (o *deviceObserver) DeviceConnected(info coredevice.DeviceInfo) {
	version := info.OSVersion
	if info.BuildVersion != "" {
		version += " (" + info.BuildVersion + ")"
	}
	o.handler.OnDeviceInfo(o.deviceName, info.ProductType, version)
}

func (o *deviceObserver) DiskImageProgress(received int64) {}

func (o *deviceObserver) ViewerConnected(session *rfb.ServerSession) {
	o.handler.OnViewerConnected(int64(session.ID()), viewerAddress(session))
}

func (o *deviceObserver) ViewerDisconnected(session *rfb.ServerSession, err error) {
	if err == nil {
		o.handler.OnViewerDisconnected(int64(session.ID()), DeviceErrorNone, "")
		return
	}
	o.handler.OnViewerDisconnected(int64(session.ID()), DeviceErrorOther, err.Error())
}

func (o *deviceObserver) StreamingChanged(active bool) {
	o.handler.OnStreaming(active)
}

func stateCode(state deviceserver.State) int32 {
	switch state {
	case deviceserver.StateConnecting:
		return DeviceServerConnecting
	case deviceserver.StatePreparing:
		return DeviceServerPreparing
	case deviceserver.StateRunning:
		return DeviceServerRunning
	case deviceserver.StateFailed:
		return DeviceServerFailed
	}
	return DeviceServerStopped
}

func deviceError(err error) (int32, string) {
	if err == nil {
		return DeviceErrorNone, ""
	}
	return int32(deviceserver.ProblemOf(err)), err.Error()
}

func viewerAddress(session *rfb.ServerSession) string {
	return session.RemoteAddr().(*net.TCPAddr).AddrPort().Addr().Unmap().String()
}
