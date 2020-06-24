# NSX Container Plugin Operator

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

## Overview

An operator for leveraging NSX as the default container networking solution for an
Openshift cluster. The operator will be deployed in the early phases of cluster
deployment, and it will take care of deploying NSX integration components, and
precisely:

* The NSX container plugin (NCP) deployment
* The nsx-ncp-bootstrap daemonset
* The nsx-node-agent daemonset

For Openshift 4 clusters, the nsx-container-plugin operator monitors the network
CR in the config.openshift.io namespace to update the container network CIDRs
used by NCP.

The nsx-container-plugin operator also monitors a dedicated ConfigMap, applies
changes to NCP and nsx-node-agent configuration, and restart the relevant pods
so that the relevant configuration changes are picked up.

In addition, the nsx-container-plugin operator monitors nodes ensuring the
corresponding NSX logical port is enabled as a container host logical port.

## Try it out

Generate install-config.yaml by using openshift-install command.
```
$ openshift-install --dir=MY_CLUSTER create install-config
```

Edit `MY_CLUSTER/install-config.yaml` to update networking section.
Change `networkType` to `ncp`(case insensitive).
Set container network CIDRs `clusterNetwork` in `MY_CLUSTER/install-config.yaml`.

Create manifest files:
```
$ openshift-install --dir=MY_CLUSTER create manifests
```
Put operator yaml files from `deploy/` to `MY_CLUSTER/manifests`, edit configmap.yaml
about operator configurations, add the operator image and NCP image in operator.yaml.

Generate ignition configuration files:
```
$ openshift-install --dir=MY_CLUSTER create ignition-configs
```
This bootstrap ignition file will be added to the terraform tfvars.
Then use terraform to install Openshift 4 cluster on vSphere.

### Build & Run

Building the nsx-container-plugin operator is very simple. From the project root
directory simply type the following command.

```
docker build -f build/Dockerfile .
```

At the moment the nsx-container-plugin operator only works on Openshift 4
environments

## Documentation

### Cluster network config
Cluster network config is initially set in install-config.yaml, user could apply
`Network.config.openshift.io` CRD to update `clusterNetwork` in `manifests/cluster-network-02-config.yml`.
*Example configurations*
```
apiVersion: config.openshift.io/v1
kind: Network
metadata:
  name: cluster
spec:
  clusterNetwork:
  - cidr: 10.10.0.0/14
  networkType: ncp
```

### Operator ConfigMap
Operator ConfigMap `nsx-ncp-operator-config` is used to provide NCP configurations.
As for now we only support NSX Policy API, single Tier topology on Openshift 4,
the operator sets `policy_nsxapi` as True, `single_tier_topology` as True.
In the ConfigMap, some fields are mandatory including `cluster`, `nsx_api_managers`,
`tier0_gateway`(for single T1 case), `top_tier_router`(for single T0 case),
`external_ip_pools`(for SNAT mode). If any of above options is not provided in the
operator ConfigMap, the operator will fail to reconcile configurations, error messages
will be added in clusteroperator nsx-ncp Degraded conditions.

### NCP Image
User needs to set NCP image as an environment parameter `NCP_IMAGE` in `deploy/operator.yaml`.

### Unsafe changes
If CIDRs in `clusterNetwork` are already applied, it is unsafe to remove them.
NSX NCP operator won't fail when it detects some existing network CIDRs are deleted,
but the removal may cause unexpected issues.

## Contributing

We welcome community contributions to the NSX Container plugin Operator!
Before you start working with nsx-container-plugin-operator, please read our 
[Developer Certificate of Origin](https://cla.vmware.com/dco). All contributions to this repository must be
signed as described on that page. Your signature certifies that you wrote the patch or have the right to pass it on
as an open-source patch. For more detailed information, refer to [CONTRIBUTING.md](CONTRIBUTING.md).

## License

This repository is available under the [Apache 2.0 license](LICENSE).
