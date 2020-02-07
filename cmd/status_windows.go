package cmd

import (
	"fmt"

	"github.com/freshvolk/sftpgo/service"
	"github.com/spf13/cobra"
)

var (
	statusCmd = &cobra.Command{
		Use:   "status",
		Short: "Retrieve the status for the SFTPGo Windows Service",
		Run: func(cmd *cobra.Command, args []string) {
			s := service.WindowsService{
				Service: service.Service{
					Shutdown: make(chan bool),
				},
			}
			status, err := s.Status()
			if err != nil {
				fmt.Printf("Error querying service status: %v\r\n", err)
			} else {
				fmt.Printf("Service status: %#v\r\n", status.String())
			}
		},
	}
)

func init() {
	serviceCmd.AddCommand(statusCmd)
}
