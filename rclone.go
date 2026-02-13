// Sync files and directories to and from local and remote object stores
//
// Nick Craig-Wood <nick@craig-wood.com>
package main

import (
	_ "github.com/rclone/rclone/backend/all" // import all backends
	"github.com/rclone/rclone/cmd"
	_ "github.com/rclone/rclone/cmd/all" // import all commands
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/dis_operations"
	_ "github.com/rclone/rclone/lib/plugin" // import plugins
)

func main() {
	config.PreDeleteRemote = dis_operations.DeleteDatamap
	cmd.Main()
}
