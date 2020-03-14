package cmd

import (
	"os"

	"github.com/krarey/azure-cluster-upgrade/deploy"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var cfgFile string

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "azure-cluster-upgrade",
	Short: "Blue/Green upgrade operations for Azure Scale Sets",
	Long: `Interacts with the Azure API to perform a blue/green deployment.

Expects a Virtual Machine Scale Set whose configuration has recently been updated.
Expands the chosen scale set by a factor of two, and once all VMs have entered the
'Running' state, protects the replacement instances and reduces Scale Set capacity
to its original value.`,
	Run: deploy.Run,
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.Flags().StringP("subscription-id", "s", "", "Subscription ID")
	rootCmd.Flags().StringP("resource-group", "r", "", "Resource Group name")
	rootCmd.Flags().StringP("vm-scale-set", "v", "", "Virtual Machine Scale Set name")

	rootCmd.MarkFlagRequired("subscription-id")
	rootCmd.MarkFlagRequired("resource-group")
	rootCmd.MarkFlagRequired("vm-scale-set")
}
