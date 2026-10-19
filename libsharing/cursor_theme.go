package libsharing

import "github.com/nekohasekai/open-screen-sharing/internal/cursortheme"

func ImportCursorTheme(source string, destination string) error {
	return cursortheme.Import(source, destination)
}
