/*
Copyright 2019 Gravitational, Inc.

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

package clusterconfig

import (
	"github.com/gravitational/gravity/lib/app"
	"github.com/gravitational/gravity/lib/ops"
	"github.com/gravitational/gravity/lib/storage"
	"github.com/gravitational/gravity/lib/storage/clusterconfig"
	"github.com/gravitational/gravity/lib/update"
	"github.com/gravitational/gravity/lib/update/internal/rollingupdate"

	"github.com/gravitational/trace"
)

// NewOperationPlan creates a new operation plan for the specified operation
func NewOperationPlan(
	operator ops.Operator,
	apps app.Applications,
	operation ops.SiteOperation,
	clusterConfig clusterconfig.Interface,
	servers []storage.Server,
) (plan *storage.OperationPlan, err error) {
	cluster, err := operator.GetLocalSite()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	app, err := apps.GetApp(cluster.App.Package)
	if err != nil {
		return nil, trace.Wrap(err, "failed to query installed application")
	}
	plan, err = newOperationPlan(*app, cluster.DNSConfig, operator, operation, clusterConfig, servers)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	err = operator.CreateOperationPlan(operation.Key(), *plan)
	if err != nil {
		if trace.IsNotFound(err) {
			return nil, trace.NotImplemented(
				"cluster operator does not implement the API required to update cluster configuration. " +
					"Please make sure you're running the command on a compatible cluster.")
		}
		return nil, trace.Wrap(err)
	}
	return plan, nil
}

// newOperationPlan returns a new plan for the specified operation
// and the given set of servers
func newOperationPlan(
	app app.Application,
	dnsConfig storage.DNSConfig,
	operator rollingupdate.ConfigPackageRotator,
	operation ops.SiteOperation,
	clusterConfig clusterconfig.Interface,
	servers []storage.Server,
) (*storage.OperationPlan, error) {
	builder := rollingupdate.Builder{App: app.Package}
	updates, err := rollingupdate.RuntimeConfigUpdates(app.Manifest, operator, operation.Key(), servers)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	masters, nodes := update.SplitServers(updates)
	if len(masters) == 0 {
		return nil, trace.NotFound("no master servers found in cluster state")
	}
	shouldUpdateNodes := shouldUpdateNodes(clusterConfig, len(nodes))
	updateServers := updates
	if !shouldUpdateNodes {
		updateServers = masters
	}
	config := *builder.Config("Update runtime configuration", updateServers)
	updateMasters := *builder.Masters(
		masters,
		"Update cluster configuration",
		"Update configuration on node %q",
	).Require(config)
	phases := update.Phases{config, updateMasters}

	if shouldUpdateNodes {
		updateNodes := *builder.Nodes(
			nodes, masters[0].Server,
			"Update cluster configuration",
			"Update configuration on node %q",
		).Require(config, updateMasters)
		phases = append(phases, updateNodes)
	}

	plan := &storage.OperationPlan{
		OperationID:   operation.ID,
		OperationType: operation.Type,
		AccountID:     operation.AccountID,
		ClusterName:   operation.SiteDomain,
		Phases:        phases.AsPhases(),
		Servers:       servers,
		DNSConfig:     dnsConfig,
	}
	update.ResolvePlan(plan)

	return plan, nil
}

func shouldUpdateNodes(clusterConfig clusterconfig.Interface, numWorkerNodes int) bool {
	if numWorkerNodes == 0 {
		return false
	}
	var hasComponentUpdate, hasCIDRUpdate bool
	config := clusterConfig.GetGlobalConfig()
	hasComponentUpdate = len(config.FeatureGates) != 0
	hasCIDRUpdate = len(config.PodCIDR) != 0 || len(config.ServiceCIDR) != 0
	return !clusterConfig.GetKubeletConfig().IsEmpty() || hasComponentUpdate || hasCIDRUpdate
}
