package misc

import (
	"io"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/fileperm"
	log "github.com/sirupsen/logrus"
)

func CopyConfigTemplate(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() {
		if errClose := in.Close(); errClose != nil {
			log.WithError(errClose).Warn("failed to close source config file")
		}
	}()

	if err = fileperm.EnsurePrivateDir(filepath.Dir(dst)); err != nil {
		return err
	}

	out, err := fileperm.OpenPrivateFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC)
	if err != nil {
		return err
	}
	defer func() {
		if errClose := out.Close(); errClose != nil {
			log.WithError(errClose).Warn("failed to close destination config file")
		}
	}()

	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
