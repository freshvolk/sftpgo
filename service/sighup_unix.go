// +build !windows

package service

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/freshvolk/sftpgo/dataprovider"
	"github.com/freshvolk/sftpgo/httpd"
	"github.com/freshvolk/sftpgo/logger"
)

func registerSigHup() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGHUP)
	go func() {
		for range sig {
			logger.Debug(logSender, "", "Received reload request")
			dataprovider.ReloadConfig()
			httpd.ReloadTLSCertificate()
		}
	}()
}
