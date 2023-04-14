/* Copyright © 2020 VMware, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package types

const (
	NcpDefaultReplicas int = 1
	DefaultMTU         int = 1500
)

var (
	NcpSections      = []string{"DEFAULT", "ha", "k8s", "coe", "nsx_v3", "vc"}
	OperatorSections = []string{"DEFAULT", "ha", "k8s", "coe", "nsx_v3", "vc", "nsx_node_agent", "nsx_kube_proxy"}
	// AgentSections are responsible for rendering nsx-node-agent configmap, while not all keys updates in section `coe` and
	// `k8s` require restart of nsx-node-agent. So AgentRestartSections are responsible for checking whether nsx-node-agent
	// should be restarted when related keys are updated.
	AgentSections              = []string{"DEFAULT", "k8s", "coe", "nsx_node_agent", "nsx_kube_proxy"}
	AgentRestartSections       = []string{"DEFAULT", "nsx_node_agent", "nsx_kube_proxy"}
	BootstrapRestartOptionKeys = map[string][]string{
		"DEFAULT": {
			"log_dir", "log_file", "log_rotation_file_max_mb", "log_rotation_backup_count",
		},
		"nsx_node_agent": {
			"enable_ipv6", "use_nsx_ovs_kernel_module", "ovs_uplink_port", "mtu",
		},
	}
	AgentRestartOptionKeys = map[string][]string{
		"k8s": {
			"apiserver_host_ip", "apiserver_host_port", "client_token_file", "ca_file", "enable_hostport_snat",
		},
		"coe": {"connect_retry_timeout"},
	}
)

var TASSection = string("cf")

var MPOptions = map[string][]string{
	"nsx_v3": {
		"top_firewall_section_marker", "bottom_firewall_section_marker",
	},
}

var WCPOptions = map[string][]string{
	"nsx_v3": {
		"dlb_l4_persistence", "single_tier_sr_topology", "enforcement_point", "search_node_tag_on",
		"vif_app_id_type", "configure_t0_redistribution", "multi_t0",
	},
	"k8s": {
		"enable_vnet_crd", "enable_lb_monitor_crd", "enable_nsxnetworkconfig_crd", "enable_routeset_crd",
		"enable_ip_pool_crd", "enable_vm_crd", "lb_statistic_monitor_interval", "enable_lb_vs_statistics_monitor",
		"network_info_resync_period", "ip_usage_alarm_threshold",
	},
	"coe": {
		"node_type",
	},
	"vc": {"vc_endpoint", "sso_domain", "https_port"},
}
