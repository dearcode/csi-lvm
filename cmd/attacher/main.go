/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/golang/glog"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	csiclient "k8s.io/csi-api/pkg/client/clientset/versioned"
	csiinformers "k8s.io/csi-api/pkg/client/informers/externalversions"

	"github.com/dearcode/csi-lvm/pkg/attacher"
)

const (
	// Number of worker threads
	threads = 10

	// Default timeout of short CSI calls like GetPluginInfo
	csiTimeout = time.Second

	// Name of CSI plugin for dummy operation
	dummyAttacherName = "csi/dummy"
)

// Command line flags
var (
	kubeconfig        = flag.String("kubeconfig", "", "Absolute path to the kubeconfig file. Required only when running out of cluster.")
	resync            = flag.Duration("resync", 10*time.Minute, "Resync interval of the attacher.")
	connectionTimeout = flag.Duration("connection-timeout", 1*time.Minute, "Timeout for waiting for CSI driver socket.")
	csiAddress        = flag.String("csi-address", "/run/csi/socket", "Address of the CSI driver socket.")
	dummy             = flag.Bool("dummy", false, "Run in dummy mode, i.e. not connecting to CSI driver and marking everything as attached. Expected CSI driver name is \"csi/dummy\".")
	showVersion       = flag.Bool("version", false, "Show version.")
	timeout           = flag.Duration("timeout", 15*time.Second, "Timeout for waiting for attaching or detaching the volume.")

	enableLeaderElection    = flag.Bool("leader-election", false, "Enable leader election.")
	leaderElectionNamespace = flag.String("leader-election-namespace", "", "Namespace where this attacher runs.")
	leaderElectionIdentity  = flag.String("leader-election-identity", "", "Unique idenity of this attcher. Typically name of the pod where the attacher runs.")
)

var (
	version = "unknown"
)

func main() {
	flag.Set("logtostderr", "true")
	flag.Parse()

	if *showVersion {
		fmt.Println(os.Args[0], version)
		return
	}
	glog.Infof("Version: %s", version)

	// Create the client config. Use kubeconfig if given, otherwise assume in-cluster.
	config, err := buildConfig(*kubeconfig)
	if err != nil {
		glog.Error(err.Error())
		os.Exit(1)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		glog.Error(err.Error())
		os.Exit(1)
	}

	csiClientset, err := csiclient.NewForConfig(config)
	if err != nil {
		glog.Error(err.Error())
		os.Exit(1)
	}

	factory := informers.NewSharedInformerFactory(clientset, *resync)
	var csiFactory csiinformers.SharedInformerFactory
	var handler attacher.Handler

	var attacherName string
	if *dummy {
		// Do not connect to any CSI, mark everything as attached.
		handler = attacher.NewTrivialHandler(clientset)
		attacherName = dummyAttacherName
	} else {
		// Connect to CSI.
		csiConn, err := attacher.New(*csiAddress, *connectionTimeout)
		if err != nil {
			glog.Error(err.Error())
			os.Exit(1)
		}

		// Check it's ready
		if err = waitForDriverReady(csiConn, *connectionTimeout); err != nil {
			glog.Error(err.Error())
			os.Exit(1)
		}

		// Find driver name.
		ctx, cancel := context.WithTimeout(context.Background(), csiTimeout)
		defer cancel()
		attacherName, err = csiConn.GetDriverName(ctx)
		if err != nil {
			glog.Error(err.Error())
			os.Exit(1)
		}
		glog.V(2).Infof("CSI driver name: %q", attacherName)

		pvLister := factory.Core().V1().PersistentVolumes().Lister()
		nodeLister := factory.Core().V1().Nodes().Lister()
		vaLister := factory.Storage().V1beta1().VolumeAttachments().Lister()
		csiFactory := csiinformers.NewSharedInformerFactory(csiClientset, *resync)
		nodeInfoLister := csiFactory.Csi().V1alpha1().CSINodeInfos().Lister()
		handler = attacher.NewCSIHandler(clientset, attacherName, csiConn, pvLister, nodeLister, nodeInfoLister, vaLister, timeout)
		glog.V(2).Infof("CSI driver supports attacherPublishUnpublish, using real CSI handler")
	}

	if *enableLeaderElection {
		// Leader election was requested.
		if leaderElectionNamespace == nil || *leaderElectionNamespace == "" {
			glog.Error("-leader-election-namespace must not be empty")
			os.Exit(1)
		}
		if leaderElectionIdentity == nil || *leaderElectionIdentity == "" {
			glog.Error("-leader-election-identity must not be empty")
			os.Exit(1)
		}
		// Name of config map with leader election lock
		lockName := "external-attacher-leader-" + attacherName
		waitForLeader(clientset, *leaderElectionNamespace, *leaderElectionIdentity, lockName)
	}

	ctrl := attacher.NewCSIAttachController(
		clientset,
		attacherName,
		handler,
		factory.Storage().V1beta1().VolumeAttachments(),
		factory.Core().V1().PersistentVolumes(),
	)

	// run...
	stopCh := make(chan struct{})
	factory.Start(stopCh)
	if csiFactory != nil {
		csiFactory.Start(stopCh)
	}
	go ctrl.Run(threads, stopCh)

	// ...until SIGINT
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	<-c
	close(stopCh)
}

func buildConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	return rest.InClusterConfig()
}

func waitForDriverReady(csiConn attacher.CSIConnection, timeout time.Duration) error {
	now := time.Now()
	finish := now.Add(timeout)
	var err error
	for {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		err = csiConn.Probe(ctx)
		if err == nil {
			glog.V(2).Infof("Probe succeeded")
			return nil
		}
		glog.V(2).Infof("Probe failed with %s", err)

		now := time.Now()
		if now.After(finish) {
			return fmt.Errorf("Failed to probe the attacher: %s", err)
		}
		time.Sleep(time.Second)
	}
}
