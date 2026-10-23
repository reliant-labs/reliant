package component

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/pkg/components"
)

func newSearchCmd(_ *factory.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "search <query>",
		Short: "Search components by keyword",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			lib := components.NewLibrary()
			query := strings.Join(args, " ")
			entries, matched, total := lib.SearchDetailed(query)
			if len(entries) == 0 {
				fmt.Printf("No components found matching %q\n", query)
				return nil
			}
			// Best-match search can answer with less than the whole query.
			// Say so, or a 2-of-4 hit reads as an exact one.
			if matched < total {
				fmt.Printf("No component matches all %d terms in %q — showing the best matches (%d of %d terms).\n",
					total, query, matched, total)
			}
			fmt.Println(components.FormatComponentList(entries))
			return nil
		},
	}
}
