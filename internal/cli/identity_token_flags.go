package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/packagemaze/maze-cli/internal/auth"
)

// identityTokenAliases pairs each identity-token input with its earlier
// --oidc-token-* name, which keeps working but is hidden from help.
var identityTokenAliases = []struct{ name, alias string }{
	{name: "identity-token-env", alias: "oidc-token-env"},
	{name: "identity-token-file", alias: "oidc-token-file"},
	{name: "identity-token-stdin", alias: "oidc-token-stdin"},
}

// bindIdentityTokenFlags registers where a supplied identity token comes from,
// into auth.Config's OIDCTokenEnv, OIDCTokenFile, and OIDCTokenStdin. Each
// name and its alias set the same field.
func bindIdentityTokenFlags(command *cobra.Command, env *string, file *string, stdin *bool) {
	flags := command.Flags()
	envHelp := "Environment variable holding the identity token; when it is empty, " + auth.IdentityTokenEnv + " is read"
	for _, name := range []string{"identity-token-env", "oidc-token-env"} {
		flags.StringVar(env, name, auth.DefaultOIDCTokenEnv, envHelp)
	}
	for _, name := range []string{"identity-token-file", "oidc-token-file"} {
		flags.StringVar(file, name, "", "File holding the identity token")
	}
	for _, name := range []string{"identity-token-stdin", "oidc-token-stdin"} {
		flags.BoolVar(stdin, name, false, "Read the identity token from stdin")
	}
	for _, pair := range identityTokenAliases {
		_ = flags.MarkHidden(pair.alias)
	}
}

// checkIdentityTokenAliases refuses a flag given under both its names, since
// only one of the two values could be used.
func checkIdentityTokenAliases(command *cobra.Command) error {
	flags := command.Flags()
	for _, pair := range identityTokenAliases {
		if flags.Changed(pair.name) && flags.Changed(pair.alias) {
			return fmt.Errorf("--%s is the earlier name of --%s; pass only one of them", pair.alias, pair.name)
		}
	}
	return nil
}
