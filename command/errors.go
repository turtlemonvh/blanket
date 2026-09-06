package command

import (
	"errors"
	"fmt"
	"os"

	"github.com/turtlemonvh/blanket/client"
)

// printAPIError writes err to stderr as "error: <status> <message>" for a
// *client.APIError -- the shape a failed server call takes now that the
// client checks the response status before decoding it
// (turtlemonvh/blanket#112) -- or "error: <message>" for anything else
// (an unreachable server, a malformed response). It does not exit; the
// caller decides the exit code (1 for an API error, per the table in
// docs/usage.md).
func printAPIError(err error) {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		fmt.Fprintf(os.Stderr, "error: %d %s\n", apiErr.Status, apiErr.Message)
		return
	}
	fmt.Fprintf(os.Stderr, "error: %s\n", err.Error())
}
