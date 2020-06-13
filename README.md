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

TBD

### Build & Run

Building the nsx-container-plugin operator is very simple. From the project root
directory simply type the following command.

```
docker build -f build/Dockerfile .
```

At the moment the nsx-container-plugin operator only works on Openshift 4
environments

## Documentation

TBD

## Contributing

We welcome community contributions to the NSX Container plugin Operator!
Before you start working with nsx-container-plugin-operator, please read our 
[Developer Certificate of Origin](https://cla.vmware.com/dco). All contributions to this repository must be
signed as described on that page. Your signature certifies that you wrote the patch or have the right to pass it on
as an open-source patch. For more detailed information, refer to [CONTRIBUTING.md](CONTRIBUTING.md).

## License

This repository is available under the [Apache 2.0 license](LICENSE).
