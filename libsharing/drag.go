package libsharing

import (
	"strings"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
)

type RemoteDrag struct {
	Image []byte
	items []rfb.RemoteDragItem
}

func (d *RemoteDrag) Len() int32 {
	return int32(len(d.items))
}

func (d *RemoteDrag) Path(index int32) string {
	if index < 0 || int(index) >= len(d.items) {
		return ""
	}
	return d.items[index].Path
}

func (d *RemoteDrag) IsFolder(index int32) bool {
	if index < 0 || int(index) >= len(d.items) {
		return false
	}
	return d.items[index].Folder
}

func (s *Session) StartDrop(names string) (int64, error) {
	client, err := s.connected()
	if err != nil {
		return 0, err
	}
	var paths []string
	for _, name := range strings.Split(names, "\n") {
		if name != "" {
			paths = append(paths, "/"+name)
		}
	}
	id, err := client.SendDrag(rfb.NewFileClipboard(paths))
	return int64(id), err
}

func (s *Session) CancelDrag(dragID int64) error {
	client, err := s.connected()
	if err != nil {
		return err
	}
	return client.CancelDrag(uint32(dragID))
}

func (s *Session) CancelRemoteDrag() error {
	client, err := s.connected()
	if err != nil {
		return err
	}
	return client.CancelRemoteDrag()
}
