/* Copyright © 2020 VMware, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package configmap

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/pkg/errors"
	"github.com/vmware/nsx-container-plugin-operator/pkg/controller/sharedinfo"
	"github.com/vmware/nsx-container-plugin-operator/pkg/controller/statusmanager"
	operatortypes "github.com/vmware/nsx-container-plugin-operator/pkg/types"

	"gopkg.in/ini.v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubectl/pkg/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func init() {
	configv1.AddToScheme(scheme.Scheme)
}

func createMockConfigMap() *corev1.ConfigMap {
	mockConfigMap := &corev1.ConfigMap{Data: map[string]string{}}
	data := &mockConfigMap.Data
	cfg, _ := ini.Load([]byte((*data)[operatortypes.ConfigMapDataKey]))
	cfg.NewSections(operatortypes.OperatorSections...)
	(*data)[operatortypes.ConfigMapDataKey], _ = iniWriteToString(cfg)

	return mockConfigMap
}

func createMockNetworkSpec(cidrs []string) *configv1.NetworkSpec {
	mockSpec := &configv1.NetworkSpec{ClusterNetwork: []configv1.ClusterNetworkEntry{}}
	for _, cidr := range cidrs {
		entry := configv1.ClusterNetworkEntry{CIDR: cidr}
		mockSpec.ClusterNetwork = append(mockSpec.ClusterNetwork, entry)
	}
	return mockSpec
}

func getTestReconcileConfigMap(t string) *ReconcileConfigMap {
	client := fake.NewFakeClient()
	namespace := "operator-namespace"
	mapper := &statusmanager.FakeRESTMapper{}
	sharedInfo := &sharedinfo.SharedInfo{
		AdaptorName: t,
	}
	status := statusmanager.New(
		client, mapper, "testing", "1.2.3", namespace, sharedInfo)
	sharedInfo.NetworkConfig = &configv1.Network{}
	reconcileConfigMap := ReconcileConfigMap{
		client:     client,
		status:     status,
		sharedInfo: sharedInfo,
	}
	if t == "openshift4" {
		reconcileConfigMap.Adaptor = &ConfigMapOc{}
	} else {
		reconcileConfigMap.Adaptor = &ConfigMapK8s{}
	}
	return &reconcileConfigMap
}

func TestFillDefaults(t *testing.T) {
	cidrs := []string{"10.0.0.0/24"}
	mockOcConfigMap := createMockConfigMap()
	mockNetworkSpec := createMockNetworkSpec(cidrs)
	data := &mockOcConfigMap.Data

	r := getTestReconcileConfigMap("openshift4")

	err := r.FillDefaults(mockOcConfigMap, mockNetworkSpec)
	if err != nil {
		t.Fatalf("failed to fill default config")
	}
	cfg, err := ini.Load([]byte((*data)[operatortypes.ConfigMapDataKey]))
	assert.Equal(t, "openshift4", cfg.Section("coe").Key("adaptor").Value())
	assert.Equal(t, "True", cfg.Section("nsx_v3").Key("policy_nsxapi").Value())
	assert.Equal(t, "True", cfg.Section("nsx_v3").Key("single_tier_topology").Value())
	assert.Equal(t, "True", cfg.Section("coe").Key("enable_snat").Value())
	assert.Equal(t, "True", cfg.Section("ha").Key("enable").Value())
	assert.Equal(t, "10.0.0.0/24", cfg.Section("nsx_v3").Key("container_ip_blocks").Value())
	assert.Equal(t, "3", cfg.Section("nsx_node_agent").Key("waiting_before_cni_response").Value())
	assert.Equal(t, "1500", cfg.Section("nsx_node_agent").Key("mtu").Value())
	assert.Equal(t, "false", cfg.Section("nsx_node_agent").Key("enable_ovs_mcast_snooping").Value())

	mockConfigMap := createMockConfigMap()
	data = &mockConfigMap.Data
	r = getTestReconcileConfigMap("kubernetes")
	err = r.FillDefaults(mockConfigMap, mockNetworkSpec)
	if err != nil {
		t.Fatalf("failed to fill default config")
	}
	cfg, err = ini.Load([]byte((*data)[operatortypes.ConfigMapDataKey]))
	assert.Equal(t, "kubernetes", cfg.Section("coe").Key("adaptor").Value())
	assert.Equal(t, "True", cfg.Section("nsx_v3").Key("policy_nsxapi").Value())
	assert.Equal(t, "", cfg.Section("nsx_v3").Key("single_tier_topology").Value())
	assert.Equal(t, "True", cfg.Section("coe").Key("enable_snat").Value())
	assert.Equal(t, "True", cfg.Section("ha").Key("enable").Value())
	assert.Equal(t, "", cfg.Section("nsx_v3").Key("container_ip_blocks").Value())
	assert.Equal(t, "", cfg.Section("nsx_node_agent").Key("waiting_before_cni_response").Value())
	assert.Equal(t, "1500", cfg.Section("nsx_node_agent").Key("mtu").Value())
	assert.Equal(t, "", cfg.Section("nsx_node_agent").Key("enable_ovs_mcast_snooping").Value())
}

func TestAppendErrorIfNotNil(t *testing.T) {
	errs := &[]error{}
	err := error(nil)
	appendErrorIfNotNil(errs, err)
	assert.Empty(t, *errs)

	err = errors.Errorf("test error")
	appendErrorIfNotNil(errs, err)
	assert.Equal(t, 1, len(*errs))
}

func TestFillDefault(t *testing.T) {
	cfg := ini.Empty()
	cfg.NewSections(operatortypes.OperatorSections...)

	fillDefault(cfg, "DEFAULT", "debug", "True", false)
	assert.Equal(t, "True", cfg.Section("DEFAULT").Key("debug").Value())

	fillDefault(cfg, "DEFAULT", "debug", "False", false)
	assert.Equal(t, "True", cfg.Section("DEFAULT").Key("debug").Value())

	fillDefault(cfg, "DEFAULT", "debug", "False", true)
	assert.Equal(t, "False", cfg.Section("DEFAULT").Key("debug").Value())
}

func TestFillClusterNetwork(t *testing.T) {
	cidrs := []string{"10.0.0.0/16", "20.0.0.0/14"}
	mockConfigMap := createMockConfigMap()
	mockNetworkSpec := createMockNetworkSpec(cidrs)
	data := &mockConfigMap.Data
	cfg, _ := ini.Load([]byte((*data)[operatortypes.ConfigMapDataKey]))

	fillClusterNetwork(mockNetworkSpec, cfg)
	assert.Equal(t, "10.0.0.0/16,20.0.0.0/14", cfg.Section("nsx_v3").Key("container_ip_blocks").Value())
}

func TestValidate(t *testing.T) {
	mockConfigMap := &corev1.ConfigMap{}
	mockNetworkSpec := &configv1.NetworkSpec{}
	r := getTestReconcileConfigMap("openshift4")
	err := r.Validate(mockConfigMap, mockNetworkSpec)
	assert.NotNil(t, err)
}

func TestValidateConfig(t *testing.T) {
	cfg := ini.Empty()

	err := validateConfig(cfg, "testSec", "testKey")
	assert.NotNil(t, err)

	cfg.NewSection("testSec")
	err = validateConfig(cfg, "testSec", "testKey")
	assert.NotNil(t, err)

	cfg.Section("testSec").NewKey("testKey", "testValue")
	err = validateConfig(cfg, "testSec", "testKey")
	assert.Nil(t, err)
}

func TestValidateConfigMap(t *testing.T) {
	mockConfigMap := createMockConfigMap()

	r := getTestReconcileConfigMap("openshift4")
	errs := r.validateConfigMap(mockConfigMap)
	assert.Equal(t, 4, len(errs))

	data := &mockConfigMap.Data
	cfg, _ := ini.Load([]byte((*data)[operatortypes.ConfigMapDataKey]))
	cfg.Section("coe").NewKey("adaptor", "openshift4")
	cfg.Section("coe").NewKey("cluster", "mockCluster")
	cfg.Section("nsx_v3").NewKey("nsx_api_managers", "mockIP")
	cfg.Section("coe").NewKey("enable_snat", "False")
	cfg.Section("nsx_v3").NewKey("tier0_gateway", "mockT0")
	cfg.Section("nsx_node_agent").NewKey("mtu", "1500")
	(*data)[operatortypes.ConfigMapDataKey], _ = iniWriteToString(cfg)

	errs = r.validateConfigMap(mockConfigMap)
	assert.Empty(t, errs)
}

func TestValidateNodeAgentOptions(t *testing.T) {
	mockConfigMap := createMockConfigMap()
	data := &mockConfigMap.Data
	cfg, _ := ini.Load([]byte((*data)[operatortypes.ConfigMapDataKey]))
	cfg.Section("nsx_node_agent").NewKey("mtu", "1500")
	(*data)[operatortypes.ConfigMapDataKey], _ = iniWriteToString(cfg)

	errs := validateNodeAgentOptions(mockConfigMap)
	assert.Empty(t, errs)

	fillDefault(cfg, "nsx_node_agent", "mtu", "s120", true)
	(*data)[operatortypes.ConfigMapDataKey], _ = iniWriteToString(cfg)
	errs = validateNodeAgentOptions(mockConfigMap)
	assert.Equal(t, 1, len(errs))
}

func TestValidateCommonOptions(t *testing.T) {
	mockConfigMap := createMockConfigMap()
	data := &mockConfigMap.Data
	cfg, _ := ini.Load([]byte((*data)[operatortypes.ConfigMapDataKey]))
	cfg.Section("DEFAULT").NewKey("log_file", "test.log")
	(*data)[operatortypes.ConfigMapDataKey], _ = iniWriteToString(cfg)

	errs := validateCommonOptions(mockConfigMap)
	cfg, _ = ini.Load([]byte((*data)[operatortypes.ConfigMapDataKey]))
	// assert.Empty(t, errs)
	// assert.Equal(t, "", cfg.Section("DEFAULT").Key("log_file").Value())
	assert.Equal(t, 1, len(errs))
	assert.Equal(t, "test.log", cfg.Section("DEFAULT").Key("log_file").Value())
}

func TestValidateUnSupportedOptions(t *testing.T) {
	mockConfigMap := createMockConfigMap()
	data := &mockConfigMap.Data
	cfg, _ := ini.Load([]byte((*data)[operatortypes.ConfigMapDataKey]))

	// Test MP specific option validation
	fillDefault(cfg, "nsx_v3", "policy_nsxapi", "True", true)
	fillDefault(cfg, "nsx_v3", "top_firewall_section_marker", "top", true)
	fillDefault(cfg, "nsx_v3", "bottom_firewall_section_marker", "bottom", true)
	(*data)[operatortypes.ConfigMapDataKey], _ = iniWriteToString(cfg)
	errs := validateUnSupportedOptions(mockConfigMap)
	cfg, _ = ini.Load([]byte((*data)[operatortypes.ConfigMapDataKey]))
	assert.Empty(t, errs)
	assert.False(t, cfg.Section("nsx_v3").HasKey("top_firewall_section_marker"))
	assert.False(t, cfg.Section("nsx_v3").HasKey("bottom_firewall_section_marker"))

	// Test WCP specific option validation
	fillDefault(cfg, "nsx_v3", "dlb_l4_persistence", "cookie", true)
	// add one non-wcp option
	fillDefault(cfg, "nsx_v3", "l4_persistence", "cookie", true)
	fillDefault(cfg, "nsx_v3", "multi_t0", "False", true)
	fillDefault(cfg, "k8s", "enable_vnet_crd", "True", true)
	fillDefault(cfg, "k8s", "ip_usage_alarm_threshold", "0.8", true)
	fillDefault(cfg, "coe", "node_type", "HOSTVM", true)
	fillDefault(cfg, "vc", "vc_endpoint", "10.10.10.1", true)
	fillDefault(cfg, "vc", "https_port", "8181", true)
	(*data)[operatortypes.ConfigMapDataKey], _ = iniWriteToString(cfg)
	cfg, _ = ini.Load([]byte((*data)[operatortypes.ConfigMapDataKey]))
	assert.True(t, cfg.Section("nsx_v3").HasKey("dlb_l4_persistence"))
	assert.True(t, cfg.Section("nsx_v3").HasKey("multi_t0"))
	assert.True(t, cfg.Section("k8s").HasKey("enable_vnet_crd"))
	assert.True(t, cfg.Section("k8s").HasKey("ip_usage_alarm_threshold"))
	assert.True(t, cfg.Section("coe").HasKey("node_type"))
	assert.True(t, cfg.Section("vc").HasKey("vc_endpoint"))
	assert.True(t, cfg.Section("vc").HasKey("https_port"))
	assert.True(t, cfg.Section("nsx_v3").HasKey("l4_persistence"))

	errs = validateUnSupportedOptions(mockConfigMap)
	cfg, _ = ini.Load([]byte((*data)[operatortypes.ConfigMapDataKey]))
	assert.Empty(t, errs)
	assert.False(t, cfg.Section("nsx_v3").HasKey("dlb_l4_persistence"))
	assert.False(t, cfg.Section("nsx_v3").HasKey("multi_t0"))
	assert.False(t, cfg.Section("k8s").HasKey("enable_vnet_crd"))
	assert.False(t, cfg.Section("k8s").HasKey("node_type"))
	assert.False(t, cfg.Section("vc").HasKey("vc_endpoint"))
	assert.False(t, cfg.Section("vc").HasKey("https_port"))
	assert.True(t, cfg.Section("nsx_v3").HasKey("l4_persistence"))

	// Test TAS specific option validation
	cfg.Section(operatortypes.TASSection).NewKey("bbs_poll_interval", "1")
	(*data)[operatortypes.ConfigMapDataKey], _ = iniWriteToString(cfg)

	errs = validateUnSupportedOptions(mockConfigMap)
	cfg, _ = ini.Load([]byte((*data)[operatortypes.ConfigMapDataKey]))
	assert.Empty(t, errs)
	sec, err := cfg.GetSection(operatortypes.TASSection)
	assert.Nil(t, sec)
	assert.NotNil(t, err)
}

func TestValidateClusterNetwork(t *testing.T) {
	mockNetworkSpec := &configv1.NetworkSpec{}
	r := getTestReconcileConfigMap("openshift4")
	errs := r.validateClusterNetwork(mockNetworkSpec)
	assert.Equal(t, 1, len(errs))

	mockNetworkSpec.NetworkType = "ncp"
	errs = r.validateClusterNetwork(mockNetworkSpec)
	assert.Equal(t, 1, len(errs))

	mockNetworkSpec.ClusterNetwork = []configv1.ClusterNetworkEntry{
		{CIDR: "mockCIDR"},
	}
	errs = r.validateClusterNetwork(mockNetworkSpec)
	assert.Equal(t, 1, len(errs))

	mockNetworkSpec.ClusterNetwork = []configv1.ClusterNetworkEntry{
		{CIDR: "10.0.0.0/31"},
	}
	errs = r.validateClusterNetwork(mockNetworkSpec)
	assert.Equal(t, 2, len(errs))

	mockNetworkSpec.ClusterNetwork = []configv1.ClusterNetworkEntry{
		{
			CIDR:       "10.0.0.0/16",
			HostPrefix: uint32(12),
		},
	}
	errs = r.validateClusterNetwork(mockNetworkSpec)
	assert.Equal(t, 1, len(errs))

	mockNetworkSpec.ClusterNetwork = []configv1.ClusterNetworkEntry{
		{
			CIDR:       "10.0.0.0/16",
			HostPrefix: uint32(24),
		},
	}
	errs = r.validateClusterNetwork(mockNetworkSpec)
	assert.Empty(t, errs)
}

func TestRender(t *testing.T) {
	mockConfigMap := createMockConfigMap()
	var nsxSecret *corev1.Secret
	var lbSecret *corev1.Secret
	var ncpReplicas int32 = 1
	var ncpNodeSelector *map[string]string
	var ncpTolerations *[]corev1.Toleration
	var nsxNodeAgentDsTolerations *[]corev1.Toleration

	objs, err := Render(mockConfigMap, &ncpReplicas, ncpNodeSelector,
		ncpTolerations, nsxNodeAgentDsTolerations,
		nsxSecret, lbSecret)
	assert.Empty(t, objs)
	assert.Error(t, err, "failed to render manifests")
}

func TestNeedApplyChange(t *testing.T) {
	currConfigMap := createMockConfigMap()
	var prevConfigMap *corev1.ConfigMap = nil
	needChange, err := NeedApplyChange(currConfigMap, prevConfigMap)
	assert.True(t, needChange.ncp)
	assert.True(t, needChange.agent)
	assert.True(t, needChange.bootstrap)
	assert.Nil(t, err)

	prevConfigMap = createMockConfigMap()
	needChange, err = NeedApplyChange(currConfigMap, prevConfigMap)
	assert.False(t, needChange.ncp)
	assert.False(t, needChange.agent)
	assert.False(t, needChange.bootstrap)
	assert.Nil(t, err)

	preData := &prevConfigMap.Data
	preCfg, _ := ini.Load([]byte((*preData)[operatortypes.ConfigMapDataKey]))

	preCfg.Section("nsx_node_agent").NewKey("ovs_uplink_port", "eth1")
	preCfg.Section("nsx_node_agent").NewKey("mtu", "1600")
	(*preData)[operatortypes.ConfigMapDataKey], _ = iniWriteToString(preCfg)
	needChange, err = NeedApplyChange(currConfigMap, prevConfigMap)
	assert.False(t, needChange.ncp)
	assert.True(t, needChange.agent)
	assert.True(t, needChange.bootstrap)
	assert.Nil(t, err)

	preCfg.Section("nsx_node_agent").DeleteKey("ovs_uplink_port")
	preCfg.Section("nsx_node_agent").DeleteKey("mtu")
	preCfg.Section("k8s").NewKey("loglevel", "DEBUG")

	(*preData)[operatortypes.ConfigMapDataKey], _ = iniWriteToString(preCfg)
	needChange, err = NeedApplyChange(currConfigMap, prevConfigMap)
	assert.True(t, needChange.ncp)
	assert.False(t, needChange.agent)
	assert.False(t, needChange.bootstrap)
	assert.Nil(t, err)

	preCfg.Section("nsx_node_agent").NewKey("ovs_uplink_port", "eth1")
	preCfg.Section("nsx_node_agent").NewKey("mtu", "1600")
	(*preData)[operatortypes.ConfigMapDataKey], _ = iniWriteToString(preCfg)

	curData := &currConfigMap.Data
	curCfg, _ := ini.Load([]byte((*curData)[operatortypes.ConfigMapDataKey]))
	curCfg.Section("nsx_node_agent").NewKey("ovs_uplink_port", "eth2")
	curCfg.Section("nsx_node_agent").NewKey("mtu", "1500")
	curCfg.Section("nsx_v3").NewKey("external_ip_pools_lb", "10.30.0.0/16, IP_1-IP_2")
	(*curData)[operatortypes.ConfigMapDataKey], _ = iniWriteToString(curCfg)
	needChange, err = NeedApplyChange(currConfigMap, prevConfigMap)
	// k8s section remove "loglevel" and nsx_v3 section add "external_ip_pools_lb"
	assert.True(t, needChange.ncp)
	assert.True(t, needChange.agent)
	assert.True(t, needChange.bootstrap)
	assert.Nil(t, err)

	preCfg.DeleteSection("k8s")
	preCfg.Section("nsx_v3").NewKey("external_ip_pools_lb", "10.30.0.0/16, IP_1-IP_2")
	curCfg.Section("k8s").NewKey("loglevel", "INFO")
	curCfg.DeleteSection("nsx_node_agent")
	(*preData)[operatortypes.ConfigMapDataKey], _ = iniWriteToString(preCfg)
	(*curData)[operatortypes.ConfigMapDataKey], _ = iniWriteToString(curCfg)
	needChange, err = NeedApplyChange(currConfigMap, prevConfigMap)
	// k8s section removed
	assert.True(t, needChange.ncp)
	// nsx_node_agent section removed
	assert.True(t, needChange.agent)
	assert.True(t, needChange.bootstrap)
	assert.Nil(t, err)

	preCfg.Section("coe").NewKey("cluster", "cluster-1")
	curCfg.Section("coe").NewKey("cluster", "cluster-1")
	(*preData)[operatortypes.ConfigMapDataKey], _ = iniWriteToString(preCfg)
	(*curData)[operatortypes.ConfigMapDataKey], _ = iniWriteToString(curCfg)
	needChange, err = NeedApplyChange(currConfigMap, prevConfigMap)
	preCfg.Section("coe").DeleteKey("cluster")
	curCfg.Section("coe").DeleteKey("cluster")
	assert.Nil(t, err)

	preCfg.Section("coe").NewKey("cluster", "cluster-1")
	curCfg.Section("coe").NewKey("cluster", "cluster-2")
	(*preData)[operatortypes.ConfigMapDataKey], _ = iniWriteToString(preCfg)
	(*curData)[operatortypes.ConfigMapDataKey], _ = iniWriteToString(curCfg)
	needChange, err = NeedApplyChange(currConfigMap, prevConfigMap)
	preCfg.Section("coe").DeleteKey("cluster")
	curCfg.Section("coe").DeleteKey("cluster")
	assert.NotNil(t, err)

	preCfg.Section("nsx_v3").NewKey("l4_lb_auto_scaling", "True")
	curCfg.Section("nsx_v3").NewKey("l4_lb_auto_scaling", "True")
	(*preData)[operatortypes.ConfigMapDataKey], _ = iniWriteToString(preCfg)
	(*curData)[operatortypes.ConfigMapDataKey], _ = iniWriteToString(curCfg)
	needChange, err = NeedApplyChange(currConfigMap, prevConfigMap)
	preCfg.Section("nsx_v3").DeleteKey("l4_lb_auto_scaling")
	curCfg.Section("nsx_v3").DeleteKey("l4_lb_auto_scaling")
	assert.Nil(t, err)

	preCfg.Section("nsx_v3").NewKey("l4_lb_auto_scaling", "True")
	curCfg.Section("nsx_v3").NewKey("l4_lb_auto_scaling", "False")
	(*preData)[operatortypes.ConfigMapDataKey], _ = iniWriteToString(preCfg)
	(*curData)[operatortypes.ConfigMapDataKey], _ = iniWriteToString(curCfg)
	needChange, err = NeedApplyChange(currConfigMap, prevConfigMap)
	preCfg.Section("nsx_v3").DeleteKey("l4_lb_auto_scaling")
	curCfg.Section("nsx_v3").DeleteKey("l4_lb_auto_scaling")
	assert.NotNil(t, err)
}

func TestInSlice(t *testing.T) {
	str := "test"
	strs := []string{"a", "b"}
	assert.False(t, inSlice(str, strs))

	strs = []string{"a", "b", "test"}
	assert.True(t, inSlice(str, strs))
}

func TestStringSliceEqual(t *testing.T) {
	slice1 := []string{"a", "b"}
	slice2 := []string{"b", "a"}
	assert.True(t, stringSliceEqual(slice1, slice2))
	slice1 = []string{"b", "c"}
	assert.False(t, stringSliceEqual(slice1, slice2))
	slice1 = []string{"x", "y"}
	assert.False(t, stringSliceEqual(slice1, slice2))
	slice2 = []string{"x", "y"}
	assert.True(t, stringSliceEqual(slice1, slice2))
}

func TestGenerateConfigMap(t *testing.T) {
	cfg := ini.Empty()
	cfg.NewSections("sec1", "sec2", "sec3")
	cfg.Section("sec1").NewKey("key1", "val1")
	cfg.Section("sec2").NewKey("key2", "val2")
	cfg.Section("sec3").NewKey("key3", "val3")

	iniString, _ := generateConfigMap(cfg, []string{"sec1", "sec2"})
	destCfg, _ := ini.Load([]byte(iniString))
	assert.Equal(t, []string{"DEFAULT", "sec1", "sec2"}, destCfg.SectionStrings())
	assert.Equal(t, "val1", destCfg.Section("sec1").Key("key1").Value())
	assert.Equal(t, "val2", destCfg.Section("sec2").Key("key2").Value())
	sec, _ := destCfg.GetSection("sec3")
	assert.Nil(t, sec)
}

func TestGenerateOperatorConfigMap(t *testing.T) {
	opConfigMap := createMockConfigMap()
	ncpConfigMap := createMockConfigMap()
	data := &ncpConfigMap.Data
	cfg, _ := ini.Load([]byte((*data)[operatortypes.ConfigMapDataKey]))
	cfg.DeleteSection("nsx_node_agent")
	cfg.Section("nsx_v3").NewKey("nsx_api_managers", "mockIP")
	(*data)[operatortypes.ConfigMapDataKey], _ = iniWriteToString(cfg)
	agentConfigMap := createMockConfigMap()
	data = &agentConfigMap.Data
	cfg, _ = ini.Load([]byte((*data)[operatortypes.ConfigMapDataKey]))
	cfg.DeleteSection("nsx_v3")
	cfg.Section("nsx_node_agent").NewKey("ovs_uplink_port", "eth1")
	(*data)[operatortypes.ConfigMapDataKey], _ = iniWriteToString(cfg)

	GenerateOperatorConfigMap(opConfigMap, ncpConfigMap, agentConfigMap)
	data = &opConfigMap.Data
	cfg, _ = ini.Load([]byte((*data)[operatortypes.ConfigMapDataKey]))
	assert.Equal(t, "mockIP", cfg.Section("nsx_v3").Key("nsx_api_managers").Value())
	assert.Equal(t, "eth1", cfg.Section("nsx_node_agent").Key("ovs_uplink_port").Value())
}

func TestIniWriteToString(t *testing.T) {
	cfg := ini.Empty()
	var buf bytes.Buffer
	cfg.WriteTo(&buf)
	expStr := "\n[DEFAULT]\n" + buf.String()
	retStr, _ := iniWriteToString(cfg)
	assert.Equal(t, expStr, retStr)
}

func TestOptionInConfigMap(t *testing.T) {
	mockConfigMap := createMockConfigMap()
	data := &mockConfigMap.Data
	cfg, _ := ini.Load([]byte((*data)[operatortypes.ConfigMapDataKey]))
	cfg.Section("nsx_v3").NewKey("nsx_api_cert_file", "mock_cert")
	cfg.Section("nsx_v3").NewKey("mock_key", "")
	(*data)[operatortypes.ConfigMapDataKey], _ = iniWriteToString(cfg)

	assert.True(t, optionInConfigMap(mockConfigMap, "nsx_v3", "nsx_api_cert_file"))
	assert.False(t, optionInConfigMap(mockConfigMap, "nsx_v3", "lb_default_cert_path"))
	assert.False(t, optionInConfigMap(mockConfigMap, "nsx_v3", "mock_key"))
}

func TestGetOptionInConfigMap(t *testing.T) {
	mockConfigMap := createMockConfigMap()
	data := &mockConfigMap.Data
	cfg, _ := ini.Load([]byte((*data)[operatortypes.ConfigMapDataKey]))
	cfg.Section("nsx_node_agent").NewKey("mtu", "1500")
	(*data)[operatortypes.ConfigMapDataKey], _ = iniWriteToString(cfg)

	assert.Equal(t, "1500", getOptionInConfigMap(mockConfigMap, "nsx_node_agent", "mtu"))
	assert.Equal(t, "", getOptionInConfigMap(mockConfigMap, "nsx_node_agent", "test"))
}

func TestIsMTUChanged(t *testing.T) {
	currConfigMap := createMockConfigMap()
	prevConfigMap := createMockConfigMap()
	assert.False(t, IsMTUChanged(currConfigMap, prevConfigMap))

	data := &currConfigMap.Data
	cfg, _ := ini.Load([]byte((*data)[operatortypes.ConfigMapDataKey]))
	cfg.Section("nsx_node_agent").NewKey("mtu", "1600")
	(*data)[operatortypes.ConfigMapDataKey], _ = iniWriteToString(cfg)
	assert.True(t, IsMTUChanged(currConfigMap, prevConfigMap))
}

func TestFillNsxAuthCfg(t *testing.T) {
	mockConfigMap := createMockConfigMap()
	data := &mockConfigMap.Data

	mockValue := []byte("mockCrt")
	nsxSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nsx-secret",
			Namespace: "nsx-system-operator",
		},
		Data: map[string][]byte{
			"tls.crt": mockValue, "tls.key": mockValue, "tls.ca": mockValue,
		},
	}

	FillNsxAuthCfg(mockConfigMap, nsxSecret)
	cfg, _ := ini.Load([]byte((*data)[operatortypes.ConfigMapDataKey]))

	assert.Equal(t, "/etc/nsx-ujo/nsx-cert/tls.crt", cfg.Section("nsx_v3").Key("nsx_api_cert_file").Value())
	assert.Equal(t, "/etc/nsx-ujo/nsx-cert/tls.key", cfg.Section("nsx_v3").Key("nsx_api_private_key_file").Value())
	assert.Equal(t, "/etc/nsx-ujo/nsx-cert/tls.ca", cfg.Section("nsx_v3").Key("ca_file").Value())

	// Secret with empty value
	mockSecret := &corev1.Secret{
		Data: map[string][]byte{"tls.crt": {}, "tls.key": {}, "tls.ca": {}},
	}
	fillDefault(cfg, "nsx_v3", "nsx_api_cert_file", "", true)
	fillDefault(cfg, "nsx_v3", "nsx_api_private_key_file", "", true)
	fillDefault(cfg, "nsx_v3", "ca_file", "", true)
	(*data)[operatortypes.ConfigMapDataKey], _ = iniWriteToString(cfg)
	FillNsxAuthCfg(mockConfigMap, mockSecret)
	cfg, _ = ini.Load([]byte((*data)[operatortypes.ConfigMapDataKey]))
	assert.Equal(t, "", cfg.Section("nsx_v3").Key("nsx_api_cert_file").Value())
	assert.Equal(t, "", cfg.Section("nsx_v3").Key("nsx_api_private_key_file").Value())
	assert.Equal(t, "", cfg.Section("nsx_v3").Key("ca_file").Value())
}

func TestFillLbCertCfg(t *testing.T) {
	mockConfigMap := createMockConfigMap()
	data := &mockConfigMap.Data

	mockValue := []byte("mockCrt")
	lbSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "lb-secret",
			Namespace: "nsx-system-operator",
		},
		Data: map[string][]byte{
			"tls.crt": mockValue, "tls.key": mockValue,
		},
	}

	FillLbCertCfg(mockConfigMap, lbSecret)
	cfg, _ := ini.Load([]byte((*data)[operatortypes.ConfigMapDataKey]))
	assert.Equal(t, "/etc/nsx-ujo/lb-cert/tls.crt", cfg.Section("nsx_v3").Key("lb_default_cert_path").Value())
	assert.Equal(t, "/etc/nsx-ujo/lb-cert/tls.key", cfg.Section("nsx_v3").Key("lb_priv_key_path").Value())

	// Secret with empty value
	mockSecret := &corev1.Secret{
		Data: map[string][]byte{"tls.crt": {}, "tls.key": {}},
	}
	fillDefault(cfg, "nsx_v3", "lb_default_cert_path", "", true)
	fillDefault(cfg, "nsx_v3", "lb_priv_key_path", "", true)
	(*data)[operatortypes.ConfigMapDataKey], _ = iniWriteToString(cfg)
	FillLbCertCfg(mockConfigMap, mockSecret)
	cfg, _ = ini.Load([]byte((*data)[operatortypes.ConfigMapDataKey]))
	assert.Equal(t, "", cfg.Section("nsx_v3").Key("lb_default_cert_path").Value())
	assert.Equal(t, "", cfg.Section("nsx_v3").Key("lb_priv_key_path").Value())
}
