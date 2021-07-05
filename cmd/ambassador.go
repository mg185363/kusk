package cmd

import (
	"fmt"
	"log"

	"github.com/spf13/cobra"

	"github.com/kubeshop/openapi-operator/generators/ambassador"
)

// ambassadorCmd represents the ambassador command
var (
	ambassadorCmd = &cobra.Command{
		Use:   "ambassador",
		Short: "Generates ambassador mappings for your cluster from the provided api specification",
		Long:  "",
		Run: func(cmd *cobra.Command, args []string) {
			generateMappings()
		},
	}

	ambassadorNamespace string
	basePath            string
	serviceName         string
	serviceNamespace    string
	trimPrefix          string
	rootOnly            bool
)

func generateMappings() {
	mappings, err := ambassador.GenerateMappings(ambassador.Options{
		ServiceNamespace: serviceNamespace,
		ServiceName:      serviceName,
		BasePath:         basePath,
		TrimPrefix:       trimPrefix,
		RootOnly:         rootOnly,
	}, apiSpecContents)

	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(mappings)
}

func init() {
	rootCmd.AddCommand(ambassadorCmd)

	// Here you will define your flags and configuration settings.
	// kusk generate -i petstore.yaml --generator=ambassador --service-name=petstore --service-namespace=default --root-only=true

	ambassadorCmd.Flags().StringVarP(
		&ambassadorNamespace,
		"ambassador-namespace",
		"",
		"ambassador",
		"",
	)

	ambassadorCmd.Flags().StringVarP(
		&serviceName,
		"service-name",
		"",
		"",
		"",
	)
	ambassadorCmd.MarkFlagRequired("service-name")

	ambassadorCmd.Flags().StringVarP(
		&serviceNamespace,
		"service-namespace",
		"",
		"default",
		"",
	)

	ambassadorCmd.Flags().BoolVarP(
		&rootOnly,
		"root-only",
		"",
		true,
		"",
	)

	ambassadorCmd.Flags().StringVarP(
		&basePath,
		"base-path",
		"",
		"",
		"",
	)

	ambassadorCmd.Flags().StringVarP(
		&trimPrefix,
		"trim-prefix",
		"",
		"",
		"",
	)
}