// Sync files and directories to and from local and remote object stores
//
// Nick Craig-Wood <nick@craig-wood.com>
package main

import (
	_ "github.com/rclone/rclone/backend/all" // import all backends
	"github.com/rclone/rclone/cmd"
	_ "github.com/rclone/rclone/cmd/all"    // import all commands
	_ "github.com/rclone/rclone/lib/plugin" // import plugins
	"github.com/rclone/rclone/fs/config"    // DeleteRemote의 PreDeleteCheckFunc 정의하기 위해 import
    "github.com/rclone/rclone/fs/dis_operations" // PreDeleteCheckFunc의 알맹이 함수
)

func main() {
	config.PreDeleteCheckFunc = dis_operations.CheckAndDeleteRemote
	config.PostDeleteActionFunc = dis_operations.ReuploadMigratedFiles
	cmd.Main()
}
