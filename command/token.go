package command

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/client"
	"github.com/turtlemonvh/blanket/lib/auth"
	"github.com/turtlemonvh/blanket/server"
)

// `blanket token create|list|revoke` (turtlemonvh/blanket#101). Each is a
// call to the server's /token/ API, authenticated with this host's
// credentials file (or BLANKET_TOKEN / --token), which must be an admin
// token.

var tokenCreateConf struct {
	Name    string
	Scopes  []string
	Types   []string
	Tasks   []string
	Expires string
}

var tokenCmd = &cobra.Command{
	Use:   "token",
	Short: "Create, list and revoke API tokens (needs an admin token)",
}

var tokenCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Mint a new API token and print it once",
	Long: `Mint a new API token. The token is printed once and cannot be shown again.

--scopes takes scope names and preset names, comma-separated or repeated:
  presets: viewer (read), agent (read, submit),
           operator (read, submit, tasks:manage, types:write, workers:manage), admin
  scopes:  read, submit, tasks:manage, types:write, workers:manage, admin

An operator token can write task types, launch workers and submit tasks,
which together is code execution as the blanket user: treat it like admin.

--types limits submit to those task types and read to tasks of them.
--tasks limits read to those task ids. A token can always read and cancel
the tasks it created itself.`,
	Run: func(cmd *cobra.Command, args []string) {
		initCLI()
		if tokenCreateConf.Name == "" {
			fail("--name is required")
		}
		res, err := client.CreateToken(server.TokenCreateRequest{
			Name:    tokenCreateConf.Name,
			Scopes:  tokenCreateConf.Scopes,
			Types:   tokenCreateConf.Types,
			Tasks:   tokenCreateConf.Tasks,
			Expires: tokenCreateConf.Expires,
		}, viper.GetInt("port"))
		if err != nil {
			printAPIError(err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "created token %s (%s) with scopes %s\n", res.Id.Hex(), res.Name, scopeList(res.Scopes))
		if hasOperatorPowers(res.Scopes) {
			fmt.Fprintln(os.Stderr, "note: types:write + workers:manage + submit amounts to code execution as the blanket user")
		}
		fmt.Fprintln(os.Stderr, "store it now; it will not be shown again:")
		fmt.Println(res.Secret)
	},
}

var tokenListCmd = &cobra.Command{
	Use:   "list",
	Short: "List API tokens (never their values)",
	Run: func(cmd *cobra.Command, args []string) {
		initCLI()
		ts, err := client.ListTokens(viper.GetInt("port"))
		if err != nil {
			printAPIError(err)
			os.Exit(1)
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tSCOPES\tRESTRICTIONS\tCREATED\tLAST USED\tEXPIRES")
		for _, t := range ts {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", t.Id.Hex(), t.Name, scopeList(t.Scopes),
				restrictionList(t.Restrictions), ts2s(t.CreatedTs), ts2s(t.LastUsedTs), ts2s(t.ExpiresTs))
		}
		w.Flush()
	},
}

var tokenRevokeCmd = &cobra.Command{
	Use:   "revoke <id>",
	Short: "Revoke an API token by id",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		initCLI()
		if err := client.RevokeToken(args[0], viper.GetInt("port")); err != nil {
			printAPIError(err)
			os.Exit(1)
		}
		fmt.Printf("revoked %s\n", args[0])
	},
}

func init() {
	f := tokenCreateCmd.Flags()
	f.StringVar(&tokenCreateConf.Name, "name", "", "what the token is for, e.g. \"claude-desktop\" (required)")
	f.StringSliceVar(&tokenCreateConf.Scopes, "scopes", nil, "scopes and/or presets, e.g. agent or read,tasks:manage (required)")
	f.StringSliceVar(&tokenCreateConf.Types, "types", nil, "restrict to these task types")
	f.StringSliceVar(&tokenCreateConf.Tasks, "tasks", nil, "restrict read to these task ids")
	f.StringVar(&tokenCreateConf.Expires, "expires", "", "expire after this long, e.g. 90d or 12h (default: never)")
	tokenCmd.AddCommand(tokenCreateCmd, tokenListCmd, tokenRevokeCmd)
	RootCmd.AddCommand(tokenCmd)
}

// initCLI is the usual config + quiet logging for a one-shot client
// command.
func initCLI() {
	InitializeConfig()
	viper.Set("logLevel", "error")
	InitializeLogging()
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "error: "+msg)
	os.Exit(1)
}

func scopeList(ss []auth.Scope) string {
	parts := make([]string, len(ss))
	for i, s := range ss {
		parts[i] = string(s)
	}
	return strings.Join(parts, ",")
}

func restrictionList(r auth.Restrictions) string {
	var parts []string
	if len(r.Types) > 0 {
		parts = append(parts, "types="+strings.Join(r.Types, ","))
	}
	if len(r.Tasks) > 0 {
		parts = append(parts, "tasks="+strings.Join(r.Tasks, ","))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " ")
}

func hasOperatorPowers(ss []auth.Scope) bool {
	t := auth.Token{Scopes: ss}
	return t.HasScope(auth.ScopeTypesWrite) && t.HasScope(auth.ScopeWorkersManage)
}

func ts2s(ts int64) string {
	if ts == 0 {
		return "-"
	}
	return time.Unix(ts, 0).Format("2006-01-02 15:04")
}
