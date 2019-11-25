/*
 *
 * Copyright 2019 gRPC authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 */

package client

import (
	"fmt"

	xdspb "github.com/envoyproxy/go-control-plane/envoy/api/v2"
	"github.com/golang/protobuf/ptypes"
	"google.golang.org/grpc/grpclog"
)

// newCDSRequest generates an CDS request proto for the provided clusterName,
// to be sent out on the wire.
func (v2c *v2Client) newCDSRequest(clusterName []string) *xdspb.DiscoveryRequest {
	return &xdspb.DiscoveryRequest{
		Node:          v2c.nodeProto,
		TypeUrl:       clusterURL,
		ResourceNames: clusterName,
	}
}

// sendCDS sends an CDS request for provided clusterName on the provided
// stream.
func (v2c *v2Client) sendCDS(stream adsStream, clusterName []string) bool {
	if err := stream.Send(v2c.newCDSRequest(clusterName)); err != nil {
		grpclog.Warningf("xds: CDS request for resource %v failed: %v", clusterName, err)
		return false
	}
	return true
}

// handleCDSResponse processes an CDS response received from the xDS server. On
// receipt of a good response, it also invokes the registered watcher callback.
func (v2c *v2Client) handleCDSResponse(resp *xdspb.DiscoveryResponse) error {
	v2c.mu.Lock()
	defer v2c.mu.Unlock()

	wi := v2c.watchMap[cdsResource]
	if wi == nil {
		return fmt.Errorf("xds: no CDS watcher found when handling CDS response: %+v", resp)
	}

	var returnUpdate cdsUpdate
	localCache := make(map[string]cdsUpdate)
	for _, r := range resp.GetResources() {
		var resource ptypes.DynamicAny
		if err := ptypes.UnmarshalAny(r, &resource); err != nil {
			return fmt.Errorf("xds: failed to unmarshal resource in CDS response: %v", err)
		}
		cluster, ok := resource.Message.(*xdspb.Cluster)
		if !ok {
			return fmt.Errorf("xds: unexpected resource type: %T in CDS response", resource.Message)
		}
		update, err := validateCluster(cluster)
		if err != nil {
			return err
		}

		// If the Cluster message in the CDS response did not contain a
		// serviceName, we will just use the clusterName for EDS.
		if update.serviceName == "" {
			update.serviceName = cluster.GetName()
		}
		localCache[cluster.GetName()] = update
		if cluster.GetName() == wi.target[0] {
			returnUpdate = update
		}
	}
	v2c.cdsCache = localCache

	var err error
	if returnUpdate.serviceName == "" {
		err = fmt.Errorf("xds: CDS target %s not found in received response %+v", wi.target, resp)
	}
	wi.stopTimer()
	wi.callback.(cdsCallback)(returnUpdate, err)
	return nil
}

func validateCluster(cluster *xdspb.Cluster) (cdsUpdate, error) {
	emptyUpdate := cdsUpdate{serviceName: "", doLRS: false}
	switch {
	case cluster.GetType() != xdspb.Cluster_EDS:
		return emptyUpdate, fmt.Errorf("xds: unexpected cluster type %v in response: %+v", cluster.GetType(), cluster)
	case cluster.GetEdsClusterConfig().GetEdsConfig().GetAds() == nil:
		return emptyUpdate, fmt.Errorf("xds: unexpected edsConfig in response: %+v", cluster)
	case cluster.GetLbPolicy() != xdspb.Cluster_ROUND_ROBIN:
		return emptyUpdate, fmt.Errorf("xds: unexpected lbPolicy %v in response: %+v", cluster.GetLbPolicy(), cluster)
	}

	return cdsUpdate{
		serviceName: cluster.GetEdsClusterConfig().GetServiceName(),
		doLRS:       cluster.GetLrsServer().GetSelf() != nil,
	}, nil
}
