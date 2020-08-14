// Copyright (c) 2020 Doc.ai and/or its affiliates.
//
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at:
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"net/url"
	"os"
	"time"

	"github.com/networkservicemesh/sdk/pkg/registry/common/connect"
	"github.com/networkservicemesh/sdk/pkg/registry/common/dnsresolve"
	"github.com/networkservicemesh/sdk/pkg/registry/common/swap"

	nested "github.com/antonfisher/nested-logrus-formatter"
	"github.com/kelseyhightower/envconfig"
	api_registry "github.com/networkservicemesh/api/pkg/api/registry"
	"github.com/sirupsen/logrus"
	"github.com/spiffe/go-spiffe/v2/spiffetls/tlsconfig"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/networkservicemesh/sdk/pkg/networkservice/chains/registry"

	"github.com/networkservicemesh/sdk/pkg/registry/core/chain"
	"github.com/networkservicemesh/sdk/pkg/tools/debug"
	"github.com/networkservicemesh/sdk/pkg/tools/grpcutils"
	"github.com/networkservicemesh/sdk/pkg/tools/log"
	"github.com/networkservicemesh/sdk/pkg/tools/signalctx"
)

// Config is configuration for cmd-registry-proxy-dns
type Config struct {
	ListenOn       url.URL `default:"unix:///listen.on.socket" desc:"url to listen on" split_words:"true"`
	ProxyNSMgrURL  url.URL `desc:"url to proxy nsmgr"`
	PublicNSMgrURL url.URL `desc:"url to nsmgr"`
	Domain         string  `desc:"domain name"`
}

func main() {
	// Setup context to catch signals
	ctx := signalctx.WithSignals(context.Background())
	ctx, cancel := context.WithCancel(ctx)

	// Setup logging
	logrus.SetFormatter(&nested.Formatter{})
	logrus.SetLevel(logrus.TraceLevel)
	ctx = log.WithField(ctx, "cmd", os.Args[0])

	// Debug self if necessary
	if err := debug.Self(); err != nil {
		log.Entry(ctx).Infof("%s", err)
	}

	startTime := time.Now()

	// Get config from environment
	config := &Config{}
	if err := envconfig.Usage("registry-proxy-dns", config); err != nil {
		logrus.Fatal(err)
	}
	if err := envconfig.Process("registry-proxy-dns", config); err != nil {
		logrus.Fatalf("error processing config from env: %+v", err)
	}

	log.Entry(ctx).Infof("Config: %#v", config)

	// Get a X509Source
	source, err := workloadapi.NewX509Source(ctx)
	if err != nil {
		logrus.Fatalf("error getting x509 source: %+v", err)
	}
	svid, err := source.GetX509SVID()
	if err != nil {
		logrus.Fatalf("error getting x509 svid: %+v", err)
	}
	logrus.Infof("SVID: %q", svid.ID)

	nseChain := chain.NewNetworkServiceEndpointRegistryServer(
		dnsresolve.NewNetworkServiceEndpointRegistryServer(),
		swap.NewNetworkServiceEndpointRegistryServer(config.Domain, &config.ProxyNSMgrURL, &config.PublicNSMgrURL),
		connect.NewNetworkServiceEndpointRegistryServer(func(ctx context.Context, cc grpc.ClientConnInterface) api_registry.NetworkServiceEndpointRegistryClient {
			return chain.NewNetworkServiceEndpointRegistryClient(api_registry.NewNetworkServiceEndpointRegistryClient(cc))
		}),
	)

	nsChain := chain.NewNetworkServiceRegistryServer(
		dnsresolve.NewNetworkServiceRegistryServer(),
		swap.NewNetworkServiceRegistryServer(config.Domain),
		connect.NewNetworkServiceRegistryServer(func(ctx context.Context, cc grpc.ClientConnInterface) api_registry.NetworkServiceRegistryClient {
			return chain.NewNetworkServiceRegistryClient(api_registry.NewNetworkServiceRegistryClient(cc))
		}),
	)

	// Create GRPC Server and register services
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsconfig.MTLSServerConfig(source, source, tlsconfig.AuthorizeAny()))))

	registry.NewServer(nsChain, nseChain).Register(server)

	srvErrCh := grpcutils.ListenAndServe(ctx, &config.ListenOn, server)
	exitOnErr(ctx, cancel, srvErrCh)
	log.Entry(ctx).Infof("Startup completed in %v", time.Since(startTime))
	<-ctx.Done()
}

func exitOnErr(ctx context.Context, cancel context.CancelFunc, errCh <-chan error) {
	// If we already have an error, log it and exit
	select {
	case err := <-errCh:
		log.Entry(ctx).Fatal(err)
	default:
	}
	// Otherwise wait for an error in the background to log and cancel
	go func(ctx context.Context, errCh <-chan error) {
		err := <-errCh
		log.Entry(ctx).Error(err)
		cancel()
	}(ctx, errCh)
}
