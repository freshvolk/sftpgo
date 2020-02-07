// Full featured and highly configurable SFTP server.
// For more details about features, installation, configuration and usage please refer to the README inside the source tree:
// https://github.com/freshvolk/sftpgo/blob/master/README.md
package main // import "github.com/freshvolk/sftpgo"

import (
	"github.com/freshvolk/sftpgo/cmd"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
)

func main() {
	cmd.Execute()
}
