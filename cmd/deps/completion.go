package main

import (
	"github.com/urfave/cli/v3"
)

// completionHelp are the long help texts of the `deps completion <shell>` commands.
var completionHelp = map[string]string{
	"bash": `Generate the autocompletion script for the bash shell.

This script depends on the 'bash-completion' package.
If it is not installed already, you can install it via your OS's package manager.

To load completions in your current shell session:

	source <(deps completion bash)

To load completions for every new session, execute once:

#### Linux:

	deps completion bash > /etc/bash_completion.d/deps

#### macOS:

	deps completion bash > $(brew --prefix)/etc/bash_completion.d/deps

You will need to start a new shell for this setup to take effect.`,
	"zsh": `Generate the autocompletion script for the zsh shell.

If shell completion is not already enabled in your environment you will need
to enable it.  You can execute the following once:

	echo "autoload -U compinit; compinit" >> ~/.zshrc

To load completions in your current shell session:

	source <(deps completion zsh)

To load completions for every new session, execute once:

#### Linux:

	deps completion zsh > "${fpath[1]}/_deps"

#### macOS:

	deps completion zsh > $(brew --prefix)/share/zsh/site-functions/_deps

You will need to start a new shell for this setup to take effect.`,
	"fish": `Generate the autocompletion script for the fish shell.

To load completions in your current shell session:

	deps completion fish | source

To load completions for every new session, execute once:

	deps completion fish > ~/.config/fish/completions/deps.fish

You will need to start a new shell for this setup to take effect.`,
	"powershell": `Generate the autocompletion script for powershell.

The script registers the completer for the command named like the script file, so save it
as deps.ps1:

	deps completion powershell > deps.ps1

To load completions in your current shell session, run it:

	. ./deps.ps1

To load completions for every new session, run it from your powershell profile.`,
}

// configureCompletion shapes urfave/cli's built-in completion command (`deps completion
// bash|zsh|fish|powershell`): listed in help, with a help text per shell, and with the
// usage error handling of the other commands. The scripts complete by calling deps with
// --generate-shell-completion.
func configureCompletion(c *cli.Command) {
	c.Hidden = false
	c.Usage = "Generate the autocompletion script for the specified shell"
	c.Description = "Generate the autocompletion script for deps for the specified shell.\n" +
		"See each sub-command's help for details on how to use the generated script."
	c.OnUsageError = flagError
	// No `deps completion help`; `deps completion --help` lists the shells.
	c.HideHelpCommand = true
	for _, s := range c.Commands {
		if s.Name == "pwsh" {
			s.Name, s.Aliases = "powershell", []string{"pwsh"}
		}
		s.Usage = "Generate the autocompletion script for " + s.Name
		s.Description = completionHelp[s.Name]
		s.OnUsageError = flagError
		s.ArgValidator = noArgs
	}
}
