package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dearcode/csi-lvm/pkg/adapter"
)

var (
	endpoint   string
	driverName string
	driverPath string
	nodeID     string
)

func init() {
	flag.Set("logtostderr", "true")
}

func main() {

	flag.CommandLine.Parse([]string{})

	cmd := &cobra.Command{
		Use:   "lvmAdapter",
		Short: "Flex volume adapter for CSI",
		Run: func(cmd *cobra.Command, args []string) {
			handle()
		},
	}

	cmd.Flags().AddGoFlagSet(flag.CommandLine)

	cmd.PersistentFlags().StringVar(&nodeID, "nodeid", "", "node id")
	cmd.MarkPersistentFlagRequired("nodeid")

	cmd.PersistentFlags().StringVar(&endpoint, "endpoint", "", "CSI endpoint")
	cmd.MarkPersistentFlagRequired("endpoint")

	cmd.PersistentFlags().StringVar(&driverPath, "driverpath", "", "path to flexvolume driver path")
	cmd.MarkPersistentFlagRequired("driverpath")

	cmd.PersistentFlags().StringVar(&driverName, "drivername", "", "name of the driver")
	cmd.MarkPersistentFlagRequired("drivername")

	if err := cmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "%s", err.Error())
		os.Exit(1)
	}

	os.Exit(0)
}

func handle() {
	lvmAdapter := adapter.New()
	lvmAdapter.Run(driverName, driverPath, nodeID, endpoint)
}
