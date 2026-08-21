// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 18:35:24
// Original filename: src/cmd/completion.go
// Bash/Zsh completion scripts via Cobra.

package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

var completionCmd = &cobra.Command{
	Use:   "completion",
	Short: "Generate shell completion scripts",
	Long: `Generate completion scripts for your shell.

Bash:
  $ source <($PROGRAM_NAME completion bash)
  # To persist:
  $ $PROGRAM_NAME completion bash | sudo tee /etc/bash_completion.d/$PROGRAM_NAME > /dev/null

Zsh:
  $ $PROGRAM_NAME completion zsh > ~/.zsh/_$PROGRAM_NAME
  $ echo 'fpath=($HOME/.zsh $fpath)' >> ~/.zshrc
  $ echo 'autoload -Uz compinit && compinit' >> ~/.zshrc
  # Or, for current session:
  $ source <($PROGRAM_NAME completion zsh)

You of course replace $PROGRAM_NAME with this tool's name
`,
}

var completionBashCmd = &cobra.Command{
	Use:   "bash",
	Short: "Generate a Bash completion script",
	RunE: func(cmd *cobra.Command, args []string) error {
		// V2 is recommended; writes to stdout
		return rootCmd.GenBashCompletionV2(os.Stdout, true)
	},
}

var completionZshCmd = &cobra.Command{
	Use:   "zsh",
	Short: "Generate a Zsh completion script",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Ensure the script is zsh-compatible
		return rootCmd.GenZshCompletion(os.Stdout)
	},
}

func init() {
	completionCmd.AddCommand(completionBashCmd, completionZshCmd)
}
