/* Copyright © 2020 VMware, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/vmware/nsx-container-plugin-operator/pkg/apis"
	"github.com/vmware/nsx-container-plugin-operator/pkg/controller"
	"github.com/vmware/nsx-container-plugin-operator/version"

	"github.com/spf13/pflag"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var log = logf.Log.WithName("cmd")

func printVersion() {
	log.Info(fmt.Sprintf("Operator Version: %s", version.Version))
	log.Info(fmt.Sprintf("Go Version: %s", runtime.Version()))
	log.Info(fmt.Sprintf("Go OS/Arch: %s/%s", runtime.GOOS, runtime.GOARCH))
}

func main() {
	var metricsBindAddr string
	flag.StringVar(&metricsBindAddr, "metrics-server-bind-address", ":8181", "The address the prometheus metrics server binds to.")

	zapOpts := zap.Options{}
	zapOpts.BindFlags(flag.CommandLine)

	// Add flags registered by imported packages (e.g. glog and
	// controller-runtime)
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()

	logf.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))

	printVersion()

	// Check if we need to watch a specific namespace
	namespace := os.Getenv("WATCH_NAMESPACE")
	if namespace == "" {
		log.Info("WATCH_NAMESPACE not set, watching all namespaces")
	}

	if strings.Contains(namespace, ",") {
		namespace = ""
		log.Info(`This operator cannot handle multiple
			  namespaces. The operator will watch for changes
		          across all namespaces`)
	}

	// Create manager with appropriate options for controller-runtime v0.24.1
	options := manager.Options{
		Metrics: metricsserver.Options{
			BindAddress: metricsBindAddr,
		},
		LeaderElection:          false,
		LeaderElectionID:        "nsx-ncp-operator-lock",
		LeaderElectionNamespace: namespace,
	}

	mgr, err := manager.New(ctrl.GetConfigOrDie(), options)
	if err != nil {
		log.Error(err, "unable to start manager")
		os.Exit(1)
	}

	log.Info("Registering Components.")

	// Setup Scheme for all resources
	if err := apis.AddToScheme(mgr.GetScheme()); err != nil {
		log.Error(err, "unable to add APIs to scheme")
		os.Exit(1)
	}

	// Setup all Controllers
	if err := controller.AddToManager(mgr, namespace); err != nil {
		log.Error(err, "unable to add controllers to manager")
		os.Exit(1)
	}

	log.Info("Starting the Cmd.")

	// Start the Cmd
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "Manager exited non-zero")
		os.Exit(1)
	}
}
