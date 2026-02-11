package dis_password

import (
	"fmt"

	"github.com/rclone/rclone/cmd"
	"github.com/rclone/rclone/fs/dis_operations"
	"github.com/spf13/cobra"
)

func init() {
	cmd.Root.AddCommand(commandDefinition)
}

var commandDefinition = &cobra.Command{
	Use:   "dis_password",
	Short: `Get the password for distributed operations.`,
	Long: `Get the password for distributed operations.
If the password does not exist, it will be generated.
`,
	Run: func(command *cobra.Command, args []string) {
		cmd.Run(true, true, command, func() error {
			password := dis_operations.TryGetPassword()
			if password != "" {
				fmt.Println(password)
			}
			return nil
		})
	},
}
