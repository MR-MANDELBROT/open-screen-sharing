package imagemounter

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
)

const XcodeDirectory = "/Library/Developer/DeveloperDiskImages/iOS_DDI/Restore"

type SSHOptions struct {
	Address    string
	User       string
	Password   string
	PrivateKey []byte
	Passphrase string
	Directory  string
}

type SSHSource struct {
	client    *ssh.Client
	address   string
	directory string
}

func DialSSH(ctx context.Context, options SSHOptions) (*SSHSource, error) {
	config := &ssh.ClientConfig{
		User:            options.User,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}
	if len(options.PrivateKey) > 0 {
		signer, err := ParsePrivateKey(options.PrivateKey, options.Passphrase)
		if err != nil {
			return nil, err
		}
		config.Auth = []ssh.AuthMethod{ssh.PublicKeys(signer)}
	} else {
		password := options.Password
		config.Auth = []ssh.AuthMethod{
			ssh.Password(password),
			ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for index := range answers {
					answers[index] = password
				}
				return answers, nil
			}),
		}
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", options.Address)
	if err != nil {
		return nil, fmt.Errorf("imagemounter: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	clientConn, channels, requests, err := ssh.NewClientConn(conn, options.Address, config)
	if !stop() {
		if err == nil {
			clientConn.Close()
		}
		conn.Close()
		return nil, ctx.Err()
	}
	if err != nil {
		conn.Close()
		if strings.Contains(err.Error(), "unable to authenticate") {
			return nil, fmt.Errorf("imagemounter: %s refused the user name, password or key: %w", options.Address, err)
		}
		return nil, fmt.Errorf("imagemounter: ssh %s: %w", options.Address, err)
	}
	directory := options.Directory
	if directory == "" {
		directory = XcodeDirectory
	}
	return &SSHSource{client: ssh.NewClient(clientConn, channels, requests), address: options.Address, directory: directory}, nil
}

func ParsePrivateKey(key []byte, passphrase string) (ssh.Signer, error) {
	signer, err := ssh.ParsePrivateKey(key)
	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) {
		if passphrase == "" {
			return nil, ErrPassphraseNeeded
		}
		signer, err = ssh.ParsePrivateKeyWithPassphrase(key, []byte(passphrase))
	}
	if err != nil {
		return nil, fmt.Errorf("imagemounter: private key: %w", err)
	}
	return signer, nil
}

var ErrPassphraseNeeded = errors.New("imagemounter: the private key needs its passphrase")

func (s *SSHSource) String() string {
	return s.address + ":" + s.directory
}

func (s *SSHSource) Close() error {
	return s.client.Close()
}

func (s *SSHSource) path(name string) (string, error) {
	file := s.directory + "/" + name
	for _, character := range file {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("/._-,", character)) {
			return "", fmt.Errorf("imagemounter: unexpected file name %q", name)
		}
	}
	return file, nil
}

func statError(file, errorOutput string, err error) error {
	message := strings.TrimSpace(errorOutput)
	if strings.Contains(message, "No such file") {
		return &fs.PathError{Op: "open", Path: file, Err: fs.ErrNotExist}
	}
	if message == "" && err != nil {
		message = err.Error()
	}
	return fmt.Errorf("imagemounter: read %s on the Mac: %s", file, message)
}

func (s *SSHSource) Size(ctx context.Context, name string) (int64, error) {
	file, err := s.path(name)
	if err != nil {
		return 0, err
	}
	session, err := s.client.NewSession()
	if err != nil {
		return 0, fmt.Errorf("imagemounter: ssh session: %w", err)
	}
	defer session.Close()
	stop := context.AfterFunc(ctx, func() { session.Close() })
	defer stop()
	var output, errorOutput bytes.Buffer
	session.Stdout = &output
	session.Stderr = &errorOutput
	runErr := session.Run("stat -f %z " + file)
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	size, parseErr := strconv.ParseInt(strings.TrimSpace(output.String()), 10, 64)
	if runErr != nil || parseErr != nil {
		return 0, statError(file, errorOutput.String(), runErr)
	}
	return size, nil
}

func (s *SSHSource) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	file, err := s.path(name)
	if err != nil {
		return nil, err
	}
	session, err := s.client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("imagemounter: ssh session: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { session.Close() })
	output, err := session.StdoutPipe()
	if err != nil {
		stop()
		session.Close()
		return nil, fmt.Errorf("imagemounter: %w", err)
	}
	var errorOutput bytes.Buffer
	session.Stderr = &errorOutput
	if err = session.Start("stat -f %z " + file + " && exec cat " + file); err != nil {
		stop()
		session.Close()
		return nil, fmt.Errorf("imagemounter: ssh: %w", err)
	}
	reader := bufio.NewReader(output)
	line, readErr := reader.ReadString('\n')
	size, parseErr := strconv.ParseInt(strings.TrimSpace(line), 10, 64)
	if readErr != nil || parseErr != nil {
		waitErr := session.Wait()
		stop()
		session.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, statError(file, errorOutput.String(), waitErr)
	}
	return &sshFile{reader: io.LimitReader(reader, size), size: size, session: session, stop: stop, name: file}, nil
}

type sshFile struct {
	reader  io.Reader
	size    int64
	read    int64
	session *ssh.Session
	stop    func() bool
	name    string
}

func (f *sshFile) Read(data []byte) (int, error) {
	count, err := f.reader.Read(data)
	f.read += int64(count)
	if err == io.EOF && f.read < f.size {
		err = fmt.Errorf("imagemounter: %s ended after %d of %d bytes", f.name, f.read, f.size)
	}
	return count, err
}

func (f *sshFile) Close() error {
	f.stop()
	return f.session.Close()
}
