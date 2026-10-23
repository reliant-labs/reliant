package component

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/pkg/components"
)

func newListCmd(_ *factory.Factory) *cobra.Command {
	var category string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all available components",
		RunE: func(cmd *cobra.Command, args []string) error {
			lib := components.NewLibrary()
			entries := lib.List("", category)
			fmt.Println(components.FormatComponentList(entries))
			return nil
		},
	}
	cmd.Flags().StringVarP(&category, "category", "c", "", "filter by category (layouts, charts, diagrams, deck, ui)")
	return cmd
}
